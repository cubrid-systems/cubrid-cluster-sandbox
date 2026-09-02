package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/fault"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/inspect"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/record"
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
			fmt.Fprintf(c.Out, "%-16s applied=%s  apply_lag=%s pages  fail=%s  copy=not reported  (%s, read %s)\n",
				n.Name, dash(n.Repl.Applied), dash(n.Repl.ApplyLag), dash(n.Repl.Fail),
				n.Repl.Source, n.Repl.SampledAt)
		}
	}
	if !c.JSON && !c.Quiet {
		printNotes(c)
	}
	return map[string]any{"nodes": out}, nil
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
func cmdNodeStart(c *Ctx) (any, error) {
	return nodeAction(c, func(i *fault.Injector, n string) error { return i.Start(c.Ctx, n) })
}

func partitionFlags(fs *flag.FlagSet) {
	fs.String("from", "", "narrow the cut to this peer")
	fs.String("keep", "", "preserve reachability to this host")
	fs.String("mechanism", "blackhole", "blackhole (no route) or drop (route intact, packets discarded)")
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
	c.Note("clear_is_not_recovery", SevInfo,
		"the network is restored; cluster status afterwards may legitimately show a topology that is healthy and inverted")
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
