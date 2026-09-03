package topology

import (
	"bytes"
	"strings"
	"testing"
)

const sampleConf = `
# a comment
env.instance1.master.ssh.host=192.168.1.10
env.instance1.master.ssh.user=cubrid
env.instance1.slave.ssh.host=192.168.1.11
env.instance1.slave.ssh.user=cubrid
env.instance1.ha.ha_max_heartbeat_gap=10
env.instance1.cubrid.max_clients=200
env.instance1.broker1.APPL_SERVER_MAX_SIZE=1024
default.ha.ha_port_id=59901
cubrid_download_url=http://127.0.0.1/download/CUBRID.sh
scenario=${HOME}/cubrid-testcases/sql
ha_sync_detect_timeout_in_secs=600
`

func TestParseCTPConfSeparatesWhatWeCanHonour(t *testing.T) {
	keys, other, err := ParseCTPConf(strings.NewReader(sampleConf))
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 8 {
		t.Errorf("parsed %d env/default keys, want 8: %+v", len(keys), keys)
	}
	// The keys that are not per-instance settings are reported rather than
	// dropped: cubrid_download_url in particular has to be answered out loud,
	// because csb bind-mounts a build and will not honour it.
	for _, want := range []string{"cubrid_download_url", "scenario", "ha_sync_detect_timeout_in_secs"} {
		if _, ok := other[want]; !ok {
			t.Errorf("%q was dropped instead of reported", want)
		}
	}
}

func TestCTPSetsKeepsValidationAndSkipsAddresses(t *testing.T) {
	keys, _, _ := ParseCTPConf(strings.NewReader(sampleConf))
	sets, hidden, refused := CTPSets(keys)
	joined := strings.Join(sets, " ")
	if !strings.Contains(joined, "max_clients=200") {
		t.Errorf("engine parameters were not carried: %v", sets)
	}
	// A parameter the engine has and does not advertise is not a typo. It goes
	// where a person would have had to put it by hand, rather than being
	// refused as unknown.
	if strings.Join(hidden, " ") != "ha_max_heartbeat_gap=10" {
		t.Errorf("a measured hidden parameter was not routed to --set-hidden: %v %v", sets, hidden)
	}
	// ssh.host is the topology, not a parameter, and a broker section is a file
	// this tool writes by construction.
	if strings.Contains(joined, "ssh.host") || strings.Contains(joined, "APPL_SERVER") {
		t.Errorf("something that is not an engine parameter was carried: %v", sets)
	}
	if len(refused) != 0 {
		t.Errorf("refused %v from a conf whose parameters are all known", refused)
	}

	// And an unknown key is refused rather than written, exactly as --set would.
	bad, _, _ := ParseCTPConf(strings.NewReader("env.i1.ha.ha_no_such_thing=1\n"))
	if _, _, r := CTPSets(bad); len(r) != 1 {
		t.Errorf("an unknown parameter was not refused: %v", r)
	}
}

func TestWriteCTPConfNamesContainersAndSaysSo(t *testing.T) {
	tp := &Topology{
		Cluster: "hadb", DB: "hadb",
		Nodes: []Node{{Name: "hadb-n1", Role: "master"}, {Name: "hadb-n2", Role: "slave"}},
	}
	tp.Parameters.HA = map[string]string{"ha_max_heartbeat_gap": "10"}
	var buf bytes.Buffer
	if err := tp.WriteCTPConf(&buf, "", "1000:1000"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"env.hadb.master.ssh.host=hadb-n1",
		"env.hadb.slave.ssh.host=hadb-n2",
		"env.hadb.master.ssh.user=1000:1000",
		"env.hadb.ha.ha_db_list=hadb",
		"env.hadb.ha.ha_max_heartbeat_gap=10",
		"CONTAINER NAMES",     // the transport is named, not implied
		"cubrid_download_url", // and so is what does not apply
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the fragment does not carry %q:\n%s", want, out)
		}
	}

	// A single node cannot be an ha_repl environment, and saying so beats
	// writing a conf that names a slave that does not exist.
	one := &Topology{Cluster: "solo", Nodes: []Node{{Name: "solo-n1", Role: "master"}}}
	if err := one.WriteCTPConf(&bytes.Buffer{}, "", "u"); err == nil {
		t.Error("a one-node topology produced an ha_repl conf")
	}
}
