package fault

import (
	"testing"
	"time"
)

// Conditions are held, so they have to survive the process that set them: a
// condition that outlives its scenario silently poisons the next one, and a
// condition the tool forgets cannot be cleared at all.
func TestSetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.List) != 0 {
		t.Fatalf("a cluster with no faults must read as empty, got %+v", s.List)
	}
	if err := s.add(Active{Kind: "partition", Target: "hadb-n2", Mechanism: "blackhole",
		Cut: []string{"hadb-n1"}, Since: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}

	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.List) != 1 {
		t.Fatalf("reopened set = %+v", again.List)
	}
	got := again.List[0]
	if got.Kind != "partition" || got.Target != "hadb-n2" || got.Mechanism != "blackhole" {
		t.Errorf("condition lost detail: %+v", got)
	}
	if len(got.Cut) != 1 || got.Cut[0] != "hadb-n1" {
		t.Errorf("what was cut must survive, or clear cannot reverse it: %+v", got.Cut)
	}
	if got.Since == "" {
		t.Error("a condition carries when it was entered")
	}
}
