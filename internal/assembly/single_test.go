package assembly

import (
	"context"
	"strings"
	"testing"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/topology"
)

// 02-topology.md has always said the single preset is ha_mode=off. The assembly
// wrote ha_mode=on for every preset, so a "server without HA" carried a
// heartbeat, roles and replication log it was never asked for.
func TestCubridConfWritesThePresetsHAMode(t *testing.T) {
	shipped := "# shipped\n[common]\nha_mode=on\ncubrid_port_id=1523\ndata_buffer_size=1G\n"
	cases := []struct {
		name string
		top  topology.Topology
		want string
	}{
		{"single is off", topology.Topology{DB: "solo", HAMode: topology.HAModeOff}, "ha_mode=off\n"},
		{"ha is on", topology.Topology{DB: "hadb", HAMode: topology.HAModeOn}, "ha_mode=on\n"},
		{"an artifact without the field was HA", topology.Topology{DB: "old"}, "ha_mode=on\n"},
	}
	for _, c := range cases {
		got := string((&Assembler{T: &c.top}).cubridConf(shipped))
		if strings.Count(got, "ha_mode=") != 1 || !strings.Contains(got, c.want) {
			t.Errorf("%s: want exactly one %q in\n%s", c.name, c.want, got)
		}
		if !strings.Contains(got, "data_buffer_size=1G") {
			t.Errorf("%s: the shipped defaults were dropped:\n%s", c.name, got)
		}
	}
}

// `cubrid server status` lists every server the master knows. The name is
// matched whole: a cluster whose database is perf_a must not read perf_ab as
// its own server being up.
func TestServerListedMatchesTheWholeName(t *testing.T) {
	out := "@ cubrid server status\n Server perf_ab (rel 11.4, pid 120)\n"
	if serverListed(out, "perf_a") {
		t.Error("perf_a matched perf_ab")
	}
	if !serverListed(out, "perf_ab") {
		t.Error("perf_ab did not match its own line")
	}
	if serverListed("@ cubrid server status\n No server\n", "perf_ab") {
		t.Error("matched with no server running")
	}
}

// A single cluster has one server and no roles. "master" names it, so a
// scenario written against a pair runs unchanged; "slave" names nothing and
// says why, rather than "no node".
func TestSingleHasNoStandby(t *testing.T) {
	top, err := topology.Resolve(topology.Options{Name: "solo", Preset: "single"})
	if err != nil {
		t.Fatal(err)
	}
	a := &Assembler{T: top}
	if _, err := a.Resolve(context.Background(), "slave"); err == nil || !strings.Contains(err.Error(), "no standby") {
		t.Errorf("slave on a single resolved, or failed for the wrong reason: %v", err)
	}
	if a.activeState() != StateStandalone {
		t.Errorf("active state = %q, want %q", a.activeState(), StateStandalone)
	}
}
