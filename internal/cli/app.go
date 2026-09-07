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
var nouns = []string{"cluster", "node", "fault", "repl", "ha", "scenario", "record"}

// Main runs one invocation and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	// --version is only the binary's version when it comes first: "cluster create
	// --version 11.5" selects an engine release, and a global flag that swallowed
	// it would silently do the wrong thing (docs/design/01-cli.md §3, §7).
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-version") {
		fmt.Fprintf(stdout, "csb %s\n", Version)
		return ExitOK
	}
	if answered := helpFor(args, stdout); answered {
		return ExitOK
	}
	if len(args) < 2 {
		// A noun on its own is an incomplete command rather than a question, so
		// it still exits 2 -- but what it prints is that noun's verbs, which is
		// what the caller was reaching for, and not the whole surface.
		if len(args) == 1 && knownNoun(args[0]) {
			nounUsage(stderr, args[0])
		} else {
			usage(stderr)
		}
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

	g := globalFlags(fs)
	cluster, asJSON := g.cluster, g.asJSON
	verbose, verboseS := g.verbose, g.verboseS
	quiet, quietS, timeout := g.quiet, g.quietS, g.timeout
	if cmd.Flags != nil {
		cmd.Flags(fs)
	}

	positional, err := parseInterspersed(fs, rest)
	if err != nil {
		return early(stdout, stderr, rest, cmd.key(), "usage", flagError(cmd, err)), nil
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

// globals holds the flags every command takes, declared in one place so that
// --help lists what dispatch actually accepts rather than a second copy of it.
type globals struct {
	cluster                   *string
	asJSON, verbose, verboseS *bool
	quiet, quietS             *bool
	timeout                   *time.Duration
}

func globalFlags(fs *flag.FlagSet) *globals {
	return &globals{
		cluster:  fs.String("cluster", "", "which cluster"),
		asJSON:   fs.Bool("json", false, "structured output"),
		verbose:  fs.Bool("verbose", false, "show the engine commands being run"),
		verboseS: fs.Bool("v", false, "shorthand for --verbose"),
		quiet:    fs.Bool("quiet", false, "suppress progress, keep errors"),
		quietS:   fs.Bool("q", false, "shorthand for --quiet"),
		timeout:  fs.Duration("timeout", 180*time.Second, "bound on any engine wait"),
	}
}

// globalLine is the one-line summary of the above. A test walks globalFlags and
// fails if a name is missing from it, because a global flag nothing documents is
// a flag nobody finds.
const globalLine = "global flags: --cluster NAME  --json  --timeout DURATION  --quiet/-q  --verbose/-v"

// selectorLine says what a <selector> is. It was in the README and nowhere the
// binary could reach, which left `[selector]` in the usage line meaning nothing
// to a first-time caller (docs/design/01-cli.md §2).
const selectorLine = "selectors:    master  slave  slave[n]  replica[n]  client  client[n]  <node>  all\n" +
	"              a query resolved when the command runs, not a label: after a\n" +
	"              failover `master` names the other machine"

const exitLine = "exit codes:   0 ok · 1 failed · 2 usage · 3 precondition · 4 timeout · 5 unmodelled"

const envLine = "environment:  CSB_HOME (state root)  CSB_CLUSTER (default --cluster)"

// isHelp reports whether one token asks for help.
func isHelp(a string) bool { return a == "--help" || a == "-h" || a == "-help" || a == "help" }

// asksHelp scans for a help token and stops at a bare `--`, because everything
// after that belongs to another program: `node exec master -- csql --help` is a
// question for csql, and answering it here printed our own usage instead of
// running the command. Same rule wantsJSON follows, for the same reason.
func asksHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if isHelp(a) {
			return true
		}
	}
	return false
}

// helpFor answers a help request against the surface rather than by scanning for
// a token anywhere in argv. `--help` after a noun and a verb asks about THAT
// command, which is where its own flags are: forty-five of them were declared
// with a written description each and reachable only from the README.
func helpFor(args []string, w io.Writer) bool {
	if len(args) == 0 {
		return false
	}
	if isHelp(args[0]) {
		if len(args) > 1 && knownNoun(args[1]) {
			nounUsage(w, args[1])
		} else {
			usage(w)
		}
		return true
	}
	if knownNoun(args[0]) && len(args) > 1 && isHelp(args[1]) {
		nounUsage(w, args[0])
		return true
	}
	if len(args) >= 2 {
		if cmd, ok := lookup(args[0], args[1]); ok {
			if asksHelp(args[2:]) {
				commandUsage(w, cmd)
				return true
			}
			return false
		}
	}
	// A help token anywhere else -- an unknown noun, a verb that does not exist
	// -- still answers with the whole surface, because that is what the caller
	// needs to see next.
	if asksHelp(args) {
		usage(w)
		return true
	}
	return false
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "csb %s — provision a CUBRID topology for development\n\n", Version)
	fmt.Fprintf(w, "usage: csb <noun> <verb> [selector] [flags]\n\n")
	for _, n := range nouns {
		fmt.Fprintf(w, "  %s\n", n)
		writeVerbs(w, n)
	}
	fmt.Fprintf(w, "\n%s\n%s\n%s\n%s\n", globalLine, selectorLine, exitLine, envLine)
	fmt.Fprintf(w, "\n`csb <noun> <verb> --help` lists that command's own flags.\n")
}

func nounUsage(w io.Writer, noun string) {
	fmt.Fprintf(w, "usage: csb %s <verb> [selector] [flags]\n\n", noun)
	writeVerbs(w, noun)
	fmt.Fprintf(w, "\n%s\n", globalLine)
	fmt.Fprintf(w, "\n`csb %s <verb> --help` lists that command's own flags.\n", noun)
}

func writeVerbs(w io.Writer, noun string) {
	for _, c := range registry {
		if c.Noun != noun {
			continue
		}
		line := c.Verb
		if c.Args != "" {
			line += " " + c.Args
		}
		fmt.Fprintf(w, "    %-28s %s\n", line, c.Summary)
	}
}

// commandUsage prints one command: what it is, and the flags it declares. The
// descriptions are the ones already written beside each flag -- this only makes
// them reachable from the binary.
func commandUsage(w io.Writer, cmd Command) {
	line := "csb " + cmd.Noun + " " + cmd.Verb
	if cmd.Args != "" {
		line += " " + cmd.Args
	}
	fmt.Fprintf(w, "%s — %s\n", line, cmd.Summary)
	if cmd.Flags != nil {
		fs := flag.NewFlagSet(cmd.key(), flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		cmd.Flags(fs)
		fmt.Fprintln(w)
		writeFlags(w, fs)
	}
	fmt.Fprintf(w, "\n%s\n", globalLine)
	if strings.Contains(cmd.Args, "selector") {
		fmt.Fprintf(w, "%s\n", selectorLine)
	}
}

// flagError says a flag did not parse in this tool's vocabulary rather than the
// flag package's. "flag provided but not defined: -stage" puts one dash where
// every line of documentation uses two, and leaves the caller with no way to
// find out what IS defined -- which until now was true, and is why the sentence
// ends where it does.
func flagError(cmd Command, err error) string {
	msg := err.Error()
	if name, ok := strings.CutPrefix(msg, "flag provided but not defined: -"); ok {
		msg = "unknown flag --" + strings.TrimPrefix(name, "-")
	}
	return fmt.Sprintf("%s (csb %s --help lists this command's flags)", msg, cmd.key())
}

// writeFlags renders a flag set with the double dash people actually type. The
// flag package prints one, and every line of documentation this project has
// written uses two.
func writeFlags(w io.Writer, fs *flag.FlagSet) {
	type row struct{ left, right string }
	var rows []row
	width := 0
	fs.VisitAll(func(f *flag.Flag) {
		kind, help := flag.UnquoteUsage(f)
		left := "--" + f.Name
		if kind != "" {
			left += " " + kind
		}
		if def := f.DefValue; def != "" && def != "false" && def != "0" && def != "0s" {
			help += fmt.Sprintf(" (default %s)", def)
		}
		if len(left) > width {
			width = len(left)
		}
		rows = append(rows, row{left, help})
	})
	for _, r := range rows {
		fmt.Fprintf(w, "  %-*s  %s\n", width, r.left, r.right)
	}
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
