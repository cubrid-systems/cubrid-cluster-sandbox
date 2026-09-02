package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/inspect"
)

func watchFlags(fs *flag.FlagSet) {
	fs.Duration("interval", 500*time.Millisecond, "sample period")
	fs.Duration("for", 60*time.Second, "how long to sample")
	fs.String("out", "", "write the series as JSONL")
}

type sample struct {
	At       string `json:"at"`
	Node     string `json:"node"`
	CopyLag  *int   `json:"copy_lag_pages,omitempty"`
	ApplyLag *int   `json:"apply_lag_pages,omitempty"`
	Fail     *int   `json:"fail_counter,omitempty"`
	Source   string `json:"source"`
	Ref      string `json:"reference_source,omitempty"`
}

// stageTrack answers the question a point sample cannot: when did this stage
// start falling behind, and how far did it get.
type stageTrack struct {
	First   *int    `json:"first"`
	Last    *int    `json:"last"`
	Max     *int    `json:"max"`
	RoseAt  string  `json:"rose_at,omitempty"`
	RoseSec float64 `json:"rose_after_seconds,omitempty"`
}

func (s *stageTrack) observe(v *int, at time.Time, start time.Time) {
	if v == nil {
		return
	}
	n := *v
	if s.First == nil {
		s.First = &n
		s.Max = &n
		s.Last = &n
		return
	}
	s.Last = &n
	if n > *s.Max {
		s.Max = &n
	}
	if s.RoseAt == "" && n > *s.First {
		s.RoseAt = at.UTC().Format(time.RFC3339Nano)
		s.RoseSec = at.Sub(start).Seconds()
	}
}

// cmdReplWatch samples and retains.
//
// A point sample cannot answer "when did it start falling behind, and on which
// stage", and that is the question a developer actually has after a run. The
// series is scenario-scoped -- bounded by --for, written where the caller says,
// discarded with the cluster -- which is what keeps it from turning into a
// second operational collector (docs/design/05-inspect.md §5).
//
// It carries both stages with both provenances, or it inherits every problem in
// §3 with a timestamp attached.
func cmdReplWatch(c *Ctx) (any, error) {
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	interval := c.dur("interval")
	if interval <= 0 {
		return nil, Usage("--interval must be positive")
	}
	total := c.dur("for")

	var w *os.File
	if out := c.str("out"); out != "" {
		w, err = os.Create(out)
		if err != nil {
			return nil, Failed("out_failed", "%v", err)
		}
		defer w.Close()
	}

	// The series inherits every problem in §3 with a timestamp attached, and the
	// worst of them is that a suspended stage cannot report its own stall: the
	// view is written by the process the condition suspends, so it freezes at a
	// healthy-looking constant for the whole window. The tool knows it suspended
	// that process, so it says so once at the top rather than letting a flat line
	// speak for itself.
	for node, labels := range faultsOf(c) {
		for _, l := range labels {
			if strings.HasPrefix(l, "lag(") {
				c.Note("stale_apply_info", SevError, node+": a "+l+" condition is in force, so this node's db_ha_apply_info is frozen by the very process the series is watching")
			}
		}
	}

	start := time.Now()
	deadline := start.Add(total)
	tracks := map[string]*struct {
		Copy  stageTrack `json:"copy"`
		Apply stageTrack `json:"apply"`
		Fail  stageTrack `json:"fail_counter"`
	}{}
	n := 0
	var slowest time.Duration

	for {
		took := time.Now()
		st, err := inspect.Read(c.Ctx, a.D, t)
		if err != nil {
			return nil, Failed("inspect_failed", "%v", err)
		}
		at := time.Now()
		if d := at.Sub(took); d > slowest {
			slowest = d
		}
		for _, node := range st.Nodes {
			if node.Repl == nil {
				continue
			}
			s := sample{At: at.UTC().Format(time.RFC3339Nano), Node: node.Name,
				CopyLag: node.Repl.CopyLag, ApplyLag: node.Repl.ApplyLag, Fail: node.Repl.Fail,
				Source: node.Repl.Source, Ref: node.Repl.RefSource}
			if w != nil {
				b, _ := json.Marshal(s)
				fmt.Fprintln(w, string(b))
			}
			tr, ok := tracks[node.Name]
			if !ok {
				tr = &struct {
					Copy  stageTrack `json:"copy"`
					Apply stageTrack `json:"apply"`
					Fail  stageTrack `json:"fail_counter"`
				}{}
				tracks[node.Name] = tr
			}
			tr.Copy.observe(node.Repl.CopyLag, at, start)
			tr.Apply.observe(node.Repl.ApplyLag, at, start)
			tr.Fail.observe(node.Repl.Fail, at, start)
			n++
		}
		if time.Now().After(deadline) || c.Ctx.Err() != nil {
			break
		}
		if wait := interval - time.Since(took); wait > 0 {
			time.Sleep(wait)
		}
	}

	// A sample that takes longer than the period asked for is not the period
	// asked for. The load driver reports the rate it actually held; so does this.
	if slowest > interval {
		c.Note("interval_not_held", SevWarn, fmt.Sprintf(
			"one sample took %s against a --interval of %s: reading both stages is several commands per node, and the series is spaced by what that costs",
			slowest.Round(time.Millisecond), interval))
	}
	if n == 0 {
		c.Note("no_replication_rows", SevWarn,
			"no node reported a replication position for the whole window; a master has none, and a node that has just changed role has none either")
	}

	if !c.JSON && !c.Quiet {
		for name, tr := range tracks {
			fmt.Fprintf(c.Out, "%-16s copy %s   apply %s   fail %s\n", name,
				describeTrack(&tr.Copy), describeTrack(&tr.Apply), describeTrack(&tr.Fail))
		}
		printNotes(c)
	}
	return map[string]any{
		"samples": n, "seconds": time.Since(start).Seconds(),
		"interval": interval.String(), "nodes": tracks,
	}, nil
}

func describeTrack(s *stageTrack) string {
	if s.First == nil {
		return "—"
	}
	out := fmt.Sprintf("%d→%d (max %d)", *s.First, *s.Last, *s.Max)
	if s.RoseAt != "" {
		out += fmt.Sprintf(" rose at +%.1fs", s.RoseSec)
	}
	return out
}
