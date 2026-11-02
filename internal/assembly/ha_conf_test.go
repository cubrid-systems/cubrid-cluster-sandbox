package assembly

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/engine"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/topology"
)

// A cluster without a ping host cannot diagnose a partition, and the engine says
// so in its own words: "No hosts are registered in ha_ping_hosts ... making it
// impossible to determine". The design has always said the parameter is set by
// default; for a while nothing wrote it.
func TestHAConfWritesThePingParameter(t *testing.T) {
	base := topology.Topology{
		DB:    "hadb",
		Nodes: []topology.Node{{Name: "hadb-n1", Role: "master"}, {Name: "hadb-n2", Role: "slave"}},
	}
	cases := []struct {
		name    string
		mode    string
		host    string
		set     map[string]string
		want    string
		wantNot []string
	}{
		{name: "icmp is the default and writes ha_ping_hosts",
			mode: topology.PingICMP, host: "172.19.0.1", want: "ha_ping_hosts=172.19.0.1"},
		{name: "tcp writes the other parameter",
			mode: topology.PingTCP, host: "172.19.0.1", want: "ha_tcp_ping_hosts=172.19.0.1",
			wantNot: []string{"\nha_ping_hosts="}},
		{name: "none writes neither, which is a scenario and not an accident",
			mode: topology.PingNone, host: "172.19.0.1",
			wantNot: []string{"ha_ping_hosts", "ha_tcp_ping_hosts"}},
		{name: "no host resolved writes neither",
			mode: topology.PingICMP, host: "", wantNot: []string{"ha_ping_hosts"}},
		{name: "ha_copy_sync_mode as Resolve spelled it",
			mode: topology.PingICMP, host: "172.19.0.1",
			set:  map[string]string{"ha_copy_sync_mode": "async:async"},
			want: "\nha_copy_sync_mode=async:async\n"},
		{name: "an explicit --set wins and is not written twice",
			mode: topology.PingICMP, host: "172.19.0.1",
			set:  map[string]string{"ha_ping_hosts": "ping-host"},
			want: "ha_ping_hosts=ping-host", wantNot: []string{"172.19.0.1"}},
	}
	for _, c := range cases {
		tp := base
		tp.PingMode, tp.PingHost = c.mode, c.host
		tp.Parameters.HA = c.set
		got := string((&Assembler{T: &tp}).haConf())
		if c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s: missing %q in\n%s", c.name, c.want, got)
		}
		for _, no := range c.wantNot {
			if strings.Contains(got, no) {
				t.Errorf("%s: found %q in\n%s", c.name, no, got)
			}
		}
	}
}

// The engine reads only HA parameters from cubrid_ha.conf, so a hidden key
// that is not one goes to cubrid.conf: written to cubrid_ha.conf,
// ignore_trailing_space=yes left the pair comparing 'k' and 'k ' as two keys,
// and nothing said so.
func TestAHiddenKeyGoesWhereTheEngineReadsIt(t *testing.T) {
	tp := topology.Topology{
		DB:    "hadb",
		Nodes: []topology.Node{{Name: "hadb-n1", Role: "master"}, {Name: "hadb-n2", Role: "slave"}},
	}
	tp.Parameters.Hidden = map[string]string{"ha_calc_score_interval_in_msecs": "300000", "ignore_trailing_space": "yes"}
	a := &Assembler{T: &tp}
	ha, conf := string(a.haConf()), string(a.cubridConf("[common]\n"))
	if !strings.Contains(ha, "\nha_calc_score_interval_in_msecs=300000\n") || strings.Contains(ha, "ignore_trailing_space") {
		t.Errorf("cubrid_ha.conf:\n%s", ha)
	}
	if !strings.Contains(conf, "\nignore_trailing_space=yes\n") || strings.Contains(conf, "ha_calc_score_interval_in_msecs") {
		t.Errorf("cubrid.conf:\n%s", conf)
	}
}

// Both documents that specify this surface address a node by the SUFFIX of its
// name -- README's "n1 also selects", and 01-cli.md §2's "n1  a node by name".
// Only the full "<cluster>-n1" resolved, so a caller who read either was told a
// node they can see does not exist, and a caller who did not check the exit code
// had their command land nowhere at all.
func TestANodeResolvesByItsSuffixAsWellAsItsFullName(t *testing.T) {
	top, err := topology.Resolve(topology.Options{
		Name:   "hadb",
		Engine: &engine.Identity{Kind: "build", Path: "/builds/install.out"},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &Assembler{T: top}

	for _, sel := range []string{"hadb-n1", "n1"} {
		got, rerr := a.Resolve(context.Background(), sel)
		if rerr != nil {
			t.Errorf("%q: %v", sel, rerr)
			continue
		}
		if len(got) != 1 || got[0] != "hadb-n1" {
			t.Errorf("%q resolved to %v, want [hadb-n1]", sel, got)
		}
	}

	// A name that is neither says so, and names what there is: "no node" with no
	// list leaves the caller guessing at the very moment they got it wrong.
	_, rerr := a.Resolve(context.Background(), "n9")
	if rerr == nil {
		t.Fatal("a node that does not exist resolved")
	}
	for _, want := range []string{"n9", "hadb-n1", "hadb-n2"} {
		if !strings.Contains(rerr.Error(), want) {
			t.Errorf("the error does not carry %q: %v", want, rerr)
		}
	}
}

// WriteConfig is the one place every conf is written from -- create, create
// --from and cluster up -- so an artifact an earlier csb wrote with the value
// as it was asked ("async") still starts.
func TestWriteConfigSpellsAnUnspelledHACopySyncMode(t *testing.T) {
	engineTree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(engineTree, "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(engineTree, "conf", "cubrid.conf"), []byte("[common]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tp := topology.Topology{
		DB:    "hadb",
		Nodes: []topology.Node{{Name: "hadb-n1", Role: "master", Kind: topology.KindDB}, {Name: "hadb-n2", Role: "slave", Kind: topology.KindDB}},
	}
	tp.Parameters.HA = map[string]string{"ha_copy_sync_mode": "async"}
	a := &Assembler{T: &tp, Workdir: t.TempDir()}
	if err := a.WriteConfig(tp.Nodes[0], engineTree); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(a.Workdir, "hadb-n1", "cubrid", "conf", "cubrid_ha.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "\nha_copy_sync_mode=async:async\n") {
		t.Errorf("cubrid_ha.conf:\n%s", b)
	}
}
