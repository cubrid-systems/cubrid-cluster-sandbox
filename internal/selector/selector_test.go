package selector

import "testing"

func TestParse(t *testing.T) {
	ok := []struct {
		in    string
		kind  Kind
		index int
		node  string
	}{
		{"master", Master, -1, ""},
		{"slave", Slave, -1, ""},
		{"all", All, -1, ""},
		{"slave[0]", Slave, 0, ""},
		{"slave[12]", Slave, 12, ""},
		{"replica[0]", Replica, 0, ""},
		// A client node is part of the cluster and not of the HA group, and both
		// forms have to parse. `client` reached Resolve without reaching the
		// grammar once, so a step addressing client[1] failed and the traffic it
		// was supposed to start silently never ran.
		{"client", Client, -1, ""},
		{"client[1]", Client, 1, ""},
		{"n1", Name, -1, "n1"},
		{"hadb-n2", Name, -1, "hadb-n2"},
	}
	for _, c := range ok {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q) errored: %v", c.in, err)
			continue
		}
		if got.Kind != c.kind || got.Index != c.index || got.NodeRaw != c.node {
			t.Errorf("Parse(%q) = %+v, want kind=%v index=%d node=%q", c.in, got, c.kind, c.index, c.node)
		}
		if got.Raw != c.in {
			t.Errorf("Parse(%q) lost the raw text: %q", c.in, got.Raw)
		}
	}

	// "replica" alone is rejected on purpose: an unindexed replica selector
	// would silently mean "the first one" in a topology that can have several.
	bad := []string{"", "replica", "Master", "slave[]", "slave[-1]", "n 1", "slave[0", "--json"}
	for _, in := range bad {
		if got, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) = %+v, want an error", in, got)
		}
	}
}

// Every kind this package can return has to be spellable, or a caller that
// handles it downstream is handling something Parse will never produce -- which
// is the shape of the bug that motivated `client[n]`: Resolve knew the selector
// and Parse rejected it, so it failed at the door rather than at the caller.
func TestEveryKindHasASpelling(t *testing.T) {
	spellings := map[Kind]string{
		Master:  "master",
		Slave:   "slave",
		Replica: "replica[0]",
		Client:  "client",
		Name:    "hadb-n1",
		All:     "all",
	}
	for k := Master; k <= All; k++ {
		spelling, ok := spellings[k]
		if !ok {
			t.Fatalf("kind %d has no spelling in this test, which means a new kind was added without one", k)
		}
		got, err := Parse(spelling)
		if err != nil {
			t.Errorf("%q does not parse: %v", spelling, err)
			continue
		}
		if got.Kind != k {
			t.Errorf("%q parsed as kind %d, want %d", spelling, got.Kind, k)
		}
	}
}
