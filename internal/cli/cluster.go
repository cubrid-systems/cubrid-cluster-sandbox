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
	fs.String("network", "docker", "docker (one host's bridge) or tailnet (nodes join a tailnet)")
	fs.String("ts-authkey", "", "tailnet auth key; or CSB_TS_AUTHKEY. Never stored in the artifact")
	fs.String("ping-host", "", "the witness a node pings to tell 'the peer is gone' from 'I am gone'")
	fs.Bool("with-broker", false, "run a broker, which is the door quiesce closes")
	fs.Float64("cpus", 0, "CPU quota per node; host-load profiles are meaningless without it")
	fs.Var(&repeatable{}, "set", "key=value, validated (repeatable)")
	fs.Var(&repeatable{}, "set-hidden", "key=value, written unvalidated (repeatable)")
	fs.String("from", "", "a describe artifact to rebuild from")
	fs.String("from-ctp", "", "a CTP ha_repl.conf to take engine parameters from")
}

// ctpSets reads a CTP ha_repl.conf and returns the --set arguments it implies.
//
// It takes the ENGINE PARAMETERS and nothing else. The node addresses in that
// file describe machines CTP would have reached over ssh; a csb cluster's nodes
// are containers this command is about to create, so their addresses are an
// output of the create rather than an input to it -- which is what
// `describe --format ctp` writes back.
//
// Unknown keys are refused rather than carried, and named. That is the same rule
// --set follows, and the reason is the same: the engine accepts a file with a key
// it ignores, so a typo that travelled from a CTP conf would take effect nowhere
// and be reported by nothing (docs/design/02-topology.md §5).
func ctpSets(c *Ctx, path string) (sets, hidden []string, err error) {
	f, oerr := os.Open(path)
	if oerr != nil {
		return nil, nil, Precondition("no_ctp_conf", "%v", oerr)
	}
	defer f.Close()
	keys, other, perr := topology.ParseCTPConf(f)
	if perr != nil {
		return nil, nil, Failed("ctp_conf_unreadable", "%v", perr)
	}
	sets, hidden, refused := topology.CTPSets(keys)
	if len(refused) > 0 {
		return nil, nil, Usage("this CTP conf carries %d key(s) csb does not know: %s. Fix the conf or pass them with --set-hidden",
			len(refused), strings.Join(refused, ", "))
	}
	if len(hidden) > 0 {
		c.Note("hidden_from_ctp", SevWarn,
			fmt.Sprintf("%d parameter(s) from this conf are ones the engine does not advertise and are written unvalidated: %s",
				len(hidden), strings.Join(hidden, ", ")))
	}
	if url, ok := other["cubrid_download_url"]; ok && url != "" {
		c.Note("download_url_ignored", SevWarn,
			"cubrid_download_url is ignored: csb bind-mounts a build from the host and never puts an engine in an image, so --build decides what runs")
	}
	if len(sets)+len(hidden) > 0 {
		c.Note("from_ctp", SevInfo,
			fmt.Sprintf("%d engine parameter(s) taken from %s", len(sets)+len(hidden), filepath.Base(path)))
	}
	return sets, hidden, nil
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

// fromArtifact rebuilds the topology out of a describe artifact instead of out
// of flags.
//
// The artifact records the engine's identity rather than only its path, because
// a build tree does not travel. So the path is taken from --build when it is
// given, or from the artifact when that path happens to exist here, and the two
// identities are compared: reproducing "the same cluster" against a different
// commit is the failure this field exists to catch.
func fromArtifact(c *Ctx, path string) (*topology.Topology, *engine.Identity, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, Usage("cannot read %s: %v", path, err)
	}
	var t topology.Topology
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, nil, Usage("%s is not a describe artifact: %v", path, err)
	}
	if t.Schema != topology.Schema {
		c.Note("schema_mismatch", SevWarn,
			"the artifact says schema "+t.Schema+" and this tool speaks "+topology.Schema)
	}
	if len(t.Nodes) == 0 {
		return nil, nil, Usage("%s has no nodes", path)
	}

	buildPath := c.str("build")
	if buildPath == "" && t.Engine != nil {
		if _, serr := os.Stat(t.Engine.Path); serr == nil {
			buildPath = t.Engine.Path
		}
	}
	if buildPath == "" {
		want := "an engine"
		if t.Engine != nil && t.Engine.Build != "" {
			want = t.Engine.Build
		}
		return nil, nil, Precondition("no_engine",
			"the artifact was built against %s and that tree is not on this machine; pass --build PATH to a tree of your own", want)
	}

	r := &run.Runner{Verbose: c.Verbose, Log: c.Err}
	id, err := engine.Resolve(c.Ctx, buildPath, r)
	if err != nil {
		return nil, nil, Precondition("no_engine", "%v", err)
	}
	if t.Engine != nil && t.Engine.Build != "" && id.Build != "" && t.Engine.Build != id.Build {
		// Not fatal: reproducing against a different build is often the point.
		// Saying nothing about it is what would be wrong.
		c.Note("engine_differs", SevWarn,
			"the artifact was taken against "+t.Engine.Build+" and this tree is "+id.Build+
				"; the topology is the same and the engine is not")
	}
	t.Engine = id

	if n := c.str("name"); n != "" && n != t.Cluster {
		// Everything else is derived from the name, so a rename is a rebuild of
		// the derived fields rather than a string substitution.
		rebuilt, rerr := topology.Resolve(topology.Options{
			Name: n, Preset: t.Preset, Nodes: len(t.Nodes), Image: t.Image,
			PingMode: t.PingMode, WithBroker: t.WithBroker,
			CPUs: t.Resources.CPUs, ShmSize: t.Resources.ShmSize, Engine: id,
		})
		if rerr != nil {
			return nil, nil, Usage("%v", rerr)
		}
		rebuilt.Parameters = t.Parameters
		t = *rebuilt
	}
	return &t, id, nil
}

func cmdClusterCreate(c *Ctx) (any, error) {
	if from := c.str("from"); from != "" {
		return createFrom(c, from)
	}
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
	set, setHidden := repeated(c, "set"), repeated(c, "set-hidden")
	if conf := c.str("from-ctp"); conf != "" {
		fromConf, fromHidden, cerr := ctpSets(c, conf)
		if cerr != nil {
			return nil, cerr
		}
		// The conf comes first so an explicit --set on the command line wins:
		// later keys overwrite earlier ones, and a person typing an override
		// means it more than a file does.
		set = append(fromConf, set...)
		setHidden = append(fromHidden, setHidden...)
	}
	t, err := topology.Resolve(topology.Options{
		Name: name, Preset: c.str("preset"), Nodes: nodes, DB: c.str("db"),
		Image: c.str("image"), PingMode: c.str("ping-mode"), Network: c.str("network"),
		WithBroker: c.fs.Lookup("with-broker").Value.String() == "true",
		CPUs:       cpus, Set: set, SetHidden: setHidden,
		Engine: id,
	})
	if err != nil {
		return nil, Usage("%v", err)
	}
	// standUp is the half both create paths share: the image, the libc check,
	// the artifact, the containers and the assembly. Splitting it is what lets
	// `create --from` be the same operation rather than a second implementation
	// of it that drifts.
	return standUp(c, t, id)
}

func standUp(c *Ctx, t *topology.Topology, id *engine.Identity) (any, error) {
	name := t.Cluster
	c.Cluster, c.Env.Cluster = name, name
	r := &run.Runner{Verbose: c.Verbose, Log: c.Err}
	if t.Image == "" {
		t.Image = backend.ImageFor(t)
	}
	if len(t.Parameters.Hidden) > 0 {
		c.Note("hidden_parameter_set", SevWarn,
			"this cluster carries unvalidated parameters ("+strings.Join(t.HiddenKeys(), ", ")+
				"); it may be in a state the engine's documentation does not describe")
	}

	d := &backend.Docker{R: r}
	built, err := d.EnsureImage(c.Ctx, t)
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
	// The network comes before the artifact, because the ping host is resolved
	// from it and the artifact has to carry what the cluster was actually built
	// with -- a describe that omits the ping host describes a different cluster.
	if err := d.EnsureNetwork(c.Ctx, t.Network, t.Cluster); err != nil {
		return nil, Failed("network_failed", "%v", err)
	}
	if t.PingMode != topology.PingNone {
		// Resolved every time rather than taken from the artifact: an address is
		// local to the machine that issued it, so a describe rebuilt elsewhere
		// carries a witness that is not this network's.
		//
		// On the bridge the witness is the gateway. On a tailnet there is no
		// gateway, so it is a third member -- this host by default, which is
		// already on the tailnet and which a cut between two nodes does not
		// touch. --ping-host names a different one, and for a cluster spanning
		// machines it should be a third machine rather than either of the two
		// (docs/design/ADR-002-backend-contract.md).
		var gw string
		var gerr error
		switch {
		case c.str("ping-host") != "":
			gw = c.str("ping-host")
		case t.NetworkKind == topology.NetTailnet:
			gw, gerr = backend.TailnetPingHost(c.Ctx, r)
		default:
			gw, gerr = d.NetworkGateway(c.Ctx, t.Network)
		}
		if gerr != nil || gw == "" {
			c.Note("no_ping_host", SevWarn,
				"could not resolve a witness for this network: this cluster cannot diagnose a partition, and a node left alone in the group will loop in to_be_active rather than finish a promotion")
		} else {
			t.PingHost = gw
		}
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

	uid, gid := os.Getuid(), os.Getgid()
	for _, n := range t.Nodes {
		if err := d.CreateNode(c.Ctx, t, n, workdir, uid, gid); err != nil {
			return nil, Failed("node_failed", "%v", err)
		}
	}

	if t.NetworkKind == topology.NetTailnet {
		key := c.str("ts-authkey")
		if key == "" {
			key = os.Getenv("CSB_TS_AUTHKEY")
		}
		addrs := map[string]string{}
		for _, n := range t.Nodes {
			if _, err := d.TailnetUp(c.Ctx, n.Name, key, n.Name); err != nil {
				return nil, Failed("tailnet_failed", "%v", err)
			}
			ip, aerr := d.TailnetAddr(c.Ctx, n.Name)
			if aerr != nil {
				return nil, Failed("tailnet_failed", "%v", aerr)
			}
			addrs[n.Name] = ip
		}
		// Every node resolves every other node's NAME to its TAILNET address.
		//
		// ha_node_list is written with names and stays that way, so nothing in
		// the assembly changes; what changes is what those names mean. Without
		// this the names would still resolve, to bridge addresses, and the
		// cluster would quietly keep talking over the bridge while believing it
		// was on the tailnet -- and a cut expressed against a tailnet address
		// would cut nothing.
		var hosts strings.Builder
		for name, ip := range addrs {
			fmt.Fprintf(&hosts, "%s %s\n", ip, name)
		}
		for _, n := range t.Nodes {
			if res, herr := d.Privileged(c.Ctx, n.Name,
				"printf '%s' "+shellQuote(hosts.String())+" >> /etc/hosts"); herr != nil || res.ExitCode != 0 {
				return nil, Failed("tailnet_failed", "%s: could not point the peer names at their tailnet addresses", n.Name)
			}
		}
		c.Note("on_a_tailnet", SevInfo,
			fmt.Sprintf("the nodes are tailnet members and address each other there; the witness for a partition is %s", t.PingHost))
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
	removed, leftBehind, err := d.Destroy(c.Ctx, c.Cluster, network)
	if err != nil {
		return nil, Failed("destroy_failed", "%v", err)
	}
	// Destroying a cluster removes it from this machine. It does not remove it
	// from a tailnet: `tailscale logout` expires the key and leaves the device
	// listed unless the auth key was ephemeral. Somebody has to know that, and
	// the moment to tell them is now rather than when they next open the admin
	// console and find machines they do not recognise.
	if len(leftBehind) > 0 {
		c.Note("tailnet_devices_remain", SevWarn,
			"these nodes were logged out of the tailnet but stay in its device list until removed from the admin console or the API: "+
				strings.Join(leftBehind, ", ")+". An ephemeral auth key removes them automatically; a reusable one does not")
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

// createFrom rebuilds a cluster from a describe artifact.
//
// The artifact is the thing this project asks a person to paste into an issue,
// so the reproduction has to be the same code path as an ordinary create --
// otherwise the two drift and the artifact stops reproducing what it says.
func createFrom(c *Ctx, path string) (any, error) {
	t, id, err := fromArtifact(c, path)
	if err != nil {
		return nil, err
	}
	res, err := standUp(c, t, id)
	if err != nil {
		return nil, err
	}

	// What was in force when the artifact was taken is reported rather than
	// re-applied. A describe that omitted its faults would hand the next person
	// a healthy cluster and a bug that does not reproduce -- but silently
	// partitioning a cluster somebody just asked to be built is a surprise, and
	// injecting a fault is a deliberate act. So: the commands, not the act.
	if b, rerr := os.ReadFile(path); rerr == nil {
		var doc struct {
			Nodes []struct {
				Name, Role string
			} `json:"nodes"`
			Faults []struct {
				Kind, Target, Mechanism, Stage string
			} `json:"faults"`
			Load *struct {
				Profile string  `json:"profile"`
				Rate    float64 `json:"rate"`
				Batch   int     `json:"batch"`
			} `json:"load"`
		}
		if json.Unmarshal(b, &doc) == nil {
			// The recorded target is a node name from the machine the artifact
			// came from, and the rebuilt cluster's names are derived from its own
			// name. So it is translated back into the role selector it stood for,
			// which is how the tool addresses nodes anyway and which survives both
			// a rename and a failover.
			sel := func(target string) string {
				for i, n := range doc.Nodes {
					if n.Name != target {
						continue
					}
					switch {
					case i == 0:
						return "master"
					case i == 1:
						return "slave"
					default:
						return fmt.Sprintf("slave[%d]", i-1)
					}
				}
				return target
			}
			var cmds []string
			for _, f := range doc.Faults {
				switch f.Kind {
				case "partition":
					cmds = append(cmds, "csb fault partition "+sel(f.Target))
				case "lag":
					cmds = append(cmds, "csb fault lag "+sel(f.Target)+" --stage "+f.Stage+" --mechanism "+f.Mechanism)
				default:
					cmds = append(cmds, "csb fault "+f.Kind+" "+sel(f.Target))
				}
			}
			if doc.Load != nil && doc.Load.Profile != "" {
				cmd := "csb load start --profile " + doc.Load.Profile
				if doc.Load.Rate > 0 {
					cmd += fmt.Sprintf(" --rate %g/s", doc.Load.Rate)
				}
				if doc.Load.Batch > 1 {
					cmd += fmt.Sprintf(" --batch %d", doc.Load.Batch)
				}
				cmds = append(cmds, cmd)
			}
			if len(cmds) > 0 {
				c.Note("situation_not_restored", SevWarn,
					"the artifact was taken with something in force; the cluster is healthy and idle. To reproduce the situation: "+strings.Join(cmds, " ; "))
				if !c.JSON && !c.Quiet {
					fmt.Fprintln(c.Err, "note: the artifact recorded a situation. To reproduce it:")
					for _, cmd := range cmds {
						fmt.Fprintf(c.Err, "  %s\n", cmd)
					}
				}
			}
		}
	}
	return res, nil
}

// shellQuote wraps a value for a single-quoted shell string, which is how the
// hosts fragment travels into a node without a here-doc.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
