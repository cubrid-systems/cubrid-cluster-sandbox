package inspect

import "testing"

// A standalone server is the one node that serves, so a single cluster is
// "serving" by the same test a pair is -- and nothing else counts as active.
func TestStandaloneCountsAsTheOneActiveNode(t *testing.T) {
	if !IsActive(StateStandalone) || !IsActive("registered_and_active") {
		t.Error("the two serving states are not both active")
	}
	for _, s := range []string{"", "registered_and_standby", "registered_and_to_be_active", "unknown"} {
		if IsActive(s) {
			t.Errorf("%q counted as active", s)
		}
	}
	st := &Status{Nodes: []Node{{Name: "solo-n1", Server: StateStandalone}, {Name: "solo-c1"}}}
	if !st.Serving() {
		t.Error("a running standalone server is not reported as serving")
	}
	st.Nodes[0].Server = ""
	if st.Serving() {
		t.Error("a stopped standalone server is reported as serving")
	}
}

func TestServerListedMatchesTheWholeName(t *testing.T) {
	out := " Server perf_ab (rel 11.4, pid 120)\n"
	if serverListed(out, "perf_a") || !serverListed(out, "perf_ab") {
		t.Error("the database name is not matched whole")
	}
}
