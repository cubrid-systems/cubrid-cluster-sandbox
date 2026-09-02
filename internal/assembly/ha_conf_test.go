package assembly

import (
	"strings"
	"testing"

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
