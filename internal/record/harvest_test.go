package record

import (
	"testing"
	"time"
)

func TestParseEngineTime(t *testing.T) {
	m := reTime.FindStringSubmatch("Time: 09/02/26 09:43:19.667 - ERROR *** file ...")
	if m == nil {
		t.Fatal("the engine's timestamp did not match")
	}
	got := parseEngineTime(m)
	// Zero-padded months must not be read as octal: "09" is September, and the
	// bug this guards produced December of the previous year.
	if got.Year() != 2026 || got.Month() != time.September || got.Day() != 2 {
		t.Fatalf("parsed %s, want 2026-09-02", got.Format(time.RFC3339Nano))
	}
	if got.Location() != time.UTC {
		t.Errorf("engine times are read as UTC, not as the host's zone; got %s", got.Location())
	}
	if got.Hour() != 9 || got.Minute() != 43 || got.Second() != 19 {
		t.Errorf("clock = %s", got.Format("15:04:05"))
	}
	if got.Nanosecond() != 667000000 {
		t.Errorf("fraction = %d ns, want 667000000", got.Nanosecond())
	}
}

func TestHALinesAreRecognised(t *testing.T) {
	for _, line := range []string{
		"Node event: [Failover] [Success] Current node has been successfully promoted",
		"Node event: [Failback] [Cancelled] No hosts are registered in ha_ping_hosts, ...",
		"Node event: [Failback] [Diagnosis] Multiple master nodes (n2, n1) are detected",
	} {
		if m := reHA.FindStringSubmatch(line); m == nil {
			t.Errorf("not recognised: %s", line)
		}
	}
	if reHA.MatchString("Time: 09/02/26 09:43:19.667 - ERROR *** file x.c, line 1") {
		t.Error("an ordinary log line must not be taken for an HA event")
	}
}

func TestOnlySuccessCountsAsARoleChange(t *testing.T) {
	entries := []Entry{
		{T: "2026-09-02T09:43:10Z", Actor: ActorTool, Event: "node.kill"},
		{T: "2026-09-02T09:43:19Z", Actor: ActorEngine, Event: "ha.failback",
			Detail: map[string]any{"result": "Cancelled", "node": "n1"}},
		{T: "2026-09-02T09:43:19Z", Actor: ActorEngine, Event: "ha.failover",
			Detail: map[string]any{"result": "Success", "node": "n2", "line": "[Failover] [Success] ..."}},
	}
	doc := Build("hadb", entries, nil, nil)
	if len(doc.RoleChanges) != 1 {
		t.Fatalf("role changes = %d, want 1 (Cancelled and Diagnosis are not transitions)", len(doc.RoleChanges))
	}
	rc := doc.RoleChanges[0]
	if rc.Trigger != "node.kill" {
		t.Errorf("measured from %q, want the tool event that caused it", rc.Trigger)
	}
	if rc.Measured != "9s" {
		t.Errorf("measured = %q, want 9s", rc.Measured)
	}
	// The arithmetic the documented behaviour implies, stated next to it.
	if rc.Predicted != "2.5s" {
		t.Errorf("predicted = %q, want 2.5s (5 x 500ms)", rc.Predicted)
	}
	if rc.DecidedBy["ha_max_heartbeat_gap"] != 5 {
		t.Errorf("decided_by = %+v", rc.DecidedBy)
	}
}
