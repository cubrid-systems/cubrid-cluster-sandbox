package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/assembly"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/fault"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/inspect"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/load"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/record"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/topology"
)

func faultsOf(c *Ctx) map[string][]string {
	out := map[string][]string{}
	s, err := fault.Open(c.Store.ClusterDir(c.Cluster))
	if err != nil {
		return out
	}
	for _, a := range s.List {
		label := a.Kind
		if a.Mechanism != "" {
			label += "(" + a.Mechanism + ")"
		}
		out[a.Target] = append(out[a.Target], label)
	}
	return out
}

func readStatus(c *Ctx) (*inspect.Status, error) {
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	st, err := inspect.Read(c.Ctx, a.D, t)
	if err != nil {
		return nil, Failed("inspect_failed", "%v", err)
	}
	f := faultsOf(c)
	for i := range st.Nodes {
		st.Nodes[i].Faults = f[st.Nodes[i].Name]
		// The view is written by the process a lag condition suspends, so it
		// cannot report its own stall: every column freezes at a constant,
		// healthy-looking value for as long as the stall lasts. The tool knows
		// it suspended that process, so it says so rather than letting the
		// number speak for itself (docs/design/05-inspect.md §3).
		for _, lbl := range st.Nodes[i].Faults {
			if strings.HasPrefix(lbl, "lag(") && st.Nodes[i].Repl != nil {
				c.Note("stale_apply_info", SevError,
					st.Nodes[i].Name+": a lag condition is in force here, so db_ha_apply_info is frozen by the very process it would be reporting on — these figures are not a measurement of replication")
			}
		}
	}
	for _, n := range st.Notes {
		c.Note(n.Code, SevWarn, n.Message)
	}
	if len(st.Nodes) > 0 && !st.Serving() {
		c.Note("not_serving", SevWarn, "no single node is registered_and_active")
	}
	return st, nil
}

func dash(p *int) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprint(*p)
}

func cmdClusterStatus(c *Ctx) (any, error) {
	st, err := readStatus(c)
	if err != nil {
		return nil, err
	}
	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "%-16s %-5s %-13s %-26s %-9s %-7s %s\n",
			"NODE", "LIVE", "HA ROLE", "SERVER", "APPLIED", "FAIL", "FAULTS")
		for _, n := range st.Nodes {
			applied, fail := "—", "—"
			// A master has no replication position to report, and printing 0
			// there would be the same class of lie as reporting a delay the
			// source cannot support.
			if n.Repl != nil && n.Role != "active" {
				applied, fail = dash(n.Repl.Applied), dash(n.Repl.Fail)
			}
			live := "no"
			if n.Live {
				live = "yes"
			}
			faults := "—"
			if len(n.Faults) > 0 {
				faults = strings.Join(n.Faults, ",")
			}
			fmt.Fprintf(c.Out, "%-16s %-5s %-13s %-26s %-9s %-7s %s\n",
				n.Name, live, orUnknown(n.Role), orUnknown(n.Server), applied, fail, faults)
		}
		printNotes(c)
	}
	return st, nil
}

func orUnknown(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func printNotes(c *Ctx) {
	for _, n := range c.Env.Notes {
		fmt.Fprintf(c.Err, "note: %s: %s\n", n.Code, n.Message)
	}
}

func cmdNodeStatus(c *Ctx) (any, error) {
	if len(c.Args) < 1 {
		return nil, Usage("node status needs a selector")
	}
	st, err := readStatus(c)
	if err != nil {
		return nil, err
	}
	a, _, _ := loadCluster(c)
	names, rerr := a.Resolve(c.Ctx, c.Args[0])
	if rerr != nil {
		return nil, Precondition("unresolved_selector", "%v", rerr)
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var picked []inspect.Node
	for _, n := range st.Nodes {
		if want[n.Name] {
			picked = append(picked, n)
		}
	}
	if !c.JSON && !c.Quiet {
		for _, n := range picked {
			fmt.Fprintf(c.Out, "%s  live=%v  role=%s  server=%s\n", n.Name, n.Live, orUnknown(n.Role), orUnknown(n.Server))
		}
		printNotes(c)
	}
	return map[string]any{"nodes": picked}, nil
}

func cmdHaStatus(c *Ctx) (any, error) {
	st, err := readStatus(c)
	if err != nil {
		return nil, err
	}
	if !c.JSON && !c.Quiet {
		for _, n := range st.Nodes {
			fmt.Fprintf(c.Out, "%-16s %-13s %s\n", n.Name, orUnknown(n.Role), orUnknown(n.Server))
		}
		printNotes(c)
	}
	return map[string]any{"nodes": st.Nodes, "serving": st.Serving()}, nil
}

// cmdReplStatus reports two stages, never one number, and there is no field
// called "delay" (docs/design/05-inspect.md §3).
func cmdReplStatus(c *Ctx) (any, error) {
	st, err := readStatus(c)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, n := range st.Nodes {
		if n.Repl == nil {
			continue
		}
		out[n.Name] = n.Repl
		if !c.JSON && !c.Quiet {
			// Two stages, never one number, and each with its provenance. There
			// is no field called "delay".
			copy := "not reported"
			if n.Repl.CopyLag != nil {
				copy = fmt.Sprintf("%d pages behind %s", *n.Repl.CopyLag, n.Repl.RefSource)
			}
			fmt.Fprintf(c.Out, "%-16s copy=%-34s apply_lag=%s pages  fail=%s  (%s, read %s)\n",
				n.Name, copy, dash(n.Repl.ApplyLag), dash(n.Repl.Fail),
				n.Repl.Source, n.Repl.SampledAt)
		}
	}
	if !c.JSON && !c.Quiet {
		printNotes(c)
	}
	return map[string]any{"nodes": out}, nil
}

func diffFlags(fs *flag.FlagSet) {
	fs.String("table", "", "compare only this table (default: every user table)")
}

// cmdReplDiff asks the two databases what they hold, rather than asking
// replication how it is doing.
//
// It exists because those questions have different answers. A healed split brain
// left a standby permanently missing a row while `repl status` read apply_lag=0
// and fail=0 on both sides, `repl check` arrived, and `ha resync` correctly
// reported that replication was not broken -- because it was not. It was carrying
// new writes fine; it simply never carried one old one, and the engine keeps no
// view that remembers it (docs/findings/active-active-window.md).
//
// A difference is exit 1 rather than 0: the command did its job and the answer is
// bad, and a harness has to be able to tell "compared and equal" from "compared
// and not" without reading prose. Equal row counts are not equal data, and the
// output says so rather than implying more than it checked.
func cmdReplDiff(c *Ctx) (any, error) {
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	st, serr := inspect.Read(c.Ctx, a.D, t)
	if serr != nil {
		return nil, Failed("inspect_failed", "%v", serr)
	}
	var master, standby string
	for _, n := range st.Nodes {
		switch n.Server {
		case "registered_and_active":
			master = n.Name
		case "registered_and_standby":
			standby = n.Name
		}
	}
	if master == "" || standby == "" {
		return nil, Precondition("not_a_pair",
			"repl diff needs one active and one standby node; found master=%q standby=%q", master, standby)
	}
	var only []string
	if one := c.str("table"); one != "" {
		only = []string{one}
	}
	d, derr := inspect.CompareTables(c.Ctx, a.D, t, master, standby, only)
	if derr != nil {
		return nil, Failed("diff_failed", "%v", derr)
	}
	if len(d.Tables) == 0 {
		c.Note("no_user_tables", SevInfo, "this database has no user table to compare")
	}
	c.Note("row_counts_only", SevInfo,
		"this compares row counts, which is the field's own instrument and a weak one: equal counts are not equal data")
	if !c.JSON && !c.Quiet {
		for _, td := range d.Tables {
			verdict := "same"
			if !td.Same {
				verdict = "DIFFERENT"
			}
			fmt.Fprintf(c.Out, "  %-28s master=%-8d standby=%-8d %s\n", td.Table, td.Master, td.Standby, verdict)
		}
		for _, sk := range d.Skipped {
			fmt.Fprintf(c.Out, "  skipped %s\n", sk)
		}
		printNotes(c)
	}
	if len(d.Differ) > 0 {
		return d, Failed("tables_differ",
			"%d table(s) differ between %s and %s: %s. Replication may be perfectly healthy and still never carry what is missing; the field's closure is a slave rebuild",
			len(d.Differ), master, standby, strings.Join(d.Differ, ", "))
	}
	return d, nil
}

// ---- fault verbs ---------------------------------------------------------

func selectorArg(c *Ctx) (string, error) {
	if len(c.Args) < 1 {
		return "", Usage("%s %s needs a selector (master, slave, slave[n], a node name, or all)", c.Noun, c.Verb)
	}
	if _, err := ParseSelector(c.Args[0]); err != nil {
		return "", err
	}
	return c.Args[0], nil
}

func nodeAction(c *Ctx, do func(*fault.Injector, string) error) (any, error) {
	sel, err := selectorArg(c)
	if err != nil {
		return nil, err
	}
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	names, rerr := a.Resolve(c.Ctx, sel)
	if rerr != nil {
		return nil, Precondition("unresolved_selector", "%v", rerr)
	}
	inj := &fault.Injector{D: a.D, T: t}
	for _, n := range names {
		if err := do(inj, n); err != nil {
			return nil, Failed("node_action_failed", "%v", err)
		}
		if c.Record != nil {
			_ = c.Record.Append(record.ActorTool, c.Noun+"."+c.Verb, map[string]any{"node": n})
		}
		if !c.JSON && !c.Quiet {
			fmt.Fprintf(c.Out, "%s %s: %s\n", c.Noun, c.Verb, n)
		}
	}
	return map[string]any{"nodes": names}, nil
}

func cmdNodeStop(c *Ctx) (any, error) {
	return nodeAction(c, func(i *fault.Injector, n string) error { return i.Stop(c.Ctx, n) })
}
func cmdNodeKill(c *Ctx) (any, error) {
	return nodeAction(c, func(i *fault.Injector, n string) error { return i.Kill(c.Ctx, n) })
}

// cmdNodeStart starts a node and, if its applier refuses to start because its
// replication log is short of the position it was told to resume from, recopies
// that log and starts it again.
//
// The condition is not exotic and it is not caused by anything unusual: stopping
// a node's heartbeat mid-stream is enough. `ha promote` does exactly that, and a
// node put back afterwards can come up with every process reporting success and
// `cubrid heartbeat start` reporting "HA processes start: fail" -- with nothing
// in that output naming which process, because the reason is in the applier's own
// log: "logical log page N may be corrupted", which is true of a log that is
// short rather than damaged (docs/design/03-assembly.md §3).
func cmdNodeStart(c *Ctx) (any, error) {
	out, err := nodeAction(c, func(i *fault.Injector, n string) error { return i.Start(c.Ctx, n) })
	if err != nil {
		return out, err
	}
	a, t, lerr := loadCluster(c)
	if lerr != nil {
		return out, nil // started; the recovery below is a bonus, not the job
	}
	names, _ := a.Resolve(c.Ctx, c.Args[0])
	masters, merr := a.Resolve(c.Ctx, "master")
	if merr != nil || len(masters) != 1 {
		return out, nil // no single master to copy from, so nothing to recover with
	}
	for _, n := range names {
		if n == masters[0] {
			continue
		}
		// Give the node time to either join or fail. The applier writes its
		// reason seconds after `heartbeat start` returns, so asking immediately
		// finds a node that has neither joined nor explained itself yet -- which
		// is how the first version of this recovery skipped every case it was
		// written for.
		joined, short := false, false
		deadline := time.Now().Add(45 * time.Second)
		for {
			st, _ := inspect.Read(c.Ctx, a.D, t)
			for _, node := range st.Nodes {
				if node.Name == n && strings.HasPrefix(node.Server, "registered_and_") {
					joined = true
				}
			}
			if joined {
				break
			}
			if a.ApplierLogShort(c.Ctx, n) {
				short = true
				break
			}
			if time.Now().After(deadline) || c.Ctx.Err() != nil {
				break
			}
			time.Sleep(2 * time.Second)
		}
		if joined || !short {
			continue
		}
		c.Note("replication_log_short", SevWarn,
			n+" did not rejoin: its applier was told to resume from a position its replication log does not reach. Recopying the log from "+masters[0])
		if cerr := a.CopyActiveLog(c.Ctx, n, masters[0]); cerr != nil {
			return out, Failed("log_copy_failed", "%v", cerr)
		}
		inj := &fault.Injector{D: a.D, T: t}
		if serr := inj.Start(c.Ctx, n); serr != nil {
			return out, Failed("restart_failed", "%v", serr)
		}
		// Verify rather than assume. The cheap repair fixes a log that is merely
		// short; it cannot fix a recorded position that belongs to a different
		// log altogether, which is what a node demoted by a role change carries.
		// The field's answer for that case is a rebuild, and saying so beats
		// exiting 0 on a node that never came back.
		rejoined := false
		wait := time.Now().Add(60 * time.Second)
		for {
			st2, _ := inspect.Read(c.Ctx, a.D, t)
			for _, node := range st2.Nodes {
				if node.Name == n && strings.HasPrefix(node.Server, "registered_and_") {
					rejoined = true
				}
			}
			if rejoined || time.Now().After(wait) || c.Ctx.Err() != nil {
				break
			}
			time.Sleep(2 * time.Second)
		}
		if !rejoined {
			return out, Failed("rejoin_needs_rebuild",
				"%s still will not rejoin after its replication log was recopied: its recorded position belongs to a log it no longer reads, which a role change leaves behind. That is a rebuild — `csb ha resync %s --path slave` — and it is the same answer the field gives (ha_make_slavedb.sh)", n, n)
		}
		c.Note("replication_log_recopied", SevInfo, n+"'s replication log was recopied from "+masters[0]+" and the node rejoined")
	}
	return out, nil
}

func partitionFlags(fs *flag.FlagSet) {
	fs.String("from", "", "narrow the cut to this peer")
	fs.String("keep", "", "preserve reachability to this host")
	fs.String("mechanism", "blackhole", "blackhole (no route) or drop (route intact, packets discarded)")
}

func pingFlags(fs *flag.FlagSet) {
	fs.String("mechanism", "binary", "binary (the executable is not permitted) or icmp (it runs and fails)")
}

// cmdFaultPingUnavailable breaks the check rather than the network.
//
// The split-brain finding is entirely about the answer to one question, so
// "the ping host is unreachable" (fault partition) and "the ping check is
// broken" (this) are different scenarios that leave a similar-looking log
// (docs/design/04-faults.md §10). The notes below exist because both mechanisms
// can be in force while changing nothing the engine consults, and a fault that
// silently does nothing is worse than one that refuses.
func cmdFaultPingUnavailable(c *Ctx) (any, error) {
	sel, err := selectorArg(c)
	if err != nil {
		return nil, err
	}
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	names, rerr := a.Resolve(c.Ctx, sel)
	if rerr != nil {
		return nil, Precondition("unresolved_selector", "%v", rerr)
	}
	mech := c.str("mechanism")
	if mech != "binary" && mech != "icmp" {
		return nil, Usage("unknown --mechanism %q (binary or icmp)", mech)
	}
	set, ferr := fault.Open(c.Store.ClusterDir(c.Cluster))
	if ferr != nil {
		return nil, Failed("fault_state", "%v", ferr)
	}
	inj := &fault.Injector{D: a.D, T: t}
	for _, n := range names {
		if err := inj.PingUnavailable(c.Ctx, set, n, mech); err != nil {
			return nil, Failed("ping_unavailable_failed", "%v", err)
		}
		if c.Record != nil {
			_ = c.Record.Append(record.ActorTool, "fault.ping-unavailable",
				map[string]any{"node": n, "mechanism": mech})
		}
		if !c.JSON && !c.Quiet {
			fmt.Fprintf(c.Out, "ping unavailable on %s (%s)\n", n, mech)
		}
	}

	switch t.PingMode {
	case topology.PingNone:
		c.Note("no_ping_hosts", SevWarn,
			"this cluster has no ha_ping_hosts, so nothing consults ping: the condition is in force but the engine never asks")
	case topology.PingTCP:
		if mech == "icmp" {
			c.Note("tcp_ping_hosts", SevWarn,
				"this cluster pings over ha_tcp_ping_hosts, which is a TCP connect and not ICMP: dropping ICMP changes nothing the engine consults. --mechanism binary is the one that bites here")
		}
	}
	if mech == "binary" {
		c.Note("bites_at_start", SevInfo,
			"an engine that cannot run ping with ha_ping_hosts set FAILS TO START; a running engine reads HB_PING_FAILURE on its next check, which is how every master ends up demoting itself on any heartbeat loss")
	}
	if !c.JSON && !c.Quiet {
		printNotes(c)
	}
	return map[string]any{"nodes": names, "mechanism": mech}, nil
}

func cmdFaultPartition(c *Ctx) (any, error) {
	sel, err := selectorArg(c)
	if err != nil {
		return nil, err
	}
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	names, rerr := a.Resolve(c.Ctx, sel)
	if rerr != nil || len(names) != 1 {
		return nil, Precondition("unresolved_selector", "partition needs exactly one node: %v", rerr)
	}
	target := names[0]

	peers := []string{}
	if from := c.str("from"); from != "" {
		p, e := a.Resolve(c.Ctx, from)
		if e != nil {
			return nil, Precondition("unresolved_selector", "%v", e)
		}
		peers = p
	} else {
		for _, n := range t.Nodes {
			if n.Name != target {
				peers = append(peers, n.Name)
			}
		}
	}
	if keep := c.str("keep"); keep != "" {
		var filtered []string
		for _, p := range peers {
			if p != keep {
				filtered = append(filtered, p)
			}
		}
		peers = filtered
		c.Note("keep_applied", SevInfo, "reachability to "+keep+" is preserved, which is what makes a ping host survive the cut")
	}

	set, err := fault.Open(c.Store.ClusterDir(c.Cluster))
	if err != nil {
		return nil, Failed("fault_state", "%v", err)
	}
	inj := &fault.Injector{D: a.D, T: t}
	if err := inj.Partition(c.Ctx, set, target, peers, c.str("mechanism")); err != nil {
		return nil, Failed("partition_failed", "%v", err)
	}
	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "partitioned %s from %s (%s)\n", target, strings.Join(peers, ", "), c.str("mechanism"))
		printNotes(c)
	}
	return map[string]any{"target": target, "cut": peers, "mechanism": c.str("mechanism")}, nil
}

func cmdFaultClear(c *Ctx) (any, error) {
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	target := ""
	if len(c.Args) > 0 && c.Args[0] != "all" {
		names, rerr := a.Resolve(c.Ctx, c.Args[0])
		if rerr != nil || len(names) != 1 {
			return nil, Precondition("unresolved_selector", "%v", rerr)
		}
		target = names[0]
	}
	set, err := fault.Open(c.Store.ClusterDir(c.Cluster))
	if err != nil {
		return nil, Failed("fault_state", "%v", err)
	}
	inj := &fault.Injector{D: a.D, T: t}
	cleared, err := inj.Clear(c.Ctx, set, target)
	if err != nil {
		return nil, Failed("clear_failed", "%v", err)
	}
	// Clearing is not the same as recovering: after a partition that caused a
	// clean failover, nothing happens -- the roles stay swapped, indefinitely.
	// The message names what was actually reversed, because "the network is
	// restored" is not true of a fault that never touched the network.
	kinds := map[string]bool{}
	for _, a := range cleared {
		kinds[a.Kind] = true
	}
	what := "the condition is reversed"
	if kinds["partition"] {
		what = "the network is restored"
	}
	c.Note("clear_is_not_recovery", SevInfo,
		what+"; cluster status afterwards may legitimately show a topology that is healthy and inverted")
	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "cleared %d condition(s)\n", len(cleared))
		printNotes(c)
	}
	return map[string]any{"cleared": cleared}, nil
}

func cmdFaultLs(c *Ctx) (any, error) {
	if err := requireCluster(c); err != nil {
		return nil, err
	}
	set, err := fault.Open(c.Store.ClusterDir(c.Cluster))
	if err != nil {
		return nil, Failed("fault_state", "%v", err)
	}
	if !c.JSON && !c.Quiet {
		if len(set.List) == 0 {
			fmt.Fprintln(c.Out, "no conditions in force")
		}
		for _, a := range set.List {
			fmt.Fprintf(c.Out, "%-12s %-16s %-10s since %s\n", a.Kind, a.Target, a.Mechanism, a.Since)
		}
	}
	return map[string]any{"faults": set.List}, nil
}

// describeWithFaults merges what is in force into the artifact, because a
// describe taken during a partition that omits the partition hands the next
// person a healthy cluster and a bug that does not reproduce.
func describeWithFaults(c *Ctx, doc map[string]any) map[string]any {
	set, err := fault.Open(c.Store.ClusterDir(c.Cluster))
	if err != nil || len(set.List) == 0 {
		return doc
	}
	b, _ := json.Marshal(set.List)
	var raw any
	_ = json.Unmarshal(b, &raw)
	doc["faults"] = raw
	return doc
}

// describeWithLoad does the same for the workload. A cluster reproducing a bug
// under 2000 inserts a second is not the same cluster as an idle one
// (docs/design/06-load.md §7).
func describeWithLoad(c *Ctx, doc map[string]any) map[string]any {
	a, t, err := loadCluster(c)
	if err != nil {
		return doc
	}
	d := &load.Driver{D: a.D, T: t, Workdir: a.Workdir}
	for _, n := range t.Nodes {
		st, serr := d.Status(n.Name)
		if serr != nil || st == nil || !st.Running {
			continue
		}
		spec, _ := d.Spec(n.Name)
		if spec == nil {
			continue
		}
		b, _ := json.Marshal(spec)
		var raw any
		_ = json.Unmarshal(b, &raw)
		doc["load"] = raw
		return doc
	}
	return doc
}

// invalidities are the findings that make a run untrustworthy. They are stated
// rather than inferred: a reader must not have to work out for themselves that
// a measurement was taken while a fault was in force or while the nodes'
// clocks disagreed by more than the interval being measured.
func invalidities(c *Ctx) []string {
	var reasons []string

	set, err := fault.Open(c.Store.ClusterDir(c.Cluster))
	if err == nil && len(set.List) > 0 {
		reasons = append(reasons, "fault_active")
	}

	// The measurements this record exists for are single-digit seconds. Two
	// containers on one host share a clock; the moment a topology spans hosts
	// they do not, and a three-second skew silently becomes a three-second
	// finding (docs/design/07-record.md §4).
	if skew, ok := clockSkew(c); ok && skew > time.Second {
		reasons = append(reasons, "clock_skew")
		c.Note("clock_skew", SevWarn,
			fmt.Sprintf("the nodes' clocks differ by %s, which is not small next to the intervals this records", skew.Round(time.Millisecond)))
	}
	return reasons
}

func clockSkew(c *Ctx) (time.Duration, bool) {
	a, t, err := loadCluster(c)
	if err != nil {
		return 0, false
	}
	var min, max float64
	seen := 0
	for _, n := range t.Nodes {
		res, err := a.D.Exec(c.Ctx, n.Name, t.DB, "date +%s.%N")
		if err != nil || res.ExitCode != 0 {
			continue
		}
		var v float64
		if _, err := fmt.Sscan(strings.TrimSpace(res.Stdout), &v); err != nil {
			continue
		}
		if seen == 0 || v < min {
			min = v
		}
		if seen == 0 || v > max {
			max = v
		}
		seen++
	}
	if seen < 2 {
		return 0, false
	}
	return time.Duration((max - min) * float64(time.Second)), true
}

func lagFlags(fs *flag.FlagSet) {
	fs.String("stage", "apply", "copy or apply — the pipeline is two processes that stall independently")
	fs.String("mechanism", "suspend", "suspend (stage-targeted) or delay (realistic, but cannot say which stage)")
	fs.String("delay", "200ms", "for --mechanism delay")
}

func cmdFaultLag(c *Ctx) (any, error) {
	sel, err := selectorArg(c)
	if err != nil {
		return nil, err
	}
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	names, rerr := a.Resolve(c.Ctx, sel)
	if rerr != nil || len(names) != 1 {
		return nil, Precondition("unresolved_selector", "lag needs exactly one node: %v", rerr)
	}
	stage := c.str("stage")
	if stage != "copy" && stage != "apply" {
		return nil, Usage("--stage must be copy or apply; the engine reports the two separately and they stall independently")
	}
	set, err := fault.Open(c.Store.ClusterDir(c.Cluster))
	if err != nil {
		return nil, Failed("fault_state", "%v", err)
	}
	inj := &fault.Injector{D: a.D, T: t}
	if err := inj.Lag(c.Ctx, set, names[0], stage, c.str("mechanism"), c.str("delay")); err != nil {
		return nil, Precondition("lag_failed", "%v", err)
	}
	if c.str("mechanism") == "delay" {
		c.Note("stage_not_separable", SevWarn,
			"a netem delay slows both stages and cannot say which; --mechanism suspend is what separates them")
	}
	// Measured: the heartbeat watches process existence, not progress, so a
	// suspended stage does not provoke a failover. Worth saying, because it is
	// the reason this mechanism is usable at all.
	c.Note("heartbeat_unaffected", SevInfo,
		"the heartbeat monitors process existence rather than progress, so a suspended stage does not induce a failover")
	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "lag on %s: stage=%s mechanism=%s\n", names[0], stage, c.str("mechanism"))
		printNotes(c)
	}
	return map[string]any{"node": names[0], "stage": stage, "mechanism": c.str("mechanism")}, nil
}

func splitbrainFlags(fs *flag.FlagSet) {
	fs.String("flavour", "", "ping-survives, no-ping-hosts or calc-score-window; default: whichever this cluster's configuration produces")
	fs.Duration("wait", 30*time.Second, "how long to wait for the engine to reach two masters")
}

// cmdFaultSplitbrain is a composed verb: the mechanism is a partition, and the
// flavour follows from whether a ping host survives it. It exists separately
// because the intent is what a scenario means, and because getting the --keep
// right is precisely the knowledge the tool is supposed to hold.
func cmdFaultSplitbrain(c *Ctx) (any, error) {
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	// The effective configuration, not the user's overrides. --ping-mode writes
	// the parameter by construction, so reading Parameters.HA alone reported
	// "no-ping-hosts" for a cluster whose engine was logging "Ping check
	// succeeded" -- naming the wrong flavour, which is the one thing this verb
	// exists to get right (docs/design/02-topology.md §5).
	pingSet := t.Parameters.HA["ha_ping_hosts"] != "" || t.Parameters.HA["ha_tcp_ping_hosts"] != "" ||
		(t.PingMode != topology.PingNone && t.PingHost != "")
	want := c.str("flavour")
	have := "no-ping-hosts"
	if pingSet {
		have = "ping-survives"
	}
	// The third flavour is not a partition condition at all: it is the SAME
	// partition observed through a raised ha_calc_score_interval_in_msecs, which
	// is a create-time parameter. So it is reported when the cluster carries one
	// and refused when it does not, rather than faked.
	score := t.Parameters.HA["ha_calc_score_interval_in_msecs"]
	if score == "" {
		score = t.Parameters.Hidden["ha_calc_score_interval_in_msecs"]
	}
	if score != "" && score != "3000" {
		have = "calc-score-window"
	}
	if want != "" && want != have {
		return nil, Precondition("flavour_unavailable",
			"this cluster produces the %q flavour: ha_ping_hosts is %s and ha_calc_score_interval_in_msecs is %s. The flavour follows from the configuration, so build the cluster for the one you want rather than asking for it here",
			have, map[bool]string{true: "set", false: "unset"}[pingSet],
			map[bool]string{true: score, false: "default"}[score != ""])
	}

	master, rerr := a.Resolve(c.Ctx, "master")
	if rerr != nil || len(master) != 1 {
		return nil, Precondition("unresolved_selector", "%v", rerr)
	}
	var peers []string
	for _, n := range t.Nodes {
		if n.Name != master[0] {
			peers = append(peers, n.Name)
		}
	}
	set, err := fault.Open(c.Store.ClusterDir(c.Cluster))
	if err != nil {
		return nil, Failed("fault_state", "%v", err)
	}
	inj := &fault.Injector{D: a.D, T: t}
	if err := inj.Partition(c.Ctx, set, master[0], peers, "blackhole"); err != nil {
		return nil, Failed("partition_failed", "%v", err)
	}

	deadline := time.Now().Add(c.dur("wait"))
	var st *inspect.Status
	masters := 0
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		st, _ = inspect.Read(c.Ctx, a.D, t)
		masters = 0
		for _, n := range st.Nodes {
			if n.Server == "registered_and_active" {
				masters++
			}
		}
		if masters > 1 {
			break
		}
	}

	// The assertion belongs on the engine's own cancel reason, not on the
	// outcome: both flavours give two masters, and only this line tells them
	// apart. A test that asserts "two masters" passes for the wrong reason half
	// the time.
	reason, _ := inj.CancelReason(c.Ctx, master[0])
	out := map[string]any{"flavour": have, "masters": masters, "cancel_reason": reason, "partitioned": master[0]}
	if have == "calc-score-window" {
		// The claim this flavour carries is about what happens AFTER the heal --
		// an Active-Active window with replication running both ways -- and this
		// verb only induces the split. Saying so is the difference between a
		// reproduction and a name.
		c.Note("window_is_after_the_heal", SevInfo,
			"this flavour's reported anomaly is the window after `fault clear`, not the split itself: measure it with `repl check` in both directions while the interval elapses (docs/design/04-faults.md §5)")
	}
	if masters < 2 {
		c.Note("no_split_brain", SevWarn,
			fmt.Sprintf("the cluster did not reach two masters within %s; it is partitioned and the condition is in force", c.dur("wait")))
	}
	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "split brain (%s): %d master(s)\n", have, masters)
		if reason != "" {
			fmt.Fprintf(c.Out, "  the engine says: %s\n", reason)
		}
		printNotes(c)
	}
	return out, nil
}

func failcountFlags(fs *flag.FlagSet) {
	fs.String("table", "", "table to break (default csb_failcount)")
	fs.Int("rows", 5, "how many rows arrive before the primary key does")
}

func cmdFaultFailcount(c *Ctx) (any, error) {
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	master, rerr := a.Resolve(c.Ctx, "master")
	if rerr != nil || len(master) != 1 {
		return nil, Precondition("unresolved_selector", "%v", rerr)
	}
	rows, _ := strconv.Atoi(c.str("rows"))
	if rows < 1 {
		rows = 5
	}
	inj := &fault.Injector{D: a.D, T: t}
	if err := inj.FailCount(c.Ctx, master[0], c.str("table"), rows); err != nil {
		return nil, Failed("failcount_failed", "%v", err)
	}
	// Its damage is data, so clear cannot reverse it: the reversal is a repair.
	c.Note("not_clearable", SevWarn,
		"fault clear cannot reverse this — the rows are gone on one node and present on the other. The reversal is ha resync")
	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "fail count induced on %s (%d rows arrived before the primary key)\n", master[0], rows)
		printNotes(c)
	}
	return map[string]any{"master": master[0], "rows": rows}, nil
}

func quiesceFlags(fs *flag.FlagSet) {
	fs.String("mode", "ro", "ro (reads allowed) or so (slave only)")
	fs.String("mechanism", "broker", "broker (the field's mechanism) or load (this tool's own driver)")
}

func cmdClusterQuiesce(c *Ctx) (any, error) {
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	mech := c.str("mechanism")
	set, ferr := fault.Open(c.Store.ClusterDir(c.Cluster))
	if ferr != nil {
		return nil, Failed("fault_state", "%v", ferr)
	}
	inj := &fault.Injector{D: a.D, T: t}

	switch mech {
	case "", "broker":
		if !t.WithBroker {
			// Refuse rather than report success it cannot deliver: a cluster
			// with no broker has no door to close.
			return nil, Precondition("no_broker",
				"this cluster has no broker, so there is no door to close. Create it with --with-broker, or use --mechanism load to stop this tool's own writer")
		}
		if err := inj.Quiesce(c.Ctx, set, t.NodeNames(), c.str("mode"), assembly.BrokerName); err != nil {
			return nil, Failed("quiesce_failed", "%v", err)
		}
	case "load":
		d := &load.Driver{D: a.D, T: t, Workdir: a.Workdir}
		for _, n := range t.Nodes {
			_ = d.Stop(c.Ctx, n.Name)
		}
	default:
		return nil, Usage("unknown --mechanism %q (want broker or load)", mech)
	}

	// Neither mechanism closes a door the tool does not own.
	c.Note("writers_not_all_stopped", SevWarn,
		"a session opened directly against a node still writes; quiesce closes the broker and this tool's driver, and says which")
	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "quiesced %s (mechanism=%s mode=%s)\n", c.Cluster, orUnknown(mech), c.str("mode"))
		printNotes(c)
	}
	return map[string]any{"mechanism": mech, "mode": c.str("mode")}, nil
}

func cmdClusterResume(c *Ctx) (any, error) {
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	set, ferr := fault.Open(c.Store.ClusterDir(c.Cluster))
	if ferr != nil {
		return nil, Failed("fault_state", "%v", ferr)
	}
	inj := &fault.Injector{D: a.D, T: t}
	cleared, rerr := inj.Resume(c.Ctx, set, assembly.BrokerName)
	if rerr != nil {
		return nil, Failed("resume_failed", "%v", rerr)
	}
	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "resumed %s (%d condition(s) lifted)\n", c.Cluster, len(cleared))
	}
	return map[string]any{"resumed": cleared}, nil
}

func resyncFlags(fs *flag.FlagSet) {
	fs.String("path", "", "resume, table or slave; default: chosen from what is observed")
	fs.Bool("dry-run", false, "say what would be done and why, and do nothing")
}

// cmdHaResync is the reversal fault clear cannot perform, because a fail count's
// damage is data. Three paths, which are the three the field chooses between,
// and the tool reports how it decided rather than deciding silently.
func cmdHaResync(c *Ctx) (any, error) {
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	st, serr := inspect.Read(c.Ctx, a.D, t)
	if serr != nil {
		return nil, Failed("inspect_failed", "%v", serr)
	}
	var master, slave string
	for _, n := range st.Nodes {
		switch n.Server {
		case "registered_and_active":
			master = n.Name
		case "registered_and_standby":
			slave = n.Name
		}
	}
	// A named node wins over the observed standby, and it does not have to BE a
	// standby. The node that most needs rebuilding is the one that cannot rejoin
	// at all -- unregistered, no role, nothing for the pair check to find -- and
	// refusing it there sent the operator back to `cluster destroy`.
	if len(c.Args) > 0 {
		names, rerr := a.Resolve(c.Ctx, c.Args[0])
		if rerr != nil || len(names) != 1 {
			return nil, Precondition("unresolved_selector", "%v", rerr)
		}
		slave = names[0]
		if slave == master {
			return nil, Precondition("that_is_the_master",
				"%s is the master; resync repairs the other node", slave)
		}
	}
	if master == "" {
		return nil, Precondition("no_master", "resync rebuilds from a master and this group has none")
	}
	if slave == "" {
		return nil, Precondition("no_standby",
			"no node is registered as a standby; name the one to repair (csb ha resync <node>)")
	}

	fail := 0
	for _, n := range st.Nodes {
		if n.Name == slave && n.Repl != nil && n.Repl.Fail != nil {
			fail = *n.Repl.Fail
		}
	}
	inj := &fault.Injector{D: a.D, T: t}
	tables, _ := inj.FailRows(c.Ctx, slave)

	// A zero fail counter is not evidence that the two sides agree, and this
	// command used to treat it as if it were: it answered "resume -- replication
	// is behind at worst, not broken" on a cluster whose standby was permanently
	// missing a row. Both halves of that sentence were true and the conclusion
	// was wrong, because a split brain fails nothing -- each side wrote its own
	// log and both succeeded -- so the counter never moves and the applier's log
	// names no table. The comparison has to come from the catalog instead, and it
	// has to run before "nothing is wrong" is said out loud
	// (docs/findings/active-active-window.md).
	path := c.str("path")
	var diverged []string
	if fail == 0 && path == "" {
		if d, derr := inspect.CompareTables(c.Ctx, a.D, t, master, slave, nil); derr == nil {
			diverged = d.Differ
		} else {
			c.Note("not_compared", SevWarn,
				"the two databases could not be compared, so 'nothing is wrong' rests on fail_counter alone: "+derr.Error())
		}
	}

	// How the field chooses: nothing wrong, repair the affected table, or rebuild
	// the slave. The changeover sits somewhere around a thousand rows -- which is
	// their number, not one this project measured, and it is named as theirs.
	why := ""
	switch {
	case path != "":
		why = "you asked for it"
	case fail == 0 && len(diverged) > 0:
		path = "slave"
		why = fmt.Sprintf("fail_counter is 0 and nothing failed to apply, but %d table(s) hold different row counts (%s): that is what a healed split brain leaves, and replication will not carry it",
			len(diverged), strings.Join(diverged, ", "))
	case fail == 0:
		path, why = "resume", "fail_counter is 0 and every user table has the same row count on both nodes"
	case fail >= 1000:
		path, why = "slave", fmt.Sprintf("fail_counter is %d, past the point the field stops repairing tables and rebuilds", fail)
	case len(tables) > 0:
		path, why = "table", fmt.Sprintf("fail_counter is %d and the applier's log names %d table(s)", fail, len(tables))
	default:
		path, why = "resume", fmt.Sprintf("fail_counter is %d but the applier's log names no table, so there is nothing to repair from", fail)
	}

	names := make([]string, 0, len(tables))
	for k := range tables {
		names = append(names, k)
	}
	sort.Strings(names)

	out := map[string]any{"master": master, "slave": slave, "fail_counter": fail,
		"tables": names, "path": path, "decided_because": why, "diverged": diverged}

	if c.fs.Lookup("dry-run").Value.String() == "true" {
		if !c.JSON && !c.Quiet {
			fmt.Fprintf(c.Out, "would take path %q — %s\n", path, why)
			for _, n := range names {
				fmt.Fprintf(c.Out, "  affected: %s (%d failure(s) logged)\n", n, tables[n])
			}
		}
		return out, nil
	}

	switch path {
	case "resume":
		// Nothing to do, and saying so is the answer.
	case "table":
		if len(names) == 0 {
			// Asked for explicitly on a cluster where nothing failed: compare
			// everything, because "no table failed" is why we are here.
			if ut, uerr := inspect.UserTables(c.Ctx, a.D, t, master); uerr == nil {
				names = ut
			}
		}
		diffs := map[string][2]int{}
		for _, n := range names {
			m, sl, cerr := inj.CompareTable(c.Ctx, master, slave, n)
			if cerr != nil {
				c.Note("table_not_comparable", SevWarn, n+": "+cerr.Error())
				continue
			}
			if m != sl {
				diffs[n] = [2]int{m, sl}
			}
			if !c.JSON && !c.Quiet {
				verdict := "same row count"
				if m != sl {
					verdict = "DIFFERENT"
				}
				fmt.Fprintf(c.Out, "  %-28s master=%-8d standby=%-8d %s\n", n, m, sl, verdict)
			}
		}
		out["differs"] = diffs
		if len(diffs) == 0 {
			// The common case in the field, and the one nobody could confirm
			// cheaply: a counter with no divergence under it.
			c.Note("no_data_difference", SevInfo,
				"every affected table has the same row count on both nodes; the fail count is a scar rather than a divergence")
		} else {
			// Repairing a real divergence is the field's table rebuild, and this
			// tool does not do it yet. Saying so beats doing nothing under a name
			// that says otherwise.
			return out, Failed("not_implemented",
				"%d table(s) differ and rebuilding one is not built here yet; the field's path is a table rebuild from the master, or ha_make_slavedb.sh for the whole slave", len(diffs))
		}
	case "slave":
		// Nothing in the engine closes this gap on its own: the standby's
		// recorded position has already moved past the missing write -- a canary
		// written afterwards arrives -- so nothing will ever re-fetch it. Only a
		// rebuild resets that bookkeeping, and this is the engine's own rebuild.
		if err := a.RebuildSlave(c.Ctx, master, slave); err != nil {
			return out, Failed("rebuild_failed", "%v", err)
		}
		deadline := time.Now().Add(c.Timeout / 2)
		back := false
		for {
			st2, rerr := inspect.Read(c.Ctx, a.D, t)
			if rerr == nil {
				for _, n := range st2.Nodes {
					if n.Name == slave && n.Server == "registered_and_standby" {
						back = true
					}
				}
			}
			if back || time.Now().After(deadline) || c.Ctx.Err() != nil {
				break
			}
			time.Sleep(2 * time.Second)
		}
		if !back {
			return out, &Error{Code: ExitTimeout, Note: "rebuilt_but_not_standby",
				Msg: slave + " was rebuilt but has not reached registered_and_standby"}
		}
		// A rebuild that leaves the two sides still disagreeing has not repaired
		// anything, and the only way to know is to compare again.
		if d, derr := inspect.CompareTables(c.Ctx, a.D, t, master, slave, nil); derr == nil {
			out["diverged_after"] = d.Differ
			if len(d.Differ) > 0 {
				return out, Failed("still_diverged",
					"%s was rebuilt and %d table(s) still differ: %s", slave, len(d.Differ), strings.Join(d.Differ, ", "))
			}
			c.Note("rebuilt", SevInfo, slave+" was rebuilt from an online backup of "+master+" and every user table now matches")
		} else {
			c.Note("not_compared_after", SevWarn, "the rebuild finished but the two sides could not be compared: "+derr.Error())
		}
	default:
		return nil, Usage("unknown --path %q (want resume, table or slave)", path)
	}

	// The counter is not zeroed to make this output tidy. The engine leaves it
	// standing on purpose: a zero would let an operator conclude master and slave
	// agree when they do not.
	c.Note("fail_counter_left_standing", SevInfo,
		"fail_counter is not reset by this command; re-read it after replication has moved")
	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "resync path %q — %s\n", path, why)
		printNotes(c)
	}
	return out, nil
}

func checkFlags(fs *flag.FlagSet) {
	fs.String("table", "", "marker table (default csb_canary)")
	fs.Duration("wait", 30*time.Second, "how long the row has to arrive in")
}

// cmdReplCheck is the canary. The field verifies a rebuilt slave this way rather
// than by reading a threshold off a gauge, and the reason is in the measurement
// this tool can already produce: a suspended applier freezes db_ha_apply_info at
// a healthy-looking number for as long as the stall lasts. A row does not freeze.
func cmdReplCheck(c *Ctx) (any, error) {
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	master, merr := a.Resolve(c.Ctx, "master")
	if merr != nil || len(master) != 1 {
		return nil, Precondition("unresolved_selector", "the canary needs one active node: %v", merr)
	}
	standby := ""
	if len(c.Args) > 0 {
		names, rerr := a.Resolve(c.Ctx, c.Args[0])
		if rerr != nil || len(names) != 1 {
			return nil, Precondition("unresolved_selector", "%v", rerr)
		}
		standby = names[0]
	} else {
		names, rerr := a.Resolve(c.Ctx, "slave")
		if rerr != nil || len(names) != 1 {
			return nil, Precondition("unresolved_selector", "%v", rerr)
		}
		standby = names[0]
	}

	can, cerr := inspect.Check(c.Ctx, a.D, t, master[0], standby, c.str("table"), c.dur("wait"))
	if cerr != nil {
		return can, Failed("canary_failed", "%v", cerr)
	}
	if c.Record != nil {
		_ = c.Record.Append(record.ActorTool, "repl.check", map[string]any{
			"from": can.From, "to": can.To, "arrived": can.Arrived, "seconds": can.Seconds})
	}
	if !c.JSON && !c.Quiet {
		if can.Arrived {
			fmt.Fprintf(c.Out, "arrived on %s in %.2fs (marker %s)\n", can.To, can.Seconds, can.Marker)
		} else {
			fmt.Fprintf(c.Out, "NOT arrived on %s after %.0fs (marker %s written on %s)\n",
				can.To, can.Waited, can.Marker, can.From)
		}
	}
	if !can.Arrived {
		// Exit 4 rather than 1: the write succeeded and the wait ran out, which
		// is a different thing from the tool being unable to do its job.
		return can, &Error{Code: ExitTimeout, Note: "canary_did_not_arrive",
			Msg: fmt.Sprintf("the marker did not reach %s within %s; replication is not carrying writes, whatever db_ha_apply_info says",
				can.To, c.dur("wait"))}
	}
	return can, nil
}
