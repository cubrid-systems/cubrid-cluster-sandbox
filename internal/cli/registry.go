package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/record"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/run"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/selector"
)

// notYet is the honest answer for a verb the surface defines and phase 1 has not
// built. It is exit 1 with a note, not exit 2: the command exists, so "unknown
// verb" would be a lie, and a consumer needs to tell the two apart.
func notYet(doc string) func(*Ctx) (any, error) {
	return func(c *Ctx) (any, error) {
		return nil, Failed("not_implemented",
			"%s %s is specified but not built yet — see docs/design/%s", c.Noun, c.Verb, doc)
	}
}

var registry = []Command{
	// ---- cluster ---------------------------------------------------------
	{Noun: "cluster", Verb: "create", Summary: "build a topology", Mutates: true,
		Flags: createFlags, Run: cmdClusterCreate},
	{Noun: "cluster", Verb: "up", Summary: "start an existing cluster, in the order that works", Mutates: true,
		Run: cmdClusterUp},
	{Noun: "cluster", Verb: "down", Summary: "graceful stop, servers flushed", Mutates: true,
		Run: cmdClusterDown},
	{Noun: "cluster", Verb: "destroy", Summary: "containers, network, volumes", Mutates: true,
		Flags: destroyFlags, Run: cmdClusterDestroy},
	{Noun: "cluster", Verb: "status", Summary: "per-node liveness, HA role, process state", Run: cmdClusterStatus},
	{Noun: "cluster", Verb: "describe", Summary: "the reproducible artifact", Run: cmdClusterDescribe,
		Flags: func(fs *flag.FlagSet) { fs.String("out", "", "write the artifact to a file") }},
	{Noun: "cluster", Verb: "quiesce", Summary: "block writes", Mutates: true, Flags: quiesceFlags, Run: cmdClusterQuiesce},
	{Noun: "cluster", Verb: "resume", Summary: "release writes", Mutates: true, Run: cmdClusterResume},
	{Noun: "cluster", Verb: "ls", Summary: "clusters on this machine", Run: cmdClusterLs},

	// ---- node ------------------------------------------------------------
	{Noun: "node", Verb: "start", Args: "<selector>", Summary: "start a node", Mutates: true, Run: cmdNodeStart},
	{Noun: "node", Verb: "stop", Args: "<selector>", Summary: "graceful: the server flushes", Mutates: true, Run: cmdNodeStop},
	{Noun: "node", Verb: "kill", Args: "<selector>", Summary: "crash: it does not", Mutates: true, Run: cmdNodeKill},
	{Noun: "node", Verb: "status", Args: "<selector>", Summary: "one node's state", Run: cmdNodeStatus},
	{Noun: "node", Verb: "logs", Args: "<selector>", Summary: "the log a failure is actually in", Run: notYet("01-cli.md")},
	{Noun: "node", Verb: "shell", Args: "<selector>", Summary: "a shell on the node", Run: notYet("01-cli.md")},
	{Noun: "node", Verb: "exec", Args: "<selector> -- <cmd>", Summary: "run a command on the node", Run: cmdNodeExec},

	// ---- fault -----------------------------------------------------------
	{Noun: "fault", Verb: "partition", Args: "<selector>", Summary: "cut routes, not interfaces", Mutates: true, Flags: partitionFlags, Run: cmdFaultPartition},
	{Noun: "fault", Verb: "lag", Args: "<selector>", Summary: "stage-targeted replication lag", Mutates: true, Flags: lagFlags, Run: cmdFaultLag},
	{Noun: "fault", Verb: "splitbrain", Summary: "two masters, on request", Mutates: true, Flags: splitbrainFlags, Run: cmdFaultSplitbrain},
	{Noun: "fault", Verb: "failcount", Summary: "move fail_counter deliberately", Mutates: true, Flags: failcountFlags, Run: cmdFaultFailcount},
	{Noun: "fault", Verb: "ping-unavailable", Args: "<selector>", Summary: "the engine cannot ask", Mutates: true, Run: notYet("04-faults.md")},
	{Noun: "fault", Verb: "clear", Args: "[<selector>]", Summary: "reverse a condition", Mutates: true, Run: cmdFaultClear},
	{Noun: "fault", Verb: "ls", Summary: "what is currently in force", Run: cmdFaultLs},

	// ---- repl ------------------------------------------------------------
	{Noun: "repl", Verb: "status", Summary: "both stages, against the master", Run: cmdReplStatus},
	{Noun: "repl", Verb: "watch", Summary: "sample and retain", Run: notYet("05-inspect.md")},

	// ---- ha --------------------------------------------------------------
	{Noun: "ha", Verb: "status", Summary: "roles and the group", Run: cmdHaStatus},
	{Noun: "ha", Verb: "promote", Args: "<selector>", Summary: "promote a node", Mutates: true, Run: notYet("04-faults.md")},
	{Noun: "ha", Verb: "failback", Summary: "return to the original master; interactive", Mutates: true, Run: notYet("04-faults.md")},
	{Noun: "ha", Verb: "resync", Args: "[<selector>]", Summary: "repair a diverged slave", Mutates: true, Flags: resyncFlags, Run: cmdHaResync},

	// ---- load ------------------------------------------------------------
	{Noun: "load", Verb: "start", Summary: "a rate it has to hold", Mutates: true, Flags: loadStartFlags, Run: cmdLoadStart},
	{Noun: "load", Verb: "stop", Summary: "stop the driver", Mutates: true, Flags: func(fs *flag.FlagSet) { fs.String("node", "master", "which node") }, Run: cmdLoadStop},
	{Noun: "load", Verb: "status", Summary: "requested, achieved, and whether it held", Flags: func(fs *flag.FlagSet) { fs.String("node", "master", "which node") }, Run: cmdLoadStatus},

	// ---- record ----------------------------------------------------------
	{Noun: "record", Verb: "show", Summary: "the timeline", Run: cmdRecordShow,
		Flags: func(fs *flag.FlagSet) { fs.Duration("since", 0, "only entries newer than this") }},
	{Noun: "record", Verb: "export", Summary: "timeline plus the describe that opened it", Run: cmdRecordExport,
		Flags: func(fs *flag.FlagSet) { fs.String("out", "", "file to write") }},
}

// ---- implemented commands -----------------------------------------------

type clusterRow struct {
	Name       string `json:"name"`
	HasState   bool   `json:"has_state"`
	Containers int    `json:"containers"`
}

// cmdClusterLs answers from both sides: the state this tool keeps, and what is
// actually running. They disagree after an interrupted run, and the disagreement
// is the useful part -- state is derived from the world, not from a lock file
// (docs/design/03-assembly.md §1).
func cmdClusterLs(c *Ctx) (any, error) {
	names, err := c.Store.List()
	if err != nil {
		return nil, Failed("store_unreadable", "cannot read %s: %v", c.Store.ClustersDir(), err)
	}
	rows := map[string]*clusterRow{}
	for _, n := range names {
		rows[n] = &clusterRow{Name: n, HasState: true}
	}

	r := &run.Runner{Verbose: c.Verbose, Log: c.Err}
	res, derr := r.Run(c.Ctx, "docker", "ps", "--filter", "label=csb.cluster", "--format", "{{.Label \"csb.cluster\"}}")
	switch {
	case derr != nil:
		c.Note("docker_unavailable", SevWarn,
			"docker could not be run, so this lists stored state only: "+derr.Error())
	case res.ExitCode != 0:
		c.Note("docker_unavailable", SevWarn,
			"docker exited "+fmt.Sprint(res.ExitCode)+", so this lists stored state only")
	default:
		for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if rows[line] == nil {
				rows[line] = &clusterRow{Name: line}
			}
			rows[line].Containers++
		}
	}

	out := make([]clusterRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	for _, row := range out {
		if row.HasState && row.Containers == 0 {
			c.Note("stale_state", SevWarn,
				"cluster "+row.Name+" has state on disk and nothing running; cluster create resumes it, cluster destroy removes it")
		}
	}

	if !c.JSON && !c.Quiet {
		if len(out) == 0 {
			fmt.Fprintln(c.Out, "no clusters on this machine")
		} else {
			fmt.Fprintf(c.Out, "%-20s %-8s %s\n", "NAME", "STATE", "CONTAINERS")
			for _, row := range out {
				state := "-"
				if row.HasState {
					state = "yes"
				}
				fmt.Fprintf(c.Out, "%-20s %-8s %d\n", row.Name, state, row.Containers)
			}
		}
		for _, n := range c.Env.Notes {
			fmt.Fprintf(c.Err, "note: %s: %s\n", n.Code, n.Message)
		}
	}
	return map[string]any{"clusters": out}, nil
}

func requireCluster(c *Ctx) error {
	if c.Cluster == "" {
		return Usage("no cluster named: pass --cluster NAME or set CSB_CLUSTER")
	}
	if !c.Store.Exists(c.Cluster) {
		return Precondition("no_such_cluster", "no cluster %q on this machine (csb cluster ls)", c.Cluster)
	}
	return nil
}

func cmdClusterDescribe(c *Ctx) (any, error) {
	if err := requireCluster(c); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(c.Store.DescribePath(c.Cluster))
	if os.IsNotExist(err) {
		return nil, Precondition("no_describe",
			"cluster %q has no describe artifact yet; cluster create writes one", c.Cluster)
	}
	if err != nil {
		return nil, Failed("describe_unreadable", "%v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, Failed("describe_malformed", "%s is not valid JSON: %v", c.Store.DescribePath(c.Cluster), err)
	}
	doc = describeWithFaults(c, doc)
	doc = describeWithLoad(c, doc)
	b, _ = json.MarshalIndent(doc, "", "  ")
	if out := c.str("out"); out != "" {
		if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
			return nil, Failed("describe_unwritable", "cannot write %s: %v", out, err)
		}
		if !c.JSON && !c.Quiet {
			fmt.Fprintf(c.Out, "wrote %s (%d bytes)\n", out, len(b)+1)
		}
		return doc, nil
	}
	if !c.JSON && !c.Quiet {
		c.Out.Write(b)
		if len(b) > 0 && b[len(b)-1] != '\n' {
			fmt.Fprintln(c.Out)
		}
	}
	return doc, nil
}

func sinceFlag(c *Ctx, name string) time.Duration { return c.dur(name) }

// harvest pulls the engine's own HA lines into the timeline before anything
// reads it, so a record is never missing what the engine said while the tool was
// not looking.
func harvest(c *Ctx) {
	a, t, err := loadCluster(c)
	if err != nil || c.Record == nil {
		return
	}
	if n, herr := c.Record.Harvest(a.Workdir, t.NodeNames()); herr == nil && n > 0 {
		c.Note("engine_events_harvested", SevInfo,
			fmt.Sprintf("%d engine line(s) added to the timeline", n))
	}
}

func cmdRecordShow(c *Ctx) (any, error) {
	if err := requireCluster(c); err != nil {
		return nil, err
	}
	harvest(c)
	var since time.Time
	if d := sinceFlag(c, "since"); d > 0 {
		since = time.Now().Add(-d)
	}
	entries, err := c.Record.Read(since)
	if err != nil {
		return nil, Failed("record_unreadable", "%v", err)
	}
	if !c.JSON && !c.Quiet {
		if len(entries) == 0 {
			fmt.Fprintln(c.Out, "no entries")
		}
		for _, e := range entries {
			fmt.Fprintf(c.Out, "%s  %-6s %s\n", e.T, e.Actor, e.Event)
		}
	}
	return map[string]any{"timeline": entries}, nil
}

func cmdRecordExport(c *Ctx) (any, error) {
	if err := requireCluster(c); err != nil {
		return nil, err
	}
	out := c.str("out")
	if out == "" {
		return nil, Usage("record export needs --out FILE")
	}
	harvest(c)
	entries, err := c.Record.Read(time.Time{})
	if err != nil {
		return nil, Failed("record_unreadable", "%v", err)
	}

	// The describe as it stood when the record opened, not as it stands now: the
	// cluster may have been changed since, and the timeline ran against the
	// former.
	describe := c.Record.DescribeAtOpen()
	if describe == nil {
		if b, rerr := os.ReadFile(c.Store.DescribePath(c.Cluster)); rerr == nil {
			describe = b
			c.Note("describe_not_snapshotted", SevWarn,
				"this record predates the describe snapshot, so the current artifact is carried instead")
		} else {
			c.Note("no_describe", SevWarn,
				"no describe artifact to carry: a timeline without the topology it ran against is not evidence")
		}
	}

	doc := record.Build(c.Cluster, entries, describe, invalidities(c))
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, Failed("export_failed", "%v", err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return nil, Failed("export_failed", "%v", err)
	}
	if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
		return nil, Failed("export_failed", "cannot write %s: %v", out, err)
	}
	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "wrote %s (%d entries, %d role change(s), valid=%v)\n",
			out, len(entries), len(doc.RoleChanges), doc.Validity.Valid)
		printNotes(c)
	}
	return map[string]any{"out": out, "entries": len(entries),
		"role_changes": len(doc.RoleChanges), "valid": doc.Validity.Valid}, nil
}

// ParseSelector is the shared front door for every verb that takes one, so the
// error text is identical everywhere and exit 2 means the same thing.
func ParseSelector(s string) (selector.Selector, error) {
	sel, err := selector.Parse(s)
	if err != nil {
		return sel, Usage("%v", err)
	}
	return sel, nil
}
