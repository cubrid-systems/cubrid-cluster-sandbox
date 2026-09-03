package backend

import (
	"strings"
	"testing"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/engine"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/topology"
)

// The container requirements in docs/design/03-assembly.md §4 are not
// preferences; each one is load-bearing and each is asserted here, so a
// refactor cannot quietly drop one. No docker daemon is needed to check them.
func TestNodePlanCarriesEveryContainerRequirement(t *testing.T) {
	top, err := topology.Resolve(topology.Options{
		Name:   "hadb",
		Engine: &engine.Identity{Kind: "build", Path: "/builds/install.out"},
		CPUs:   4,
	})
	if err != nil {
		t.Fatal(err)
	}
	top.Image = "csb-base:test"
	argv := NodePlan(top, top.Nodes[0], "/work/hadb", "/res", 1000, 1000)
	line := strings.Join(argv, " ")

	must := map[string]string{
		"--init":                                "without a reaping PID 1, cubrid heartbeat stop never returns",
		"--cap-add=NET_ADMIN":                   "the fault mechanisms are route and qdisc operations",
		"--hostname hadb-n1":                    "the heartbeat resolves peers by hostname",
		"--user 1000:1000":                      "files written to the work directory stay editable on the host",
		"--shm-size 1g":                         "CUBRID's shared memory does not fit the 64 MB default",
		"--cpus 4":                              "a host-load profile is only reproducible against a stated core count",
		"/builds/install.out:/opt/cubrid-ro:ro": "the engine is bind-mounted, never baked into an image",
		"/work/hadb/hadb-n1/db:/db":             "every node mounts its database directory at the same container path",
		"--label csb.cluster=hadb":              "the cluster is discoverable from the world, not from a lock file",
	}
	for frag, why := range must {
		if !strings.Contains(line, frag) {
			t.Errorf("node plan is missing %q — %s\ngot: %s", frag, why, line)
		}
	}
}

func TestNodePlanOmitsCPUsWhenUnset(t *testing.T) {
	top, _ := topology.Resolve(topology.Options{Name: "hadb"})
	top.Image = "img"
	if line := strings.Join(NodePlan(top, top.Nodes[0], "/w", "/res", 0, 0), " "); strings.Contains(line, "--cpus") {
		t.Errorf("an unset quota must not become --cpus 0: %s", line)
	}
}

// The tag is the hash of the recipe, so an unchanged recipe is never rebuilt and
// a changed one is a different image rather than a silently stale one.
func TestBaseImageTagIsDerivedFromTheRecipe(t *testing.T) {
	tag := BaseImage()
	if !strings.HasPrefix(tag, "csb-base:") || len(tag) != len("csb-base:")+12 {
		t.Fatalf("tag = %q", tag)
	}
	if BaseImage() != tag {
		t.Error("the tag must be stable for an unchanged recipe")
	}
}
