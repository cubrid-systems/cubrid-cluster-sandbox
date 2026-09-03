package record

import (
	"bytes"
	"strings"
	"testing"
)

// The page is a client of the document, so what it must never do is drop a fact
// the document carries -- especially the ones the design says have to travel
// together: both intervals for a role change, and the reason a run is not a
// clean measurement.
func TestHTMLCarriesWhatTheDocumentCarries(t *testing.T) {
	doc := &Document{
		Schema:  "csb/v1",
		Cluster: "hadb",
		Opened:  "2026-09-03T00:00:00Z",
		Timeline: []Entry{
			{T: "2026-09-03T00:00:01Z", Actor: ActorTool, Event: "node.kill", Detail: map[string]any{"node": "hadb-n1"}},
			{T: "2026-09-03T00:00:07Z", Actor: ActorEngine, Event: "ha.failover"},
		},
		RoleChanges: []RoleChange{{
			Node: "hadb-n2", Kind: "Failover", Result: "Success",
			At: "2026-09-03T00:00:07Z", Trigger: "node.kill",
			Measured: "5.9s", Predicted: "2.5s",
		}},
		Validity: Validity{Valid: false, Reasons: []string{"a fault was in force for part of this run"}},
		Describe: map[string]any{"cluster": "hadb", "preset": "ha"},
	}
	var buf bytes.Buffer
	if err := HTML(&buf, doc); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"hadb", "ha.failover", "node.kill",
		"5.9s", "2.5s", // both intervals, never one
		"a fault was in force for part of this run",
		"node=hadb-n1",
		"&#34;preset&#34;: &#34;ha&#34;", // the describe, escaped rather than dropped
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the page does not carry %q", want)
		}
	}
	// Self-contained: a run record that needs the network to render is not
	// evidence six months later.
	for _, forbidden := range []string{"http://", "https://", "<script"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the page reaches outside itself: %q", forbidden)
		}
	}
}

func TestDetailLineIsStable(t *testing.T) {
	m := map[string]any{"seconds": 1.8, "target": "n2", "demoted": "n1"}
	first := detailLine(m)
	for i := 0; i < 20; i++ {
		if got := detailLine(m); got != first {
			t.Fatalf("detail order is not stable: %q then %q", first, got)
		}
	}
	if !strings.HasPrefix(first, "demoted=") {
		t.Errorf("keys are not sorted: %q", first)
	}
}
