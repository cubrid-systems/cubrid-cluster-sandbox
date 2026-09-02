package inspect

import "testing"

func TestServingMeansExactlyOneActive(t *testing.T) {
	cases := []struct {
		why    string
		states []string
		want   bool
	}{
		{"one master, one standby", []string{"registered_and_active", "registered_and_standby"}, true},
		{"split brain is not serving", []string{"registered_and_active", "registered_and_active"}, false},
		{"nobody active is not serving", []string{"registered_and_to_be_active", "registered_and_standby"}, false},
		{"a dead group is not serving", []string{"", ""}, false},
	}
	for _, c := range cases {
		var s Status
		for i, st := range c.states {
			s.Nodes = append(s.Nodes, Node{Name: string(rune('a' + i)), Server: st})
		}
		if got := s.Serving(); got != c.want {
			t.Errorf("%s: Serving() = %v, want %v", c.why, got, c.want)
		}
	}
}

// The parsers read the engine's own one-line output. They are the only place
// this project reads human-formatted text, and they read a stable word out of it.
func TestParsers(t *testing.T) {
	if got := reServer.FindString(" Server hadb (pid 33, state registered_and_active)"); got != "registered_and_active" {
		t.Errorf("server state = %q", got)
	}
	m := reMode.FindStringSubmatch("The server 'hadb''s current HA running mode is to-be-active.")
	if m == nil || m[1] != "to-be-active" {
		t.Fatalf("changemode parse = %v", m)
	}
}
