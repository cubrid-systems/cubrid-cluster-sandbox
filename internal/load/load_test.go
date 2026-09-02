package load

import "testing"

func TestProfilesAreClassifiedByKind(t *testing.T) {
	// The two kinds are not two intensities of one thing: a db load can run at
	// any volume without delaying a heartbeat, and a compile with no database
	// traffic can trigger a failover.
	for p, want := range map[string]string{
		"insert": "db", "update": "db", "mixed": "db",
		"host-cpu": "host", "host-io": "host",
	} {
		if Profiles[p] != want {
			t.Errorf("profile %q is kind %q, want %q", p, Profiles[p], want)
		}
	}
	if _, ok := Profiles["bulkload"]; ok {
		t.Error("bulkload is a named field case rather than the general driver, and is not implemented")
	}
}

func TestHostProfilesRefuseARate(t *testing.T) {
	d := &Driver{}
	err := d.Start(nil, Spec{Profile: "host-cpu", Rate: 100, Node: "n1"})
	if err == nil {
		t.Fatal("a rate is meaningless for a host profile: it saturates")
	}
}

func TestUnknownProfileIsRefused(t *testing.T) {
	d := &Driver{}
	if err := d.Start(nil, Spec{Profile: "frobnicate", Node: "n1"}); err == nil {
		t.Fatal("an unknown profile must be refused rather than silently doing nothing")
	}
}
