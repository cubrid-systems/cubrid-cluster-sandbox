package topology

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

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
	// And no HA: 02-topology.md said ha_mode=off from the start, and for a long
	// time the assembly wrote on anyway.
	if !top.HAOff() || top.HAMode != HAModeOff {
		t.Errorf("ha_mode = %q, want off", top.HAMode)
	}
}

func TestHaPresetIsHA(t *testing.T) {
	top, err := Resolve(Options{Name: "hadb"})
	if err != nil {
		t.Fatal(err)
	}
	if top.HAOff() || top.HAMode != HAModeOn {
		t.Errorf("ha_mode = %q, want on", top.HAMode)
	}
	// An artifact written before the field existed was HA, and must still be.
	var old Topology
	if old.HAOff() || old.HAModeValue() != HAModeOn {
		t.Errorf("an artifact without ha_mode reads as %q, want on", old.HAModeValue())
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
		// The engine does not read cubrid_ha.conf when ha_mode is off, so the
		// value would land nowhere -- the silence --set exists to refuse.
		{"single with an HA parameter", Options{Name: "x", Preset: "single", Set: []string{"ha_copy_sync_mode=async"}}},
		// An image for client nodes that do not exist is a flag that would do
		// nothing, and nothing is the one thing a flag must not quietly do.
		{"client image without clients", Options{Name: "x", ClientImage: "perf-client:1"}},
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

	// A key the tables do not know is written to the file its name says and
	// listed as unverified -- not refused. The engine refuses a name it does
	// not have at server start, so a typo is loud there; what the refusal here
	// used to keep out was the hundreds of real parameters the shipped conf
	// does not mention (double_write_buffer_size, supplemental_log, ...).
	top, err = Resolve(Options{Name: "x", Set: []string{"double_write_buffer_size=0", "ha_apply_mem_frobnicate=1"}})
	if err != nil {
		t.Fatalf("an unknown parameter must be carried, not refused: %v", err)
	}
	if top.Parameters.Common["double_write_buffer_size"] != "0" {
		t.Errorf("an unknown cubrid.conf key went to %+v", top.Parameters)
	}
	if top.Parameters.HA["ha_apply_mem_frobnicate"] != "1" {
		t.Errorf("an unknown ha_* key must still go to cubrid_ha.conf: %+v", top.Parameters)
	}
	if got := strings.Join(top.Parameters.Unverified, ","); got != "double_write_buffer_size,ha_apply_mem_frobnicate" {
		t.Errorf("unverified = %q; both unknown keys must be listed, sorted", got)
	}
	if top.Parameters.Unverified != nil && len(top.Parameters.Unverified) > 0 {
		known, _ := Resolve(Options{Name: "x", Set: []string{"max_clients=200"}})
		if len(known.Parameters.Unverified) != 0 {
			t.Errorf("a known key was listed as unverified: %v", known.Parameters.Unverified)
		}
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

// The artifact is the same value the tool builds from, so it has to survive the
// trip through JSON unchanged -- that is what makes `create --from` a rebuild
// rather than an approximation.
func TestTopologySurvivesJSON(t *testing.T) {
	orig, err := Resolve(Options{
		Name: "hadb", CPUs: 4, ShmSize: "1g", PingMode: PingICMP, WithBroker: true,
		Set:       []string{"max_clients=200", "ha_ping_hosts=ping-host"},
		SetHidden: []string{"ha_calc_score_interval_in_msecs=9000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	orig.Image = "csb-base:test"

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var back Topology
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(*orig, back) {
		t.Fatalf("the artifact did not survive the round trip\n orig: %+v\n back: %+v", *orig, back)
	}
	// The fields a naive artifact drops, spelled out so a refactor cannot.
	if back.Parameters.Hidden["ha_calc_score_interval_in_msecs"] != "9000" {
		t.Error("a hidden parameter must travel: the cluster may be in a state the documentation does not describe")
	}
	if back.Resources.CPUs != 4 {
		t.Error("the CPU quota must travel: it is what makes a host-load profile reproducible")
	}
	if !back.WithBroker {
		t.Error("the broker must travel: quiesce has no door to close without it")
	}
}
