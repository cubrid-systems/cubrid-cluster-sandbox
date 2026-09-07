package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/engine"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/run"
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

// fakeDocker puts a `docker` on PATH that behaves the way the test names, so the
// three ways docker can be unusable are checkable without making a machine be in
// any of them.
func fakeDocker(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if script != "" {
		if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

// A docker that cannot be used is a precondition, and which of the three it is
// decides the remedy: install it, start it, or join the group. They used to
// arrive as one message -- whichever docker command the assembly reached first,
// reported as an internal command that exited 1.
func TestPreflightSaysWhichWayDockerIsUnusable(t *testing.T) {
	cases := []struct {
		name, script, want string
	}{
		{"not installed", "", "not on this machine's PATH"},
		{"daemon unreachable",
			"echo 'Cannot connect to the Docker daemon at unix:///var/run/docker.sock' >&2; exit 1",
			"daemon could not be reached"},
		{"socket not permitted",
			"echo 'permission denied while trying to connect to the Docker daemon socket' >&2; exit 1",
			"docker group"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeDocker(t, c.script)
			d := &Docker{R: &run.Runner{}}
			err := d.Preflight(context.Background())
			if err == nil {
				t.Fatalf("%s: reported usable", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: %q does not contain %q", c.name, err, c.want)
			}
		})
	}

	fakeDocker(t, "echo 29.0.1")
	if err := (&Docker{R: &run.Runner{}}).Preflight(context.Background()); err != nil {
		t.Errorf("a working docker was refused: %v", err)
	}
}

// A node has to be allowed to write a core. Nothing set the limit, so a node
// inherited dockerd's -- commonly 0 -- and the one artifact that says why the
// engine died was discarded in silence.
func TestANodeMayWriteACore(t *testing.T) {
	top, err := topology.Resolve(topology.Options{
		Name:   "hadb",
		Engine: &engine.Identity{Kind: "build", Path: "/builds/install.out"},
	})
	if err != nil {
		t.Fatal(err)
	}
	top.Image = "csb-base:test"
	line := strings.Join(NodePlan(top, top.Nodes[0], "/work/hadb", "/res", 1000, 1000), " ")
	if !strings.Contains(line, "--ulimit core=-1:-1") {
		t.Errorf("a node is started with no core limit raised: %s", line)
	}
}

// Raising the limit is necessary and not sufficient: core_pattern decides where
// the core goes, is machine-wide rather than per-container, and when it pipes to
// a host crash handler a container's core is dropped with nothing said. csb
// cannot change it, so it has to name it.
func TestCorePatternNoteFiresOnlyWhenCoresAreLost(t *testing.T) {
	dir := "/home/u/.local/share/csb/clusters/hadb/work/<node>/db"

	msg := CorePatternNote("|/usr/share/apport/apport -p%p -s%s", dir)
	if msg == "" {
		t.Fatal("a piped core_pattern loses container cores and was not reported")
	}
	for _, want := range []string{"apport", "sysctl", CoreDest, dir} {
		if !strings.Contains(msg, want) {
			t.Errorf("the note does not carry %q: %s", want, msg)
		}
	}

	// A pattern that writes a file is not a problem, and saying so anyway would
	// be noise on every create.
	for _, ok := range []string{"core", "/db/core.%e.%p", "/var/cores/%e-%p", ""} {
		if got := CorePatternNote(ok, dir); got != "" {
			t.Errorf("core_pattern %q writes a file and was reported anyway: %s", ok, got)
		}
	}
}
