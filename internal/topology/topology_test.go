package topology

import "testing"

func TestHaPresetDerivesEverythingFromTheName(t *testing.T) {
	top, err := Resolve(Options{Name: "hadb"})
	if err != nil {
		t.Fatal(err)
	}
	if len(top.Nodes) != 2 {
		t.Fatalf("ha defaults to 2 nodes, got %d", len(top.Nodes))
	}
	if top.Nodes[0].Name != "hadb-n1" || top.Nodes[0].Role != "master" {
		t.Errorf("first node = %+v", top.Nodes[0])
	}
	if top.Nodes[1].Name != "hadb-n2" || top.Nodes[1].Role != "slave" {
		t.Errorf("second node = %+v", top.Nodes[1])
	}
	if top.Network != "hadb-net" || top.DB != "hadb" {
		t.Errorf("network=%q db=%q, both derive from the name", top.Network, top.DB)
	}
	// The same on every node: that is how each learns who its peer is.
	if got, want := top.HANodeList(), "cubrid@hadb-n1:hadb-n2"; got != want {
		t.Errorf("HANodeList() = %q, want %q", got, want)
	}
}

func TestSinglePresetHasNoPartitionToDiagnose(t *testing.T) {
	top, err := Resolve(Options{Name: "solo", Preset: "single", PingMode: PingICMP})
	if err != nil {
		t.Fatal(err)
	}
	if len(top.Nodes) != 1 || top.Nodes[0].Role != "standalone" {
		t.Fatalf("nodes = %+v", top.Nodes)
	}
	if top.PingMode != PingNone {
		t.Errorf("ping mode = %q; a lone node has no partition to diagnose", top.PingMode)
	}
}

func TestRejects(t *testing.T) {
	bad := []struct {
		why string
		o   Options
	}{
		{"uppercase name", Options{Name: "HaDb"}},
		{"name starting with a digit", Options{Name: "1db"}},
		{"unknown preset", Options{Name: "x", Preset: "shard"}},
		{"ha with one node", Options{Name: "x", Nodes: 1}},
		{"single with two", Options{Name: "x", Preset: "single", Nodes: 2}},
		{"unknown ping mode", Options{Name: "x", PingMode: "icmpv6"}},
		{"malformed --set", Options{Name: "x", Set: []string{"noequals"}}},
	}
	for _, c := range bad {
		if _, err := Resolve(c.o); err == nil {
			t.Errorf("%s: expected an error", c.why)
		}
	}
}

func TestParameterRouting(t *testing.T) {
	top, err := Resolve(Options{
		Name: "x",
		Set:  []string{"ha_ping_hosts=ping-host", "max_clients=200"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if top.Parameters.HA["ha_ping_hosts"] != "ping-host" {
		t.Errorf("ha_ key went to %+v", top.Parameters)
	}
	if top.Parameters.Common["max_clients"] != "200" {
		t.Errorf("cubrid.conf key went to %+v", top.Parameters)
	}

	// An unknown key is refused rather than written to a file the engine will
	// silently ignore -- and the refusal names the documented escape hatch.
	_, err = Resolve(Options{Name: "x", Set: []string{"frobnicate=7"}})
	if err == nil {
		t.Fatal("an unknown parameter must be refused")
	}
	if want := "--set-hidden"; !contains(err.Error(), want) {
		t.Errorf("the refusal must name %s: %v", want, err)
	}

	// --set-hidden takes what --set cannot validate, because the three
	// parameters that decide when a failover happens are absent from paramdump.
	top, err = Resolve(Options{Name: "x", SetHidden: []string{"ha_calc_score_interval_in_msecs=300000"}})
	if err != nil {
		t.Fatal(err)
	}
	if top.Parameters.Hidden["ha_calc_score_interval_in_msecs"] != "300000" {
		t.Errorf("hidden = %+v", top.Parameters.Hidden)
	}
	if got := top.HiddenKeys(); len(got) != 1 {
		t.Errorf("HiddenKeys() = %v", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
