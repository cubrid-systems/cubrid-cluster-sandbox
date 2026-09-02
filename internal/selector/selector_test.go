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
