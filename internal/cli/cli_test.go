package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// invoke runs one command with a private CSB_HOME and returns code, stdout, stderr.
func invoke(t *testing.T, home string, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("CSB_HOME", home)
	t.Setenv("CSB_CLUSTER", "")
	var out, errb bytes.Buffer
	code := Main(args, &out, &errb)
	return code, out.String(), errb.String()
}

func decode(t *testing.T, s string) Envelope {
	t.Helper()
	var e Envelope
	if err := json.Unmarshal([]byte(s), &e); err != nil {
		t.Fatalf("output is not the JSON envelope: %v\n%s", err, s)
	}
	return e
}

func TestExitCodesAreDistinct(t *testing.T) {
	home := t.TempDir()

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"unknown noun is a usage error", []string{"clstr", "ls"}, ExitUsage},
		{"unknown verb is a usage error", []string{"cluster", "frobnicate"}, ExitUsage},
		{"too few arguments", []string{"cluster"}, ExitUsage},
		{"a verb that needs a cluster and was given none", []string{"record", "show"}, ExitUsage},
		{"a cluster that does not exist", []string{"record", "show", "--cluster", "nope"}, ExitPrecondition},
		// There is no longer a "specified but not built" case to put here: the
		// surface names 35 verbs and all 35 are built. This line was pointed at
		// repl watch and failed loudly the day it landed, which is the test
		// doing its job.
		{"a verb whose flags do not parse", []string{"repl", "watch", "--cluster", "nope", "--nonsense"}, ExitUsage},
		{"a command that works", []string{"cluster", "ls", "--timeout", "5s"}, ExitOK},
		// Everything after a bare -- belongs to the program being run on the
		// node. Reading -u as one of ours made exit 2 out of a command that
		// should have got as far as looking for the cluster.
		{"flags after -- are not ours", []string{"node", "exec", "master", "--cluster", "nope", "--", "csql", "-u", "dba", "-c", "SELECT 1"}, ExitPrecondition},
	}
	for _, c := range cases {
		got, _, _ := invoke(t, home, c.args...)
		if got != c.want {
			t.Errorf("%s: csb %s exited %d, want %d", c.name, strings.Join(c.args, " "), got, c.want)
		}
	}
}

func TestEnvelopeShape(t *testing.T) {
	home := t.TempDir()
	code, out, _ := invoke(t, home, "cluster", "ls", "--json", "--timeout", "5s")
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	e := decode(t, out)
	if e.Schema != SchemaVersion {
		t.Errorf("schema = %q, want %q", e.Schema, SchemaVersion)
	}
	if e.Command != "cluster ls" {
		t.Errorf("command = %q", e.Command)
	}
	if !e.OK {
		t.Errorf("ok = false on a successful command")
	}
	if _, err := time.Parse(time.RFC3339, e.At); err != nil {
		t.Errorf("at = %q is not RFC3339: %v", e.At, err)
	}
	if e.Notes == nil {
		t.Error("notes must be an empty list rather than null, so a consumer can range over it")
	}
	if e.Data == nil {
		t.Error("data must be present even when empty")
	}
}

// A failure still produces the envelope, with ok=false and a machine-readable
// note -- a consumer must not have to parse stderr to learn why.
func TestFailureIsStillTheEnvelope(t *testing.T) {
	home := t.TempDir()
	code, out, _ := invoke(t, home, "repl", "watch", "--cluster", "hadb", "--json")
	if code != ExitPrecondition {
		t.Fatalf("exit %d, want %d\n%s", code, ExitPrecondition, out)
	}
	e := decode(t, out)
	if e.OK {
		t.Error("ok must be false")
	}
	if len(e.Notes) != 1 || e.Notes[0].Code != "no_such_cluster" {
		t.Fatalf("notes = %+v, want one no_such_cluster", e.Notes)
	}
	if e.Notes[0].Severity != SevError {
		t.Errorf("severity = %q, want %q", e.Notes[0].Severity, SevError)
	}
}

func TestDescribeAndRecordRoundTrip(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "clusters", "hadb")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	describe := `{"schema":"csb/v1","cluster":"hadb","preset":"ha"}`
	if err := os.WriteFile(filepath.Join(dir, "describe.json"), []byte(describe), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, _ := invoke(t, home, "cluster", "describe", "--cluster", "hadb", "--json")
	if code != ExitOK {
		t.Fatalf("describe exited %d\n%s", code, out)
	}
	e := decode(t, out)
	data, ok := e.Data.(map[string]any)
	if !ok || data["preset"] != "ha" {
		t.Fatalf("describe data = %#v", e.Data)
	}

	// A mutating verb opens the record without anyone switching it on, and does
	// it BEFORE running -- so a verb that fails must still have recorded that it
	// was asked for. This describe has no nodes and there is no container here,
	// so the verb cannot succeed; that it failed is the point, and which way it
	// failed is not.
	if code, _, _ := invoke(t, home, "ha", "promote", "master", "--cluster", "hadb"); code == ExitOK {
		t.Fatal("ha promote succeeded against a cluster that does not exist")
	}
	code, out, _ = invoke(t, home, "record", "show", "--cluster", "hadb", "--json")
	if code != ExitOK {
		t.Fatalf("record show exited %d\n%s", code, out)
	}
	if !strings.Contains(out, "command.ha.promote") {
		t.Errorf("the record did not open on a state-changing command:\n%s", out)
	}

	exported := filepath.Join(home, "out", "run.json")
	code, out, _ = invoke(t, home, "record", "export", "--cluster", "hadb", "--out", exported)
	if code != ExitOK {
		t.Fatalf("record export exited %d\n%s", code, out)
	}
	b, err := os.ReadFile(exported)
	if err != nil {
		t.Fatalf("export wrote nothing: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("export is not JSON: %v", err)
	}
	// A timeline without the topology it ran against is not evidence.
	if doc["describe"] == nil {
		t.Error("export must carry the describe that opened the record")
	}
	if doc["timeline"] == nil {
		t.Error("export must carry the timeline")
	}
}

func TestVersionAndHelpDoNotNeedANoun(t *testing.T) {
	home := t.TempDir()
	if code, out, _ := invoke(t, home, "--version"); code != ExitOK || !strings.HasPrefix(out, "csb ") {
		t.Errorf("--version = (%d, %q)", code, out)
	}
	if code, out, _ := invoke(t, home, "--help"); code != ExitOK || !strings.Contains(out, "usage: csb") {
		t.Errorf("--help = (%d, %q)", code, out)
	}
}

// Every verb the surface defines must be reachable, or the registry and the
// design document have drifted apart.
func TestRegistryCoversEveryNoun(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range registry {
		seen[c.Noun] = true
		if c.Run == nil {
			t.Errorf("%s has no Run", c.key())
		}
		if c.Summary == "" {
			t.Errorf("%s has no summary, so it is invisible in --help", c.key())
		}
	}
	for _, n := range nouns {
		if !seen[n] {
			t.Errorf("noun %q has no verbs", n)
		}
	}
	// And the other direction, which is the one that bit: `scenario` was added
	// to the registry and not to `nouns`, so it worked and was invisible --
	// absent from --help, and reported as an unknown noun when somebody
	// mistyped one of its verbs. A registry the help does not list is a verb
	// nobody can find.
	listed := map[string]bool{}
	for _, n := range nouns {
		listed[n] = true
	}
	for n := range seen {
		if !listed[n] {
			t.Errorf("noun %q is in the registry and not in `nouns`, so --help does not list it", n)
		}
	}
}

// Every flag a command declares must be reachable from the binary. Forty-five
// of them were declared with a written description each and printed nowhere:
// `--help` anywhere in argv produced the global usage, so the only way to learn
// that `fault lag` takes `--stage` was to read the README. This test fails the
// moment a flag goes back to being invisible.
func TestPerCommandHelpListsEveryFlagItDeclares(t *testing.T) {
	home := t.TempDir()
	for _, cmd := range registry {
		if cmd.Flags == nil {
			continue
		}
		code, out, _ := invoke(t, home, cmd.Noun, cmd.Verb, "--help")
		if code != ExitOK {
			t.Errorf("csb %s --help exited %d", cmd.key(), code)
		}
		if !strings.HasPrefix(out, "csb "+cmd.key()) {
			t.Errorf("csb %s --help does not name the command:\n%s", cmd.key(), out)
		}
		fs := flag.NewFlagSet(cmd.key(), flag.ContinueOnError)
		cmd.Flags(fs)
		fs.VisitAll(func(f *flag.Flag) {
			if !strings.Contains(out, "--"+f.Name) {
				t.Errorf("csb %s --help does not list --%s", cmd.key(), f.Name)
			}
			if f.Usage == "" {
				t.Errorf("%s --%s has no description, so its help line says nothing", cmd.key(), f.Name)
			}
			// A backquote in a usage string is not decoration: flag.UnquoteUsage
			// reads the quoted words as the flag's TYPE NAME, so
			// "broker, or `csb load`" renders as `--mechanism csb load`. Caught
			// the first time a description was edited after help became visible.
			if strings.Contains(f.Usage, "`") {
				t.Errorf("%s --%s has a backquote in its description, which renders as the flag's type: %q",
					cmd.key(), f.Name, f.Usage)
			}
		})
	}
}

// The global flags are declared once and summarised once. A global flag missing
// from the summary is a flag nobody finds, which is the same defect the test
// above covers for the per-command ones.
func TestEveryGlobalFlagIsInTheSummaryLine(t *testing.T) {
	fs := flag.NewFlagSet("globals", flag.ContinueOnError)
	globalFlags(fs)
	fs.VisitAll(func(f *flag.Flag) {
		if !strings.Contains(globalLine, "--"+f.Name) && !strings.Contains(globalLine, "-"+f.Name) {
			t.Errorf("global flag --%s is not in %q", f.Name, globalLine)
		}
	})
}

// Everything after a bare -- belongs to the program being run on the node, help
// tokens included: `node exec master -- csql --help` is a question for csql. It
// used to print our own usage and exit 0, so the command never ran.
func TestHelpStopsAtTheDoubleDash(t *testing.T) {
	home := t.TempDir()
	code, out, _ := invoke(t, home, "node", "exec", "master", "--cluster", "nope", "--", "csql", "--help")
	if code != ExitPrecondition {
		t.Errorf("exit %d, want %d (the command must run, not answer with usage)", code, ExitPrecondition)
	}
	if strings.Contains(out, "usage: csb") {
		t.Errorf("csb answered a question meant for csql:\n%s", out)
	}
}

// A noun on its own is still an incomplete command, but what it prints is that
// noun's verbs rather than all thirty-five.
func TestANounAloneListsItsOwnVerbs(t *testing.T) {
	home := t.TempDir()
	code, _, errOut := invoke(t, home, "cluster")
	if code != ExitUsage {
		t.Errorf("csb cluster exited %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, "quiesce") {
		t.Errorf("csb cluster does not list its verbs:\n%s", errOut)
	}
	if strings.Contains(errOut, "splitbrain") {
		t.Errorf("csb cluster lists another noun's verbs:\n%s", errOut)
	}
	if code, out, _ := invoke(t, home, "cluster", "--help"); code != ExitOK || !strings.Contains(out, "quiesce") {
		t.Errorf("csb cluster --help = (%d, %q)", code, out)
	}
}

// A flag that does not parse now names the remedy, because the remedy exists.
func TestAnUnknownFlagPointsAtTheCommandsHelp(t *testing.T) {
	home := t.TempDir()
	code, _, errOut := invoke(t, home, "fault", "lag", "master", "--cluster", "nope", "--stag", "apply")
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, "unknown flag --stag") {
		t.Errorf("the message still speaks the flag package's vocabulary: %q", errOut)
	}
	if !strings.Contains(errOut, "csb fault lag --help") {
		t.Errorf("the message does not name the remedy: %q", errOut)
	}
}
