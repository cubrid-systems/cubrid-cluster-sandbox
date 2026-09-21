package cli

import (
	"flag"
	"testing"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/backend"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/topology"
)

// TestBackendForIsRecordedThenAskedThenDetected pins the precedence, because
// getting it wrong is silent in the direction that matters: a machine with both
// backends installed looks for a podman cluster with docker, finds nothing, and
// says the cluster is gone rather than that it was asked with the wrong tool.
func TestBackendForIsRecordedThenAskedThenDetected(t *testing.T) {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.String("backend", "", "")
	c := &Ctx{fs: fs}

	t.Setenv(backend.BackendEnv, "docker")
	if err := fs.Set("backend", "docker"); err != nil {
		t.Fatal(err)
	}
	if got := backendFor(c, "podman"); got != backend.KindPodman {
		t.Errorf("a cluster is reached with what made it, whatever is asked: got %q", got)
	}
	if got := backendFor(c, ""); got != backend.KindDocker {
		t.Errorf("with nothing recorded, --backend decides: got %q", got)
	}

	if err := fs.Set("backend", ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv(backend.BackendEnv, "podman")
	if got := backendFor(c, ""); got != backend.KindPodman {
		t.Errorf("with nothing recorded and nothing asked, detection decides: got %q", got)
	}

	// A command that has no --backend flag at all -- every verb but create --
	// still has to resolve one rather than fall over.
	bare := &Ctx{fs: flag.NewFlagSet("up", flag.ContinueOnError)}
	if got := backendFor(bare, "podman"); got != backend.KindPodman {
		t.Errorf("cluster up has no --backend flag and must still use the recorded one: got %q", got)
	}
}

// TestResolveRecordsTheBackendInTheArtifact is the other half: the precedence
// above is only useful if what was decided reaches describe.json. It did not
// for the first clusters stood up with podman, because create recorded the flag
// rather than the decision, and $CSB_BACKEND leaves the flag empty.
func TestResolveRecordsTheBackendInTheArtifact(t *testing.T) {
	top, err := topology.Resolve(topology.Options{Name: "hadb", Backend: "podman"})
	if err != nil {
		t.Fatal(err)
	}
	if top.Backend != "podman" {
		t.Errorf("the artifact must say what made the cluster: got %q", top.Backend)
	}
}
