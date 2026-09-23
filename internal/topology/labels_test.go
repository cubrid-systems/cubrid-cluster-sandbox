package topology

import "testing"

// A node records the machine it was stood up on, and every node gets it. The
// field is per node because the case it exists for is a cluster whose nodes are
// not all in one place.
func TestEveryNodeRecordsTheMachineItWasCreatedOn(t *testing.T) {
	top, err := Resolve(Options{Name: "hadb", Host: "desk", Clients: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(top.Nodes) != 3 {
		t.Fatalf("want two db nodes and a client, got %d", len(top.Nodes))
	}
	for _, n := range top.Nodes {
		if n.Host != "desk" {
			t.Errorf("%s records host %q, want desk", n.Name, n.Host)
		}
	}
}

// Labels are recorded verbatim and never interpreted. The tool that drives this
// one uses them to find its own clusters again, so a label this tool decided to
// understand would be a label it could decide to change.
func TestLabelsAreRecordedAsGiven(t *testing.T) {
	top, err := Resolve(Options{Name: "hadb", Labels: []string{"run=tk-42", "owner=testkit"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := top.Labels["run"]; got != "tk-42" {
		t.Errorf("run label is %q, want tk-42", got)
	}
	if got := top.Labels["owner"]; got != "testkit" {
		t.Errorf("owner label is %q, want testkit", got)
	}
	if _, err := Resolve(Options{Name: "hadb", Labels: []string{"nokey"}}); err == nil {
		t.Error("a label without = should be refused rather than recorded as a key with no value")
	}
}

// A cluster made before these fields existed has neither, and reports neither.
// An empty column is the honest answer where a guess would be a claim.
func TestATopologyWithoutThemIsStillValid(t *testing.T) {
	top, err := Resolve(Options{Name: "hadb"})
	if err != nil {
		t.Fatal(err)
	}
	if top.Labels != nil {
		t.Errorf("labels should be absent, not empty: %v", top.Labels)
	}
	for _, n := range top.Nodes {
		if n.Host != "" {
			t.Errorf("%s invented a host: %q", n.Name, n.Host)
		}
	}
}
