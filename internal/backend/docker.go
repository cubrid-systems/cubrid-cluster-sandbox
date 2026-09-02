// Package backend turns a topology into containers. It shells out to docker
// rather than using the SDK, because the command line is what a user can
// reproduce by hand (ADR-001), and every plan is a []string a test can read.
//
// The engine is never in an image. A host-built tree is bind-mounted read-only,
// so rebuilding the engine rebuilds nothing here -- that is DESIGN.md §2 G2, and
// it is the difference between an engine image and a base image.
package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/run"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/topology"
)

// baseDockerfile is the whole recipe. It changes when the assembly's needs
// change, and never when the engine does.
//
// Every line is load-bearing: iputils-ping because hb_check_ping shells out to
// ping and its absence returns 127, which the caller reads as a failed ping --
// so an image without it makes every master demote itself on any heartbeat loss;
// iproute2 because the partition is a route operation; procps because the
// inspector reads process state; python3 because seeding and the load driver run
// inside the node.
const baseDockerfile = `FROM ubuntu:24.04
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      iproute2 iputils-ping procps python3 ca-certificates \
 && rm -rf /var/lib/apt/lists/*
`

// BaseImage is tagged by the hash of its own recipe, so a changed recipe is a
// different image and an unchanged one is never rebuilt.
func BaseImage() string {
	sum := sha256.Sum256([]byte(baseDockerfile))
	return "csb-base:" + hex.EncodeToString(sum[:])[:12]
}

type Docker struct {
	R *run.Runner
}

func (d *Docker) docker(ctx context.Context, args ...string) (*run.Result, error) {
	res, err := d.R.Run(ctx, "docker", args...)
	if err != nil {
		return res, fmt.Errorf("docker could not be run: %w", err)
	}
	if res.ExitCode != 0 {
		return res, fmt.Errorf("docker %s exited %d: %s",
			strings.Join(args, " "), res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return res, nil
}

// EnsureImage builds the base image if this machine does not have it. Returns
// true when it had to build, which the caller reports because the first run of
// the tool is otherwise a mysterious minute.
func (d *Docker) EnsureImage(ctx context.Context) (bool, error) {
	tag := BaseImage()
	if _, err := d.R.Run(ctx, "docker", "image", "inspect", tag); err == nil {
		if res, _ := d.R.Run(ctx, "docker", "image", "inspect", tag); res != nil && res.ExitCode == 0 {
			return false, nil
		}
	}
	dir, err := os.MkdirTemp("", "csb-base-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(baseDockerfile), 0o644); err != nil {
		return false, err
	}
	if _, err := d.docker(ctx, "build", "-q", "-t", tag, dir); err != nil {
		return false, err
	}
	return true, nil
}

func (d *Docker) EnsureNetwork(ctx context.Context, name, cluster string) error {
	if res, _ := d.R.Run(ctx, "docker", "network", "inspect", name); res != nil && res.ExitCode == 0 {
		return nil
	}
	_, err := d.docker(ctx, "network", "create", "--label", "csb.cluster="+cluster, name)
	return err
}

// NodePlan is the argv for one container, kept separate from running it so the
// container requirements in docs/design/03-assembly.md §4 can be asserted
// without a docker daemon.
func NodePlan(t *topology.Topology, node topology.Node, workdir string, uid, gid int) []string {
	args := []string{
		"run", "-d",
		"--name", node.Name,
		"--hostname", node.Name, // the heartbeat resolves peers by hostname
		"--network", t.Network,
		"--init",              // without a reaping PID 1, heartbeat stop never returns
		"--cap-add=NET_ADMIN", // the fault mechanisms are route and qdisc operations
		"--shm-size", t.Resources.ShmSize,
		"--user", strconv.Itoa(uid) + ":" + strconv.Itoa(gid), // files stay editable on the host
		"--label", "csb.cluster=" + t.Cluster,
		"--label", "csb.node=" + node.Name,
		"--label", "csb.role=" + node.Role,
	}
	if t.Resources.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(t.Resources.CPUs, 'g', -1, 64))
	}
	if t.Engine != nil && t.Engine.Path != "" {
		args = append(args, "-v", t.Engine.Path+":/opt/cubrid-ro:ro")
	}
	nodeWork := filepath.Join(workdir, node.Name)
	args = append(args,
		"-v", workdir+":/work",
		"-v", filepath.Join(nodeWork, "db")+":/db", // the same container path on every node
		"-e", "HOME=/work/"+node.Name,
		"-e", "CUBRID_DATABASES=/db",
		// The engine's log timestamps carry no zone, so the record would have to
		// guess one. Pinning it removes the guess: everything a node writes is
		// UTC, which is what the record stores.
		"-e", "TZ=UTC",
		t.Image,
		"sleep", "infinity",
	)
	return args
}

// CreateNode makes one container's directories and starts it.
func (d *Docker) CreateNode(ctx context.Context, t *topology.Topology, node topology.Node, workdir string, uid, gid int) error {
	if err := os.MkdirAll(filepath.Join(workdir, node.Name, "db"), 0o755); err != nil {
		return err
	}
	_, err := d.docker(ctx, NodePlan(t, node, workdir, uid, gid)...)
	return err
}

// NodeEnv is the environment every command on a node needs. The engine finds
// its own tree through CUBRID, and both configuration files are named
// explicitly rather than left to a search path.
func NodeEnv(node, db string) []string {
	c := "/work/" + node + "/cubrid"
	return []string{
		"CUBRID=" + c,
		"CUBRID_DATABASES=/db",
		"CUBRID_CONF_FILE=" + c + "/conf/cubrid.conf",
		"CUBRID_HA_CONF_FILE=" + c + "/conf/cubrid_ha.conf",
		"PATH=" + c + "/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"LD_LIBRARY_PATH=" + c + "/lib:" + c + "/cci/lib",
	}
}

// Exec runs a shell command inside a node with that environment.
func (d *Docker) Exec(ctx context.Context, node, db, command string) (*run.Result, error) {
	args := []string{"exec"}
	for _, e := range NodeEnv(node, db) {
		args = append(args, "-e", e)
	}
	args = append(args, node, "bash", "-lc", command)
	return d.R.Run(ctx, "docker", args...)
}

type NodeState struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Role    string `json:"role"`
}

// Nodes reports what is actually running for a cluster, which is where cluster
// state comes from: the world, not a lock file.
func (d *Docker) Nodes(ctx context.Context, cluster string) ([]NodeState, error) {
	res, err := d.docker(ctx, "ps", "-a",
		"--filter", "label=csb.cluster="+cluster,
		"--format", "{{.Names}}\t{{.State}}\t{{.Label \"csb.role\"}}")
	if err != nil {
		return nil, err
	}
	var out []NodeState
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		st := NodeState{Name: f[0]}
		if len(f) > 1 {
			st.Running = f[1] == "running"
		}
		if len(f) > 2 {
			st.Role = f[2]
		}
		out = append(out, st)
	}
	return out, nil
}

// Destroy removes the containers and the network. It reports what it removed
// rather than failing on what was already gone: destroy after an interrupted
// create is the normal case, not the exception.
func (d *Docker) Destroy(ctx context.Context, cluster, network string) (removed []string, err error) {
	nodes, nerr := d.Nodes(ctx, cluster)
	if nerr != nil {
		return nil, nerr
	}
	for _, n := range nodes {
		if _, e := d.docker(ctx, "rm", "-f", n.Name); e == nil {
			removed = append(removed, n.Name)
		}
	}
	if res, _ := d.R.Run(ctx, "docker", "network", "rm", network); res != nil && res.ExitCode == 0 {
		removed = append(removed, network)
	}
	return removed, nil
}
