package cli

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/assembly"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/fault"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/inspect"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/record"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/topology"
)

func promoteFlags(fs *flag.FlagSet) {
	fs.Duration("wait", 120*time.Second, "bound on the promotion")
}

// masters reports which nodes are serving as one. Two is not a promotion
// problem, it is a split brain, and the difference matters to every verb below.
func masters(st *inspect.Status) []string {
	var out []string
	for _, n := range st.Nodes {
		if n.Server == "registered_and_active" {
			out = append(out, n.Name)
		}
	}
	return out
}

func nodeByName(st *inspect.Status, name string) *inspect.Node {
	for i := range st.Nodes {
		if st.Nodes[i].Name == name {
			return &st.Nodes[i]
		}
	}
	return nil
}

// takeMasterAway is the mechanism under both promote and failback.
//
// `promote` is not the inverse of anything. A demotion cannot be driven from
// outside -- changemode refuses an active->standby transition the heartbeat did
// not drive (server_support.c:1558) -- so what looks like a demotion is really
// the heartbeat replacing the server process. Every verb here therefore works by
// making the heartbeat decide, and says so (docs/design/04-faults.md §2).
//
// The bound and the judgement are both from the phase-0 run: `cubrid heartbeat
// stop` polls COMMDB_HA_DEACT_CONFIRM_NO_SERVER on a one-second sleep and a
// zombie cub_server answers "still running" forever, so the container needs an
// init process AND anything driving this step must be bounded and decide on the
// observed roles rather than on the command's exit status
// (docs/findings/failback.md).
func takeMasterAway(c *Ctx, a *assembly.Assembler, t *topology.Topology, current, target string, wait time.Duration) (float64, bool, error) {
	start := time.Now()
	returned := make(chan struct{}, 1)
	go func() {
		_, _ = a.D.Exec(c.Ctx, current, t.DB, "cubrid heartbeat stop")
		returned <- struct{}{}
	}()

	deadline := time.Now().Add(wait)
	for {
		st, err := inspect.Read(c.Ctx, a.D, t)
		if err == nil {
			if n := nodeByName(st, target); n != nil && n.Server == "registered_and_active" {
				select {
				case <-returned:
					return time.Since(start).Seconds(), true, nil
				default:
					return time.Since(start).Seconds(), false, nil
				}
			}
		}
		if time.Now().After(deadline) || c.Ctx.Err() != nil {
			return time.Since(start).Seconds(), false,
				&Error{Code: ExitTimeout, Note: "not_promoted",
					Msg: fmt.Sprintf("%s did not reach registered_and_active within %s of taking %s away", target, wait, current)}
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func cmdHaPromote(c *Ctx) (any, error) {
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
	if len(names) != 1 {
		return nil, Precondition("ambiguous_selector", "ha promote needs exactly one node; %q resolved to %d", sel, len(names))
	}
	target := names[0]

	st, err := inspect.Read(c.Ctx, a.D, t)
	if err != nil {
		return nil, Failed("inspect_failed", "%v", err)
	}
	ms := masters(st)
	switch {
	case len(ms) > 1:
		return nil, Precondition("two_masters",
			"%s are both active: that is a split brain, not a group waiting for a promotion", strings.Join(ms, " and "))
	case len(ms) == 1 && ms[0] == target:
		c.Note("already_active", SevInfo, target+" is already the master; nothing to do")
		return map[string]any{"promoted": target, "seconds": 0.0, "changed": false}, nil
	case len(ms) == 0:
		return nil, Precondition("no_master",
			"the group has no master, so there is none to take away; promote works by making the heartbeat decide, not by forcing a role")
	}
	current := ms[0]

	secs, returned, err := takeMasterAway(c, a, t, current, target, c.dur("wait"))
	if c.Record != nil {
		_ = c.Record.Append(record.ActorTool, "ha.promote",
			map[string]any{"target": target, "demoted": current, "seconds": secs})
	}
	if err != nil {
		return nil, err
	}
	if !returned {
		c.Note("stop_not_returned", SevWarn,
			"the promotion is observed in the roles, but `cubrid heartbeat stop` on "+current+" has not returned yet; the roles are the evidence, not that command's exit status")
	}
	c.Note("not_a_demotion", SevInfo,
		"nothing demoted "+current+": its heartbeat was stopped and the heartbeat promoted "+target+". "+current+" is out of the group until `node start` puts it back")
	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "%s promoted in %.1fs (%s taken away)\n", target, secs, current)
		printNotes(c)
	}
	return map[string]any{"promoted": target, "demoted": current, "seconds": secs, "changed": true}, nil
}

// ---- failback ------------------------------------------------------------

func failbackFlags(fs *flag.FlagSet) {
	fs.String("to", "", "the node service returns to (default: the one created as master)")
	fs.Bool("yes", false, "do not stop at the decision points")
	fs.Bool("dry-run", false, "print the plan and the evidence; change nothing")
	fs.Bool("quiesce", false, "close the broker door while the roles move, if there is a broker")
	fs.Duration("wait", 180*time.Second, "bound on each engine step")
}

type fbStep struct {
	N        int    `json:"step"`
	What     string `json:"what"`
	Evidence string `json:"evidence,omitempty"`
	Done     bool   `json:"done"`
	Seconds  float64
}

// cmdHaFailback returns service to the original master.
//
// It keeps the engine's name and not the operation's, because every [Failback]
// line in a CUBRID log -- and the technical team's own study notes -- mean a
// master stepping DOWN. This verb is the return trip, which the tracker calls
// "failback 작업" and which the engine has no word for at all
// (docs/design/04-faults.md §7).
//
// Interactive by default, and that is a statement about what is settled. The
// mechanism is: one measured run restored the original master in 2 s with no row
// loss. The policy is not: the threshold for "caught up", whether writes are
// quiesced, and WHO AUTHORISES IT are open (DESIGN.md §9 OQ8). A tool that
// picked those would be inventing a requirement, so it stops and asks.
func cmdHaFailback(c *Ctx) (any, error) {
	dry := c.str("dry-run") == "true"
	yes := c.str("yes") == "true"
	if c.JSON && !dry && !yes {
		return nil, Usage("ha failback stops at decision points; with --json pass --yes (already decided) or --dry-run (decide later)")
	}
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}

	to := c.str("to")
	if to == "" {
		for _, n := range t.Nodes {
			if n.Role == "master" {
				to = n.Name
			}
		}
		if to == "" {
			return nil, Usage("this topology has no node created as master; name one with --to")
		}
	} else {
		names, rerr := a.Resolve(c.Ctx, to)
		if rerr != nil || len(names) != 1 {
			return nil, Precondition("unresolved_selector", "--to needs exactly one node: %v", rerr)
		}
		to = names[0]
	}

	st, err := inspect.Read(c.Ctx, a.D, t)
	if err != nil {
		return nil, Failed("inspect_failed", "%v", err)
	}
	ms := masters(st)
	if len(ms) > 1 {
		return nil, Precondition("two_masters",
			"%s are both active: that is a split brain, not a completed failover. `ha resync` and `fault clear` come first", strings.Join(ms, " and "))
	}
	if len(ms) == 0 {
		return nil, Precondition("no_master", "the group has no master; there is no service to return")
	}
	from := ms[0]
	if from == to {
		c.Note("already_home", SevInfo, to+" is already the master; the service never left")
		return map[string]any{"to": to, "changed": false, "steps": []fbStep{}}, nil
	}

	// STEP 2's evidence, and the reason it is not one query.
	//
	// db_ha_apply_info is empty on a node that has just changed role -- the
	// measured run printed <none> at exactly the moment the operator needed it,
	// and the row appeared minutes later reading caught up. A check that treats
	// "no row" as "no lag" approves a failback onto a node it knows nothing
	// about (docs/findings/failback.md). So when the view is silent the canary
	// answers instead: a write that has to arrive.
	ev := "unknown"
	caught := false
	if n := nodeByName(st, to); n != nil && n.Repl != nil && n.Repl.ApplyLag != nil {
		caught = *n.Repl.ApplyLag == 0 && (n.Repl.Fail == nil || *n.Repl.Fail == 0)
		ev = fmt.Sprintf("db_ha_apply_info on %s: apply_lag=%s fail=%s", to, dash(n.Repl.ApplyLag), dash(n.Repl.Fail))
	} else {
		can, cerr := inspect.Check(c.Ctx, a.D, t, from, to, "", 15*time.Second)
		switch {
		case cerr != nil:
			ev = "db_ha_apply_info has no row for " + to + " and the canary could not run: " + cerr.Error()
		case can.Arrived:
			caught = true
			ev = fmt.Sprintf("db_ha_apply_info has no row for %s (it is empty across a role change); a canary written on %s arrived in %.2fs", to, from, can.Seconds)
		default:
			ev = "db_ha_apply_info has no row for " + to + " and a canary written on " + from + " did not arrive"
		}
	}

	steps := []fbStep{
		{N: 1, What: "confirm the current master", Evidence: from + " is active, " + to + " is not", Done: true},
		{N: 2, What: "is " + to + " caught up", Evidence: ev, Done: caught},
		{N: 3, What: "quiesce writes", Evidence: quiesceEvidence(c, t)},
		{N: 4, What: "stop the heartbeat on " + from},
		{N: 5, What: "wait for " + to + " to be promoted"},
		{N: 6, What: "rejoin " + from + " as slave (service stop, service start, heartbeat start)"},
		{N: 7, What: "verify: roles restored and a write still replicates"},
	}

	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "failback %s -> %s\n", from, to)
		for _, s := range steps {
			mark := " "
			if s.Done {
				mark = "*"
			}
			fmt.Fprintf(c.Out, " %s %d. %s\n", mark, s.N, s.What)
			if s.Evidence != "" {
				fmt.Fprintf(c.Out, "      %s\n", s.Evidence)
			}
		}
	}
	if !caught {
		c.Note("not_caught_up", SevWarn,
			"step 2 is not satisfied: "+ev+". How much outstanding work is acceptable is not this tool's number to pick (DESIGN.md §9 OQ8)")
	}
	if dry {
		c.Note("dry_run", SevInfo, "nothing was changed")
		if !c.JSON && !c.Quiet {
			printNotes(c)
		}
		return map[string]any{"from": from, "to": to, "changed": false, "steps": steps, "caught_up": caught}, nil
	}

	// The decision this tool does not make.
	if !yes {
		if !confirm(c, fmt.Sprintf(
			"\nMove service from %s to %s?\n"+
				"  This tool has no policy for who authorises a failback or on what evidence;\n"+
				"  that is the open question in harness/failback.sh. Type yes to proceed: ", from, to)) {
			c.Note("declined", SevInfo, "nothing was changed")
			return map[string]any{"from": from, "to": to, "changed": false, "steps": steps}, nil
		}
	}

	set, ferr := fault.Open(c.Store.ClusterDir(c.Cluster))
	if ferr != nil {
		return nil, Failed("fault_state", "%v", ferr)
	}
	inj := &fault.Injector{D: a.D, T: t}
	quiesced := false
	if c.str("quiesce") == "true" && t.WithBroker {
		var all []string
		for _, n := range t.Nodes {
			all = append(all, n.Name)
		}
		if err := inj.Quiesce(c.Ctx, set, all, "RO", assembly.BrokerName); err != nil {
			return nil, Failed("quiesce_failed", "%v", err)
		}
		quiesced = true
		steps[2].Done = true
	}

	secs, returned, err := takeMasterAway(c, a, t, from, to, c.dur("wait"))
	steps[3].Done = err == nil
	steps[4].Done = err == nil
	steps[4].Seconds = secs
	if c.Record != nil {
		_ = c.Record.Append(record.ActorTool, "ha.failback",
			map[string]any{"from": from, "to": to, "seconds": secs, "caught_up": caught})
	}
	if err != nil {
		return nil, err
	}
	if !returned {
		c.Note("stop_not_returned", SevWarn,
			"the roles moved, but `cubrid heartbeat stop` on "+from+" has not returned; the roles are the evidence")
	}

	// STEP 6. The plain `heartbeat start` trap is real: after a stop, a bare
	// start fails with "CUBRID heartbeat feature is being deactivated", and the
	// full service cycle is what gets past it.
	if err := inj.Start(c.Ctx, from); err != nil {
		return nil, Failed("rejoin_failed", "%s did not rejoin: %v", from, err)
	}
	steps[5].Done = true

	if quiesced {
		if _, err := inj.Resume(c.Ctx, set, assembly.BrokerName); err != nil {
			c.Note("resume_failed", SevWarn, "the door did not reopen: "+err.Error())
		}
	}

	// STEP 7. Roles, and then a write that has to arrive -- because roles alone
	// say the group agrees, not that replication carries anything.
	verified := false
	deadline := time.Now().Add(c.dur("wait"))
	for {
		st2, err := inspect.Read(c.Ctx, a.D, t)
		if err == nil {
			if n := nodeByName(st2, from); n != nil && strings.HasPrefix(n.Server, "registered_and_standby") {
				verified = true
			}
		}
		if verified || time.Now().After(deadline) || c.Ctx.Err() != nil {
			break
		}
		time.Sleep(time.Second)
	}
	out := map[string]any{"from": from, "to": to, "seconds": secs, "changed": true, "caught_up": caught}
	if verified {
		can, cerr := inspect.Check(c.Ctx, a.D, t, to, from, "", 30*time.Second)
		steps[6].Done = cerr == nil && can.Arrived
		if steps[6].Done {
			steps[6].Evidence = fmt.Sprintf("%s is standby again and a write on %s arrived in %.2fs", from, to, can.Seconds)
			out["canary_seconds"] = can.Seconds
		} else {
			steps[6].Evidence = from + " is standby again, but a write on " + to + " did not arrive"
			c.Note("replication_not_verified", SevError, steps[6].Evidence)
		}
	} else {
		steps[6].Evidence = from + " did not come back as standby within " + c.dur("wait").String()
		c.Note("rejoin_not_verified", SevError, steps[6].Evidence)
	}
	out["steps"] = steps

	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "\nservice returned to %s in %.1fs; %s\n", to, secs, steps[6].Evidence)
		printNotes(c)
	}
	return out, nil
}

func quiesceEvidence(c *Ctx, t *topology.Topology) string {
	if !t.WithBroker {
		return "this cluster has no broker, so there is no door to close (create --with-broker)"
	}
	if c.str("quiesce") != "true" {
		return "a broker is running; --quiesce closes it for the duration"
	}
	return "the broker's ACCESS_MODE goes to RO for the duration"
}

// confirm asks on the terminal. It is deliberately not a --force flag with a
// default: a decision nobody has written down should cost a keystroke.
func confirm(c *Ctx, prompt string) bool {
	fmt.Fprint(c.Out, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(line), "yes")
}
