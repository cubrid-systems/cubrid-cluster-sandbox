package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/assembly"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/backend"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/engine"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/record"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/run"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/topology"
)

// repeatable collects a flag given more than once, which --set and --set-hidden
// both are.
type repeatable []string

func (r *repeatable) String() string     { return strings.Join(*r, ",") }
func (r *repeatable) Set(v string) error { *r = append(*r, v); return nil }

func createFlags(fs *flag.FlagSet) {
	fs.String("name", "", "cluster name (default: the --cluster value, else hadb)")
	fs.String("preset", "ha", "ha or single")
	fs.Int("nodes", 0, "node count (default: the preset's)")
	fs.String("build", "", "path to a CUBRID install tree, bind-mounted read-only")
	fs.String("version", "", "a released version to fetch (not built yet)")
	fs.String("db", "", "database name (default: the cluster name)")
	fs.String("image", "", "base image (default: the one csb builds from its own recipe)")
	fs.String("ping-mode", "icmp", "icmp, tcp or none")
	fs.Bool("with-broker", false, "run a broker, which is the door quiesce closes")
	fs.Float64("cpus", 0, "CPU quota per node; host-load profiles are meaningless without it")
	fs.Var(&repeatable{}, "set", "key=value, validated (repeatable)")
	fs.Var(&repeatable{}, "set-hidden", "key=value, written unvalidated (repeatable)")
}

func repeated(c *Ctx, name string) []string {
	f := c.fs.Lookup(name)
	if f == nil {
		return nil
	}
	if r, ok := f.Value.(*repeatable); ok {
		return *r
	}
	return nil
}

func cmdClusterCreate(c *Ctx) (any, error) {
	name := c.str("name")
	if name == "" {
		name = c.Cluster
	}
	if name == "" {
		name = "hadb"
	}
	c.Cluster = name
	c.Env.Cluster = name

	if c.str("version") != "" {
		return nil, Failed("not_implemented",
			"--version is specified but not built yet; use --build PATH (docs/design/02-topology.md §3)")
	}
	buildPath := c.str("build")
	if buildPath == "" {
		return nil, Usage("cluster create needs --build PATH (a CUBRID install tree)")
	}

	r := &run.Runner{Verbose: c.Verbose, Log: c.Err}
	id, err := engine.Resolve(c.Ctx, buildPath, r)
	if err != nil {
		return nil, Precondition("no_engine", "%v", err)
	}
	if id.Version == "" {
		c.Note("engine_identity_thin", SevWarn,
			"cubrid_rel did not report a version, so the describe artifact cannot say which build this was")
	}

	cpus, _ := strconv.ParseFloat(c.str("cpus"), 64)
	nodes, _ := strconv.Atoi(c.str("nodes"))
	t, err := topology.Resolve(topology.Options{
		Name: name, Preset: c.str("preset"), Nodes: nodes, DB: c.str("db"),
		Image: c.str("image"), PingMode: c.str("ping-mode"),
		WithBroker: c.fs.Lookup("with-broker").Value.String() == "true",
		CPUs:       cpus, Set: repeated(c, "set"), SetHidden: repeated(c, "set-hidden"),
		Engine: id,
	})
	if err != nil {
		return nil, Usage("%v", err)
	}
	if t.Image == "" {
		t.Image = backend.BaseImage()
	}
	if len(t.Parameters.Hidden) > 0 {
		c.Note("hidden_parameter_set", SevWarn,
			"this cluster carries unvalidated parameters ("+strings.Join(t.HiddenKeys(), ", ")+
				"); it may be in a state the engine's documentation does not describe")
	}

	d := &backend.Docker{R: r}
	built, err := d.EnsureImage(c.Ctx)
	if err != nil {
		return nil, Failed("image_unavailable", "%v", err)
	}
	if built {
		c.Note("base_image_built", SevInfo,
			"built the base image "+t.Image+"; this happens when its recipe changes, never when the engine does")
	}

	// The build is bind-mounted, so a tree built on a newer distribution than the
	// image fails to load. Catch it with that sentence rather than with a linker
	// error (docs/design/02-topology.md §3).
	if id.MinGlibc != "" {
		if have, gerr := imageGlibc(c.Ctx, r, t.Image); gerr == nil && have != "" {
			if less(have, id.MinGlibc) {
				return nil, Precondition("glibc_too_old",
					"the engine at %s needs glibc %s and the image %s has %s; build against an older distribution or choose another image",
					id.Path, id.MinGlibc, t.Image, have)
			}
		} else {
			c.Note("glibc_unchecked", SevWarn, "could not read the image's glibc version, so the build was not checked against it")
		}
	}

	if err := c.Store.EnsureCluster(name); err != nil {
		return nil, Failed("store_unwritable", "%v", err)
	}
	workdir := filepath.Join(c.Store.ClusterDir(name), "work")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return nil, Failed("store_unwritable", "%v", err)
	}
	b, _ := json.MarshalIndent(t, "", "  ")
	if err := os.WriteFile(c.Store.DescribePath(name), append(b, '\n'), 0o644); err != nil {
		return nil, Failed("store_unwritable", "%v", err)
	}
	// The record opens here, and it keeps the artifact as it stood at that
	// moment: a timeline without the topology it ran against is not evidence.
	if c.Record == nil {
		c.Record = record.Open(c.Store.RecordPath(name))
	}
	if err := c.Record.SnapshotDescribe(append(b, '\n')); err != nil {
		c.Note("describe_not_snapshotted", SevWarn, err.Error())
	}

	if err := d.EnsureNetwork(c.Ctx, t.Network, t.Cluster); err != nil {
		return nil, Failed("network_failed", "%v", err)
	}
	uid, gid := os.Getuid(), os.Getgid()
	for _, n := range t.Nodes {
		if err := d.CreateNode(c.Ctx, t, n, workdir, uid, gid); err != nil {
			return nil, Failed("node_failed", "%v", err)
		}
	}

	a := &assembly.Assembler{D: d, T: t, Workdir: workdir}
	if !c.Quiet && !c.JSON {
		a.Log = c.Out
	}
	for _, n := range t.Nodes {
		if err := a.WriteConfig(n, id.Path); err != nil {
			return nil, Failed("config_failed", "%v", err)
		}
	}

	states, err := a.Up(c.Ctx)
	if err != nil {
		st, _ := a.State(c.Ctx)
		return nil, &Error{Code: ExitTimeout, Note: "did_not_reach_serving",
			Msg: fmt.Sprintf("cluster %s stopped at %q: %v (csb cluster up resumes it)", t.Cluster, st, err)}
	}

	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "cluster %s: %d node(s) on %s, state serving\n", t.Cluster, len(t.Nodes), t.Network)
		for _, n := range t.Nodes {
			fmt.Fprintf(c.Out, "  %-16s %-10s %s\n", n.Name, n.Role, states[n.Name])
		}
		for _, n := range c.Env.Notes {
			fmt.Fprintf(c.Err, "note: %s: %s\n", n.Code, n.Message)
		}
	}
	return map[string]any{"state": assembly.StateServing, "topology": t, "workdir": workdir, "nodes": states}, nil
}

// loadCluster rebuilds the assembler for a cluster that already exists, from the
// describe artifact rather than from flags.
func loadCluster(c *Ctx) (*assembly.Assembler, *topology.Topology, error) {
	if err := requireCluster(c); err != nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(c.Store.DescribePath(c.Cluster))
	if err != nil {
		return nil, nil, Precondition("no_describe", "cluster %q has no describe artifact", c.Cluster)
	}
	var t topology.Topology
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, nil, Failed("describe_malformed", "%v", err)
	}
	d := &backend.Docker{R: &run.Runner{Verbose: c.Verbose, Log: c.Err}}
	a := &assembly.Assembler{D: d, T: &t, Workdir: filepath.Join(c.Store.ClusterDir(c.Cluster), "work")}
	if !c.Quiet && !c.JSON {
		a.Log = c.Out
	}
	return a, &t, nil
}

func cmdClusterUp(c *Ctx) (any, error) {
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	for _, n := range t.Nodes {
		if res, e := a.D.R.Run(c.Ctx, "docker", "start", n.Name); e != nil || res.ExitCode != 0 {
			return nil, Precondition("no_container",
				"%s is not there; cluster create builds it", n.Name)
		}
	}
	if t.Engine != nil && t.Engine.Path != "" {
		for _, n := range t.Nodes {
			if err := a.WriteConfig(n, t.Engine.Path); err != nil {
				return nil, Failed("config_failed", "%v", err)
			}
		}
	}
	states, err := a.Up(c.Ctx)
	if err != nil {
		st, _ := a.State(c.Ctx)
		return nil, &Error{Code: ExitTimeout, Note: "did_not_reach_serving",
			Msg: fmt.Sprintf("cluster %s stopped at %q: %v", t.Cluster, st, err)}
	}
	if a.Forced {
		c.Note("promotion_forced", SevWarn,
			"the master held to_be_active with its applier drained, which a cleanly stopped group does on restart; "+
				"the promotion was completed explicitly after checking that nothing was left to apply")
	}
	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "cluster %s: state serving\n", t.Cluster)
		for _, n := range c.Env.Notes {
			fmt.Fprintf(c.Err, "note: %s: %s\n", n.Code, n.Message)
		}
	}
	return map[string]any{"state": assembly.StateServing, "nodes": states, "promotion_forced": a.Forced}, nil
}

func cmdClusterDown(c *Ctx) (any, error) {
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	if err := a.Down(c.Ctx); err != nil {
		return nil, Failed("down_failed", "%v", err)
	}
	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "cluster %s: stopped\n", t.Cluster)
	}
	return map[string]any{"state": assembly.StateDefined}, nil
}

var glibcRe = regexp.MustCompile(`(\d+\.\d+)\s*$`)

func imageGlibc(ctx context.Context, r *run.Runner, image string) (string, error) {
	res, err := r.Run(ctx, "docker", "run", "--rm", image, "ldd", "--version")
	if err != nil || res.ExitCode != 0 {
		return "", fmt.Errorf("ldd --version in %s failed", image)
	}
	first := strings.SplitN(strings.TrimSpace(res.Stdout), "\n", 2)[0]
	if m := glibcRe.FindStringSubmatch(first); m != nil {
		return m[1], nil
	}
	return "", fmt.Errorf("could not parse %q", first)
}

// less reports whether version a is older than b, for dotted numeric versions.
func less(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func destroyFlags(fs *flag.FlagSet) {
	fs.Bool("purge", false, "also remove the describe artifact and the run record")
}

func cmdClusterDestroy(c *Ctx) (any, error) {
	if err := requireCluster(c); err != nil {
		return nil, err
	}
	var t topology.Topology
	if b, err := os.ReadFile(c.Store.DescribePath(c.Cluster)); err == nil {
		_ = json.Unmarshal(b, &t)
	}
	network := t.Network
	if network == "" {
		network = c.Cluster + "-net"
	}

	d := &backend.Docker{R: &run.Runner{Verbose: c.Verbose, Log: c.Err}}
	removed, err := d.Destroy(c.Ctx, c.Cluster, network)
	if err != nil {
		return nil, Failed("destroy_failed", "%v", err)
	}

	workdir := filepath.Join(c.Store.ClusterDir(c.Cluster), "work")
	if err := os.RemoveAll(workdir); err != nil {
		c.Note("workdir_not_removed", SevWarn, err.Error())
	}

	purge := c.fs.Lookup("purge").Value.String() == "true"
	if purge {
		if err := os.RemoveAll(c.Store.ClusterDir(c.Cluster)); err != nil {
			return nil, Failed("destroy_failed", "%v", err)
		}
	} else {
		// The record is evidence. Destroying the cluster is not a reason to
		// destroy what it did -- --purge is.
		c.Note("record_kept", SevInfo,
			"the describe artifact and the run record are kept; --purge removes them too")
	}

	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "removed %d object(s): %s\n", len(removed), strings.Join(removed, " "))
		for _, n := range c.Env.Notes {
			fmt.Fprintf(c.Err, "note: %s: %s\n", n.Code, n.Message)
		}
	}
	return map[string]any{"removed": removed, "purged": purge}, nil
}

func cmdNodeExec(c *Ctx) (any, error) {
	a, _, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	if len(c.Args) < 2 {
		return nil, Usage("node exec needs a selector and a command: node exec master -- csql ...")
	}
	if _, err := ParseSelector(c.Args[0]); err != nil {
		return nil, err
	}
	names, err := a.Resolve(c.Ctx, c.Args[0])
	if err != nil {
		return nil, Precondition("unresolved_selector", "%v", err)
	}
	command := strings.Join(c.Args[1:], " ")

	out := map[string]any{}
	worst := 0
	for _, n := range names {
		res, err := a.D.Exec(c.Ctx, n, a.T.DB, command)
		if err != nil {
			return nil, Failed("exec_failed", "%v", err)
		}
		out[n] = map[string]any{"exit": res.ExitCode, "stdout": res.Stdout, "stderr": res.Stderr}
		if res.ExitCode > worst {
			worst = res.ExitCode
		}
		if !c.JSON {
			if len(names) > 1 {
				fmt.Fprintf(c.Out, "== %s\n", n)
			}
			fmt.Fprint(c.Out, res.Stdout)
			fmt.Fprint(c.Err, res.Stderr)
		}
	}
	if worst != 0 {
		// The command ran; it is its exit status that is non-zero, and a caller
		// needs that distinguished from the tool failing to run it.
		c.Note("remote_exit_nonzero", SevWarn, fmt.Sprintf("the command exited %d on at least one node", worst))
	}
	return out, nil
}
