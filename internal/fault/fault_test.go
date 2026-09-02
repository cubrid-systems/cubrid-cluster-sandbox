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

// A lag condition has to remember which process it suspended, or clear cannot
// resume it and the cluster is left with a stopped replication stage that
// nothing in the tool knows about.
func TestLagConditionRemembersWhatItStopped(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	if err := s.add(Active{Kind: "lag", Target: "hadb-n2", Mechanism: "suspend",
		Stage: "apply", Pid: "4711", Since: "2026-09-02T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := again.List[0]
	if got.Stage != "apply" {
		t.Errorf("stage lost: %+v — the pipeline is two processes and clear has to know which", got)
	}
	if got.Pid != "4711" {
		t.Errorf("pid lost: %+v — SIGCONT needs it", got)
	}
}

// Quiesce is not a fault, and it is stored beside them for the same reason: it
// is held, it must be cleared, and describe has to carry it. The mode travels
// because RO and SO are different doors.
func TestQuiesceIsHeldState(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	if err := s.add(Active{Kind: "quiesce", Target: "hadb-n1,hadb-n2",
		Mechanism: "broker", Mode: "RO", Since: "2026-09-02T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := again.List[0]
	if got.Kind != "quiesce" || got.Mode != "RO" {
		t.Errorf("quiesce did not survive: %+v", got)
	}
	if got.Target != "hadb-n1,hadb-n2" {
		t.Errorf("resume has to know which doors it closed: %+v", got)
	}
}
