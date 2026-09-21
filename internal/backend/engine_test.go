package backend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/run"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/topology"
)

// Each of these asserts one of the four differences engine.go records, in the
// spelling that was measured against the two CLIs. They are here because none
// of the four fails loudly: the wrong identity flag surfaces as a permission
// denied inside a node, the missing capability as a split-brain scenario that
// cannot discriminate, and either wrong template as an empty string that every
// caller reads as "nothing is there".

func TestPodmanNodePlanKeepsFilesEditableAndCanPing(t *testing.T) {
	top, err := topology.Resolve(topology.Options{Name: "hadb", Backend: "podman"})
	if err != nil {
		t.Fatal(err)
	}
	top.Image = "csb-base:test"
	if os.Geteuid() == 0 {
		// Rootless is podman's default and not its only mode: as root the
		// identity and capability remedies are docker's, and asserting them
		// here would be asserting the wrong half of the difference.
		t.Skip("these two differences are the rootless ones")
	}
	line := strings.Join(NodePlan(KindPodman, top, top.Nodes[0], "/work/hadb", "/res", 1000, 1000), " ")

	// Difference 1: --user names a uid inside the user namespace under rootless
	// podman, which maps to a subuid the invoking user does not own, so the
	// node cannot write its own bind-mounted state directory.
	if !strings.Contains(line, "--userns=keep-id") {
		t.Errorf("a rootless node must map the invoking user, or its files are not editable on the host: %s", line)
	}
	if strings.Contains(line, "--user ") {
		t.Errorf("--user under rootless podman is the flag that breaks operation 11: %s", line)
	}
	// Difference 2: the engine's own split-brain discrimination is a ping.
	if !strings.Contains(line, "--cap-add=NET_RAW") {
		t.Errorf("without NET_RAW a rootless node cannot open an ICMP socket, and ha_ping_hosts is one: %s", line)
	}
	if !strings.Contains(line, "--cap-add=NET_ADMIN") {
		t.Errorf("the fault verbs are route and qdisc operations on either backend: %s", line)
	}
}

func TestDockerNodePlanIsUnchangedByTheSecondBackend(t *testing.T) {
	top, err := topology.Resolve(topology.Options{Name: "hadb"})
	if err != nil {
		t.Fatal(err)
	}
	top.Image = "csb-base:test"
	line := strings.Join(NodePlan(KindDocker, top, top.Nodes[0], "/work/hadb", "/res", 1000, 1000), " ")

	if !strings.Contains(line, "--user 1000:1000") {
		t.Errorf("docker still owns its files by uid: %s", line)
	}
	if strings.Contains(line, "keep-id") {
		t.Errorf("keep-id is podman's and docker does not take it: %s", line)
	}
	// NET_RAW is the rootless remedy, and adding it where it is not needed
	// widens a container for nothing.
	if strings.Contains(line, "NET_RAW") {
		t.Errorf("a docker node already has ICMP and should not be given NET_RAW: %s", line)
	}
}

// TestLabelTemplateIsTheSpellingEachCLIParses is difference 4, and the reason
// it is a test rather than a comment: podman answers the docker spelling by
// writing a template error where the value should be and exiting 0, so a wrong
// template here is a cluster that reports LIVE=no while it is serving.
func TestLabelTemplateIsTheSpellingEachCLIParses(t *testing.T) {
	if got, want := KindDocker.LabelTemplate("csb.role"), `{{.Label "csb.role"}}`; got != want {
		t.Errorf("docker: got %s, want %s", got, want)
	}
	// podman's psReporter has no .Label method, and exposes .Labels as a map
	// where docker's is the joined string k=v,k=v.
	if got, want := KindPodman.LabelTemplate("csb.role"), `{{index .Labels "csb.role"}}`; got != want {
		t.Errorf("podman: got %s, want %s", got, want)
	}
}

// TestGatewayTemplateIsTheSpellingEachCLIParses is difference 3, which failed
// the same quiet way: an empty gateway is a cluster with no witness, and the
// two split-brain flavours stop being different scenarios.
func TestGatewayTemplateIsTheSpellingEachCLIParses(t *testing.T) {
	if got, want := KindDocker.GatewayTemplate(), "{{(index .IPAM.Config 0).Gateway}}"; got != want {
		t.Errorf("docker: got %s, want %s", got, want)
	}
	if got, want := KindPodman.GatewayTemplate(), "{{range .Subnets}}{{.Gateway}}{{end}}"; got != want {
		t.Errorf("podman: got %s, want %s", got, want)
	}
}

func TestDetectPrefersWhatIsNamedOverWhatIsInstalled(t *testing.T) {
	t.Setenv(BackendEnv, "podman")
	if got := Detect(); got != KindPodman {
		t.Errorf("$%s names the backend on a machine that has both: got %q", BackendEnv, got)
	}
	// A value that is not a backend is ignored rather than driven: Cmd() would
	// otherwise hand an arbitrary string to exec.
	t.Setenv(BackendEnv, "containerd")
	if got := Detect(); !got.Valid() {
		t.Errorf("an unknown $%s must fall back to a backend csb knows: got %q", BackendEnv, got)
	}
}

func TestTheEmptyBackendIsDocker(t *testing.T) {
	// Every cluster made before the field existed recorded nothing, and has to
	// keep being reached with what made it.
	if got := Kind("").Cmd(); got != "docker" {
		t.Errorf("a cluster with no recorded backend is a docker cluster: got %q", got)
	}
}

// fakeBackends puts a stub `docker` and a stub `podman` on PATH, each exiting
// with the given code for `inspect`. It is how the wrong-backend path is tested
// without two container engines and a real container.
func fakeBackends(t *testing.T, dockerExit, podmanExit int) {
	t.Helper()
	dir := t.TempDir()
	for name, code := range map[string]int{"docker": dockerExit, "podman": podmanExit} {
		script := fmt.Sprintf("#!/bin/sh\nexit %d\n", code)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

// TestHasContainerAsksTheBackendItWasGiven is the lookup behind the message
// that turns "the cluster is gone" into "you asked the wrong tool". It only
// ever runs against the backend that is *not* in use, so asking the wrong one
// would make the correction itself wrong.
func TestHasContainerAsksTheBackendItWasGiven(t *testing.T) {
	// podman holds the container; docker does not.
	fakeBackends(t, 1, 0)

	if (&Docker{R: &run.Runner{}, E: KindDocker}).HasContainer(context.Background(), "pmha-n1") {
		t.Error("docker reported a container it does not have")
	}
	if !(&Docker{R: &run.Runner{}, E: KindPodman}).HasContainer(context.Background(), "pmha-n1") {
		t.Error("podman did not report the container it has")
	}
}

func TestHasContainerIsFalseWhenTheBackendIsNotThere(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if (&Docker{R: &run.Runner{}, E: KindDocker}).HasContainer(context.Background(), "n1") {
		t.Error("a backend that is not installed cannot hold a container")
	}
}
