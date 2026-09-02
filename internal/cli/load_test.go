package cli

import "testing"

func TestParseRate(t *testing.T) {
	ok := map[string]float64{"2000/s": 2000, "2000": 2000, "max": 0, "": 0, " 500/s ": 500}
	for in, want := range ok {
		got, err := parseRate(in)
		if err != nil || got != want {
			t.Errorf("parseRate(%q) = (%v, %v), want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"fast", "-1", "2000/m"} {
		if _, err := parseRate(in); err == nil {
			t.Errorf("parseRate(%q) must fail rather than guess", in)
		}
	}
}
