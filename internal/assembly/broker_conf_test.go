package assembly

import (
	"regexp"
	"strings"
	"testing"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/topology"
)

// The broker file is the tool's own text, and --broker-set edits it: a key the
// template has is replaced where it stands, a key it lacks is added to the
// section, and the keys quiesce depends on are untouched because they were
// refused before they got here.
func TestBrokerConfTakesOverridesInPlace(t *testing.T) {
	top := topology.Topology{DB: "perf", WithBroker: true}
	top.Parameters.Broker = map[string]string{
		"MIN_NUM_APPL_SERVER": "16", "MAX_NUM_APPL_SERVER": "16", "SQL_LOG": "OFF",
		"APPL_SERVER_MAX_SIZE": "512",
	}
	got := string((&Assembler{T: &top}).brokerConf())

	for key, want := range map[string]string{
		"MIN_NUM_APPL_SERVER": "16", "MAX_NUM_APPL_SERVER": "16", "SQL_LOG": "OFF",
		"APPL_SERVER_MAX_SIZE": "512", "ACCESS_MODE": "RW", "BROKER_PORT": "33000",
	} {
		re := regexp.MustCompile(`(?m)^` + key + `\s*=` + want + `$`)
		if n := len(re.FindAllString(got, -1)); n != 1 {
			t.Errorf("%s=%s appears %d time(s), want 1 in\n%s", key, want, n, got)
		}
	}
	if strings.Count(got, "SQL_LOG") != 1 || strings.Contains(got, "=ON\n") && strings.Contains(got, "SQL_LOG                 =ON") {
		t.Errorf("the overridden SQL_LOG line was kept beside the new one:\n%s", got)
	}
	// The section the override lands in is the broker's, after its header.
	if strings.Index(got, "[%csb]") > strings.Index(got, "APPL_SERVER_MAX_SIZE") {
		t.Errorf("an added key landed outside the [%%csb] section:\n%s", got)
	}

	// No overrides: the file is the template, unchanged.
	plain := string((&Assembler{T: &topology.Topology{DB: "perf", WithBroker: true}}).brokerConf())
	for _, want := range []string{"MIN_NUM_APPL_SERVER     =2", "MAX_NUM_APPL_SERVER     =10", "SQL_LOG                 =ON"} {
		if !strings.Contains(plain, want) {
			t.Errorf("without overrides the template must stand: missing %q", want)
		}
	}
}
