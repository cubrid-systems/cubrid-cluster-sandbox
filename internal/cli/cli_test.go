package cli

import (
	"bytes"
	"encoding/json"
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
		// Any verb the surface defines and this phase has not built. When repl
		// watch lands, this line fails loudly and should be pointed at whatever is
		// still unbuilt -- that is the test doing its job.
		{"specified but not built", []string{"repl", "watch", "--cluster", "nope"}, ExitFailed},
		{"a command that works", []string{"cluster", "ls", "--timeout", "5s"}, ExitOK},
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
	if code != ExitFailed {
		t.Fatalf("exit %d, want %d\n%s", code, ExitFailed, out)
	}
	e := decode(t, out)
	if e.OK {
		t.Error("ok must be false")
	}
	if len(e.Notes) != 1 || e.Notes[0].Code != "not_implemented" {
		t.Fatalf("notes = %+v, want one not_implemented", e.Notes)
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

	// A mutating verb opens the record without anyone switching it on. It fails
	// (not built yet) and must still have recorded that it was asked for, which
	// is why this one has to be both Mutates and unbuilt.
	if code, _, _ := invoke(t, home, "ha", "promote", "master", "--cluster", "hadb"); code != ExitFailed {
		t.Fatalf("expected the not-implemented failure, got %d", code)
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
}
