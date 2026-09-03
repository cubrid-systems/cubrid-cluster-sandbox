package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/record"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/store"
)

// Version is stamped at build time: -ldflags "-X ...cli.Version=..."
var Version = "dev"

// Ctx is what a command is handed. Everything a command needs to decide comes
// through here, so a command never reaches for a global.
type Ctx struct {
	Noun, Verb string
	Args       []string
	Cluster    string
	JSON       bool
	Verbose    bool
	Quiet      bool
	Timeout    time.Duration
	Store      *store.Store
	Record     *record.Record
	Env        *Envelope
	Out        io.Writer
	Err        io.Writer
	Ctx        context.Context
	fs         *flag.FlagSet
}

// str and dur read a command's own flags. They return the zero value for a flag
// the command did not declare, which is a programming error rather than a user
// error and shows up immediately in that command's test.
func (c *Ctx) str(name string) string {
	if f := c.fs.Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

func (c *Ctx) dur(name string) time.Duration {
	if f := c.fs.Lookup(name); f != nil {
		if g, ok := f.Value.(flag.Getter); ok {
			if d, ok := g.Get().(time.Duration); ok {
				return d
			}
		}
	}
	return 0
}

// Note adds a machine-readable note to the envelope.
func (c *Ctx) Note(code, severity, msg string) { c.Env.note(code, severity, msg) }

// Command is one <noun> <verb>. Mutates marks the ones that change cluster
// state: those append to the record before they run, which is how the record
// opens without anybody switching it on (docs/design/07-record.md §2).
type Command struct {
	Noun, Verb string
	Args       string
	Summary    string
	Mutates    bool
	Flags      func(*flag.FlagSet)
	Run        func(*Ctx) (any, error)
}

func (c Command) key() string { return c.Noun + " " + c.Verb }

// Nouns, in the order docs/design/01-cli.md §1 lists them.
var nouns = []string{"cluster", "node", "fault", "repl", "ha", "load", "record"}

// Main runs one invocation and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	// --version is only the binary's version when it comes first: "cluster create
	// --version 11.5" selects an engine release, and a global flag that swallowed
	// it would silently do the wrong thing (docs/design/01-cli.md §3, §7).
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-version") {
		fmt.Fprintf(stdout, "csb %s\n", Version)
		return ExitOK
	}
	for _, a := range args {
		if a == "--help" || a == "-h" || a == "help" {
			usage(stdout)
			return ExitOK
		}
	}
	if len(args) < 2 {
		usage(stderr)
		return ExitUsage
	}

	noun, verb := args[0], args[1]
	cmd, ok := lookup(noun, verb)
	if !ok {
		if !knownNoun(noun) {
			return early(stdout, stderr, args, noun+" "+verb, "unknown_noun",
				fmt.Sprintf("unknown noun %q (want: %s)", noun, strings.Join(nouns, ", ")))
		}
		return early(stdout, stderr, args, noun+" "+verb, "unknown_verb",
			fmt.Sprintf("%s has no verb %q (want: %s)", noun, verb, strings.Join(verbsOf(noun), ", ")))
	}

	code, err := dispatch(cmd, args[2:], stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "csb: %v\n", err)
	}
	return code
}

func dispatch(cmd Command, rest []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet(cmd.key(), flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var (
		cluster  = fs.String("cluster", "", "which cluster")
		asJSON   = fs.Bool("json", false, "structured output")
		verbose  = fs.Bool("verbose", false, "show the engine commands being run")
		verboseS = fs.Bool("v", false, "shorthand for --verbose")
		quiet    = fs.Bool("quiet", false, "suppress progress, keep errors")
		quietS   = fs.Bool("q", false, "shorthand for --quiet")
		timeout  = fs.Duration("timeout", 180*time.Second, "bound on any engine wait")
	)
	if cmd.Flags != nil {
		cmd.Flags(fs)
	}

	positional, err := parseInterspersed(fs, rest)
	if err != nil {
		return early(stdout, stderr, rest, cmd.key(), "usage", err.Error()), nil
	}

	st, err := store.Open()
	if err != nil {
		return ExitFailed, err
	}

	name := *cluster
	if name == "" {
		name = os.Getenv("CSB_CLUSTER")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c := &Ctx{
		Noun: cmd.Noun, Verb: cmd.Verb, Args: positional,
		Cluster: name,
		JSON:    *asJSON, Verbose: *verbose || *verboseS, Quiet: *quiet || *quietS,
		Timeout: *timeout, Store: st,
		Env: newEnvelope(cmd.key(), name, time.Now()),
		Out: stdout, Err: stderr, Ctx: ctx, fs: fs,
	}
	if name != "" {
		c.Record = record.Open(st.RecordPath(name))
	}

	if cmd.Mutates && c.Record != nil {
		_ = c.Record.Append(record.ActorTool, "command."+strings.ReplaceAll(cmd.key(), " ", "."),
			map[string]any{"args": positional})
	}

	data, cmdErr := cmd.Run(c)
	if data != nil {
		c.Env.Data = data
	}

	if cmdErr != nil {
		var e *Error
		if !errors.As(cmdErr, &e) {
			e = &Error{Code: ExitFailed, Note: "error", Msg: cmdErr.Error()}
		}
		c.Env.OK = false
		c.Env.note(e.Note, SevError, e.Msg)
		if c.JSON {
			_ = c.Env.writeJSON(stdout)
			return e.Code, nil
		}
		return e.Code, errors.New(e.Msg)
	}

	if c.JSON {
		return ExitOK, c.Env.writeJSON(stdout)
	}
	return ExitOK, nil
}

// parseInterspersed lets flags and positional arguments mix, because
// "csb fault partition master --keep ping-host" reads the way a person writes it.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	// A bare `--` ends flag parsing: everything after it belongs to another
	// program. It has to be honoured here rather than left to the flag package,
	// because the loop below re-parses whatever Parse leaves behind and that
	// re-enables flag parsing for the remainder -- so `node exec n1 -- csql -u
	// dba` failed with "flag provided but not defined: -u", which is this tool
	// reading someone else's flags. Found by the end-to-end suite.
	var verbatim []string
	for i, a := range args {
		if a == "--" {
			verbatim = args[i+1:]
			args = args[:i]
			break
		}
	}
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return append(positional, verbatim...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func lookup(noun, verb string) (Command, bool) {
	for _, c := range registry {
		if c.Noun == noun && c.Verb == verb {
			return c, true
		}
	}
	return Command{}, false
}

func knownNoun(n string) bool {
	for _, x := range nouns {
		if x == n {
			return true
		}
	}
	return false
}

func verbsOf(noun string) []string {
	var v []string
	for _, c := range registry {
		if c.Noun == noun {
			v = append(v, c.Verb)
		}
	}
	sort.Strings(v)
	return v
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "csb %s — provision a CUBRID topology for development\n\n", Version)
	fmt.Fprintf(w, "usage: csb <noun> <verb> [selector] [flags]\n\n")
	for _, n := range nouns {
		fmt.Fprintf(w, "  %s\n", n)
		for _, c := range registry {
			if c.Noun != n {
				continue
			}
			line := c.Verb
			if c.Args != "" {
				line += " " + c.Args
			}
			fmt.Fprintf(w, "    %-28s %s\n", line, c.Summary)
		}
	}
	fmt.Fprintf(w, "\nglobal flags: --cluster NAME  --json  --timeout DURATION  --quiet/-q  --verbose/-v\n")
	fmt.Fprintf(w, "environment:  CSB_HOME (state root)  CSB_CLUSTER (default --cluster)\n")
}

// wantsJSON scans the raw arguments. A failure that happens before a flag set is
// parsed still has to answer in the shape the caller asked for.
func wantsJSON(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--json" || a == "-json" {
			return true
		}
	}
	return false
}

// early answers a failure that happens before a command runs -- an unknown verb,
// a flag that does not parse -- in the envelope when one was asked for.
//
// It used to print to stderr and exit 2 with no envelope at all, which broke the
// contract exactly where a consumer needs it most: it had to parse stderr to
// tell a typo from anything else (docs/design/01-cli.md §4). The end-to-end
// suite caught it on its first run.
func early(stdout, stderr io.Writer, args []string, command, note, msg string) int {
	if wantsJSON(args) {
		e := newEnvelope(command, "", time.Now())
		e.OK = false
		e.note(note, SevError, msg)
		_ = e.writeJSON(stdout)
		return ExitUsage
	}
	fmt.Fprintf(stderr, "csb: %s\n", msg)
	return ExitUsage
}
