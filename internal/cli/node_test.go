package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestKindOfNamesTheProcessNotTheFile(t *testing.T) {
	// The real names, from a running two-node cluster. The point of --which is
	// that a user names the process and never has to know any of this.
	cases := map[string]string{
		"hadb_hadb-n2_copylogdb.err":                 "copylogdb",
		"hadb@localhost_applylogdb_hadb_hadb-n1.err": "applylogdb",
		"hadb-n1_master.err":                         "master",
		"server/hadb_20260902.err":                   "server",
		"broker/csb.access":                          "broker",
		"cub_master_hadb-n1.log":                     "other",
	}
	for in, want := range cases {
		if got := kindOf(in); got != want {
			t.Errorf("kindOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStageTrackFindsWhenItStartedFallingBehind(t *testing.T) {
	start := time.Now()
	var s stageTrack
	for i, v := range []int{0, 0, 0, 7, 22, 22} {
		n := v
		s.observe(&n, start.Add(time.Duration(i)*time.Second), start)
	}
	if s.First == nil || *s.First != 0 || s.Last == nil || *s.Last != 22 || s.Max == nil || *s.Max != 22 {
		t.Fatalf("track = %+v", s)
	}
	// Three seconds in, not at the maximum: the question is when it STARTED.
	if s.RoseSec < 2.9 || s.RoseSec > 3.1 {
		t.Errorf("rose after %.2fs, want ~3s", s.RoseSec)
	}
	// A stage that never moves has no rise, and must not invent one.
	var flat stageTrack
	for i := 0; i < 4; i++ {
		n := 5
		flat.observe(&n, start.Add(time.Duration(i)*time.Second), start)
	}
	if flat.RoseAt != "" {
		t.Errorf("a flat series reported a rise at %q", flat.RoseAt)
	}
}

// The three verbs that cannot answer through the envelope say so before they
// touch anything, rather than half-running and then failing.
func TestTerminalVerbsRefuseJSON(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "clusters", "hadb")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	describe := `{"schema":"csb/v1","cluster":"hadb","preset":"ha","db":"hadb"}`
	if err := os.WriteFile(filepath.Join(dir, "describe.json"), []byte(describe), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := [][]string{
		{"node", "shell", "master", "--cluster", "hadb", "--json"},
		{"node", "logs", "master", "--cluster", "hadb", "--json", "--follow"},
		{"ha", "failback", "--cluster", "hadb", "--json"},
	}
	for _, args := range cases {
		if code, out, _ := invoke(t, home, args...); code != ExitUsage {
			t.Errorf("%v exited %d, want %d\n%s", args, code, ExitUsage, out)
		}
	}
}
