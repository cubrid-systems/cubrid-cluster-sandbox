package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/load"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/record"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/topology"
)

func loadStartFlags(fs *flag.FlagSet) {
	fs.String("profile", "insert", "insert, update, mixed, host-cpu or host-io")
	fs.String("rate", "", "statements per second, e.g. 2000/s; omit or 'max' to saturate")
	fs.Int("concurrency", 4, "workers")
	fs.Duration("for", 0, "run for this long, then stop")
	fs.String("table", "", "table to write (default csb_load)")
	fs.Int("seed", 42, "fixes the key sequence, so two runs are the same experiment")
	fs.Int("batch", 1, "rows per statement; rows/s is rate x batch")
	fs.String("node", "", "which node runs the load (default: a client if there is one, else the master)")
	fs.Bool("require-rate", false, "exit 1 if the driver could not hold the rate")
}

// parseRate accepts "2000/s", "2000" or "max".
func parseRate(s string) (float64, error) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "/s"))
	if s == "" || s == "max" {
		return 0, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("bad rate %q (want 2000/s, 2000 or max)", s)
	}
	return v, nil
}

func loadDriver(c *Ctx) (*load.Driver, string, error) {
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, "", err
	}
	// A client if there is one, else the master.
	//
	// Running the driver inside the database node was a compromise and never a
	// design: it competes with the engine for the CPU quota given to the engine,
	// which is why `driver_cost` exists at all. A client node is where a
	// workload belongs, so it is the default the moment one exists -- and when
	// there is none the tool says where the load actually went rather than
	// leaving somebody to assume.
	sel := c.str("node")
	if sel == "" {
		sel = "master"
		if len(t.Clients()) > 0 {
			sel = "client"
		} else if c.Noun == "load" && c.Verb == "start" {
			c.Note("load_on_the_engine", SevWarn,
				"this cluster has no client node, so the driver runs inside the database node and competes with the engine for its CPU quota; driver_cost reports what that costs (cluster create --clients 1)")
		}
	}
	names, rerr := a.Resolve(c.Ctx, sel)
	if rerr != nil || len(names) != 1 {
		return nil, "", Precondition("unresolved_selector", "load needs exactly one node: %v", rerr)
	}
	return &load.Driver{D: a.D, T: t, Workdir: a.Workdir}, names[0], nil
}

func cmdLoadStart(c *Ctx) (any, error) {
	d, node, err := loadDriver(c)
	if err != nil {
		return nil, err
	}
	rate, rerr := parseRate(c.str("rate"))
	if rerr != nil {
		return nil, Usage("%v", rerr)
	}
	conc, _ := strconv.Atoi(c.str("concurrency"))
	seed, _ := strconv.Atoi(c.str("seed"))
	// A client has no database of its own, so it addresses the master's by name
	// -- `<db>@<host>` rather than `<db>`, which resolves to localhost and fails
	// on a node that is not serving one. The driver was written when it always
	// ran inside the database node; the first load on a client failed 484
	// statements out of 484 saying so.
	target := d.T.DB
	if isClient(d.T, node) {
		master, merr := clusterMaster(c)
		if merr != nil {
			return nil, merr
		}
		target = d.T.DB + "@" + master
	}
	spec := load.Spec{
		Profile: c.str("profile"), Rate: rate, Concurrency: conc,
		ForSeconds: c.dur("for").Seconds(), Table: c.str("table"), Seed: seed,
		Batch: atoiOr(c.str("batch"), 1),
		DB:    target, Node: node, RequireRate: c.fs.Lookup("require-rate").Value.String() == "true",
	}
	if err := d.Start(c.Ctx, spec); err != nil {
		return nil, Usage("%v", err)
	}
	if load.Profiles[spec.Profile] == "host" && d.T.Resources.CPUs == 0 {
		// "Saturated" on a 32-core host and on a 4-core runner are different
		// experiments; without a quota the profile is not reproducible.
		c.Note("no_cpu_quota", SevWarn,
			"this node has no CPU quota, so a host profile saturates whatever the machine happens to have; create with --cpus to make it reproducible")
	}
	if c.Record != nil {
		b, _ := json.Marshal(spec)
		var detail map[string]any
		_ = json.Unmarshal(b, &detail)
		_ = c.Record.Append(record.ActorLoad, "load.start", detail)
	}
	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "load %s started on %s (requested %s, %d workers, seed %d)\n",
			spec.Profile, node, requestedLabel(rate), conc, seed)
		printNotes(c)
	}
	return map[string]any{"spec": spec}, nil
}

func atoiOr(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil && v > 0 {
		return v
	}
	return def
}

func requestedLabel(rate float64) string {
	if rate == 0 {
		return "max"
	}
	return fmt.Sprintf("%g/s", rate)
}

func cmdLoadStatus(c *Ctx) (any, error) {
	d, node, err := loadDriver(c)
	if err != nil {
		return nil, err
	}
	st, serr := d.Status(node)
	if serr != nil {
		if os.IsNotExist(serr) {
			return nil, Precondition("no_load", "no load has run on %s", node)
		}
		return nil, Failed("load_status_unreadable", "%v", serr)
	}
	if !st.Held {
		c.Note("load_rate_not_held", SevError,
			fmt.Sprintf("the driver asked for %s and achieved %s; every figure measured beside it is a figure about the driver",
				st.Requested, st.Achieved))
	}
	if st.Errors > 0 {
		c.Note("load_errors", SevWarn,
			fmt.Sprintf("%d statement(s) failed; last: %s", st.Errors, st.LastError))
	}
	if !c.JSON && !c.Quiet {
		rows := ""
		if st.RowsPerS != nil && st.Batch > 1 {
			rows = fmt.Sprintf("  rows=%.0f/s (batch %d)", *st.RowsPerS, st.Batch)
		}
		fmt.Fprintf(c.Out, "%-10s %-6s requested=%-8s achieved=%-10s held=%-5v sent=%-8d errors=%d  (%.0fs, %d workers)%s\n",
			st.Profile, st.Kind, st.Requested, st.Achieved, st.Held, st.Sent, st.Errors, st.Elapsed, st.Workers, rows)
		if st.Latency != nil {
			fmt.Fprintf(c.Out, "%-10s %-6s p50=%.1fms p90=%.1fms p99=%.1fms  (min %.1f, max %.1f, n=%d%s)\n",
				"", "latency", st.Latency.P50, st.Latency.P90, st.Latency.P99,
				st.Latency.Min, st.Latency.Max, st.Latency.Count,
				map[bool]string{true: "", false: ", sampled"}[st.LatencyComplete])
		}
		if len(st.Cost) > 0 {
			keys := make([]string, 0, len(st.Cost))
			for k := range st.Cost {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var parts []string
			for _, k := range keys {
				parts = append(parts, fmt.Sprintf("%s=%v", k, st.Cost[k]))
			}
			// The driver consumes what it measures; publish the cost beside the
			// figure rather than publish the figure alone.
			fmt.Fprintf(c.Out, "  driver cost: %s\n", strings.Join(parts, " "))
		}
		printNotes(c)
	}

	spec, _ := d.Spec(node)
	if spec != nil && spec.RequireRate && !st.Held {
		return st, Failed("load_rate_not_held",
			"--require-rate was given and the driver held %s of %s", st.Achieved, st.Requested)
	}
	return st, nil
}

func cmdLoadStop(c *Ctx) (any, error) {
	d, node, err := loadDriver(c)
	if err != nil {
		return nil, err
	}
	if err := d.Stop(c.Ctx, node); err != nil {
		return nil, Failed("load_stop_failed", "%v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	st, _ := d.Status(node)
	if c.Record != nil {
		detail := map[string]any{"node": node}
		if st != nil {
			detail["achieved"], detail["requested"], detail["held"] = st.Achieved, st.Requested, st.Held
		}
		_ = c.Record.Append(record.ActorLoad, "load.stop", detail)
	}
	if !c.JSON && !c.Quiet {
		if st != nil {
			fmt.Fprintf(c.Out, "load stopped on %s: requested %s, achieved %s, held=%v\n",
				node, st.Requested, st.Achieved, st.Held)
		} else {
			fmt.Fprintf(c.Out, "load stopped on %s\n", node)
		}
	}
	return map[string]any{"node": node, "status": st}, nil
}

func isClient(t *topology.Topology, node string) bool {
	for _, n := range t.Nodes {
		if n.Name == node {
			return n.IsClient()
		}
	}
	return false
}

func clusterMaster(c *Ctx) (string, error) {
	a, _, err := loadCluster(c)
	if err != nil {
		return "", err
	}
	names, rerr := a.Resolve(c.Ctx, "master")
	if rerr != nil || len(names) != 1 {
		return "", Precondition("no_master", "a load from a client needs a master to write to: %v", rerr)
	}
	return names[0], nil
}
