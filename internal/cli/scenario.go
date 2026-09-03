package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/load"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/record"
)

// A scenario is a sequence of this tool's own verbs and the state they are
// expected to reach, written down so that "does my change still behave" is one
// command against a build rather than knowledge somebody has to already have.
//
// Until now that knowledge lived in `harness/*.sh` and in people's heads: eight
// scripts, each encoding a sequence and its expectations, written for this
// project's own findings rather than to be pointed at somebody else's engine.
//
// Two rules shape the format.
//
// **It is the same verbs.** A step is an argv this tool already accepts, run
// through the same dispatch with the same envelope and the same exit code. A
// scenario cannot ask for anything the command line cannot, which is what keeps
// it from becoming a second way of asking.
//
// **The build is not in the file.** `--build` is an argument to the run, because
// a scenario is a statement about behaviour and the engine under test is the
// variable (DESIGN.md §2 G2). The same file runs against the build you just
// made and the one you are comparing it with.
//
// JSON rather than YAML because this tool has no dependencies and is not about
// to acquire one for a config format; `describe` is JSON for the same reason.
type Scenario struct {
	Name    string          `json:"name"`
	Cluster ScenarioCluster `json:"cluster"`
	Steps   []Step          `json:"steps"`

	// Matrix and Repeats turn one scenario into many runs, because half of what
	// people actually write is a sweep rather than a reproduction: vary one
	// thing, hold the rest, repeat it, and read a table. Every measurement
	// script in `harness/` has that skeleton, and rewriting it by hand is where
	// this project has put most of its own bugs.
	//
	// A value is substituted wherever ${name} appears, and nowhere else needs a
	// rule: one substitution, applied to the cluster's parameters and to every
	// step's arguments.
	Matrix  map[string][]any `json:"matrix,omitempty"`
	Repeats int              `json:"repeats,omitempty"`

	// Measure names what to collect from each run. It is a closed list, and
	// every entry is a field this tool already emits -- so a table cannot report
	// something nobody can go and look at.
	Measure []string `json:"measure,omitempty"`
}

type ScenarioCluster struct {
	Preset     string   `json:"preset,omitempty"`
	Network    string   `json:"network,omitempty"`
	WithBroker bool     `json:"with_broker,omitempty"`
	Set        []string `json:"set,omitempty"`
	SetHidden  []string `json:"set_hidden,omitempty"`
}

// Step is one verb, or one wait for a state, or both.
//
// The expectations are deliberately three, and each one exists because a real
// scenario in `harness/` needed it:
//
//   - **the exit code**, which the verbs already carry -- `repl check` exits 4
//     when the row does not arrive and `repl diff` exits 1 when the two sides
//     differ, so most judgements need no new vocabulary at all;
//   - **what the step printed**, because reproducing a bug is almost always
//     "these rows" (`scenario-cbrd26983.sh` checks for the id sequence
//     `1 2 21 22 41 42 61`) and because the split-brain flavours are told apart
//     by one line of the engine's own log and nothing else;
//   - **how long a role change took**, read from the run record, which measures
//     it from this tool's own event to the engine's own line.
type Step struct {
	Note       string     `json:"note,omitempty"`
	Run        []string   `json:"run,omitempty"`
	ExpectExit *int       `json:"expect_exit,omitempty"`
	Contains   []string   `json:"contains,omitempty"`           // every one must appear in what the step printed
	Absent     []string   `json:"absent,omitempty"`             // and none of these may
	RoleChange string     `json:"role_change_within,omitempty"` // e.g. "10s", against the record
	Await      *Condition `json:"await,omitempty"`
	Within     string     `json:"within,omitempty"`
}

// Condition is deliberately a short, closed list. Everything in it is something
// the tool already reports, so an expectation cannot drift away from what a
// person can go and look at.
type Condition struct {
	Masters  *int `json:"masters,omitempty"`
	Standbys *int `json:"standbys,omitempty"`

	// MasterIs names which node has to be serving, by the role it was CREATED
	// with -- "slave" means the failover happened. Counting masters cannot say
	// that: during a partition the old master is still one of them, so
	// `masters: 1` is already true before anything has moved, and a scenario
	// written that way waits for nothing. That is how this field came to exist.
	MasterIs string `json:"master_is,omitempty"`
}

func (c Condition) String() string {
	var p []string
	if c.Masters != nil {
		p = append(p, fmt.Sprintf("masters=%d", *c.Masters))
	}
	if c.Standbys != nil {
		p = append(p, fmt.Sprintf("standbys=%d", *c.Standbys))
	}
	if c.MasterIs != "" {
		p = append(p, "master_is="+c.MasterIs)
	}
	return strings.Join(p, " ")
}

// runResult is one point of the matrix, once.
type runResult struct {
	Binding map[string]string `json:"binding"`
	Cluster string            `json:"cluster"`
	Steps   []stepResult      `json:"steps"`
	Measure map[string]any    `json:"measure,omitempty"`
	Passed  bool              `json:"passed"`
}

type stepResult struct {
	N       int     `json:"step"`
	What    string  `json:"what"`
	Exit    int     `json:"exit,omitempty"`
	Seconds float64 `json:"seconds"`
	OK      bool    `json:"ok"`
	Why     string  `json:"why,omitempty"`
}

func scenarioFlags(fs *flag.FlagSet) {
	fs.String("build", "", "the engine under test; the scenario does not name one")
	fs.String("name", "", "cluster name (default: derived from the scenario)")
	fs.Bool("keep", false, "leave the cluster standing when a step fails")
}

// cmdScenarioRun stands a cluster up, walks the steps, and says pass or fail.
func cmdScenarioRun(c *Ctx) (any, error) {
	if len(c.Args) != 1 {
		return nil, Usage("scenario run needs one file: csb scenario run ha-split-brain.json --build ~/install.out")
	}
	b, err := os.ReadFile(c.Args[0])
	if err != nil {
		return nil, Precondition("no_scenario", "%v", err)
	}
	var s Scenario
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, Usage("%s is not a scenario: %v", c.Args[0], err)
	}
	if len(s.Steps) == 0 {
		return nil, Usage("%s has no steps", c.Args[0])
	}
	build := c.str("build")
	if build == "" {
		return nil, Usage("scenario run needs --build: the scenario says what should happen, the build is what it happens to")
	}

	base := c.str("name")
	if base == "" {
		base = fmt.Sprintf("scn%d", time.Now().Unix()%100000)
	}

	runs := points(s.Matrix, s.Repeats)
	all := make([]runResult, 0, len(runs))
	anyFailed := false

	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "%s\n  on %s", s.Name, build)
		if len(runs) > 1 {
			fmt.Fprintf(c.Out, "  (%d runs)", len(runs))
		}
		fmt.Fprintln(c.Out)
	}

	for ri, binding := range runs {
		name := base
		if len(runs) > 1 {
			name = fmt.Sprintf("%s%d", base, ri+1)
		}
		c.Cluster = name
		c.Record = record.Open(c.Store.RecordPath(name))

		create := []string{"cluster", "create", "--name", name, "--build", build}
		if s.Cluster.Preset != "" {
			create = append(create, "--preset", s.Cluster.Preset)
		}
		if s.Cluster.Network != "" {
			create = append(create, "--network", s.Cluster.Network)
		}
		if s.Cluster.WithBroker {
			create = append(create, "--with-broker")
		}
		for _, kv := range substituteAll(s.Cluster.Set, binding) {
			create = append(create, "--set", kv)
		}
		for _, kv := range substituteAll(s.Cluster.SetHidden, binding) {
			create = append(create, "--set-hidden", kv)
		}

		rr := runResult{Binding: binding, Cluster: name}
		if !c.JSON && !c.Quiet && len(runs) > 1 {
			fmt.Fprintf(c.Out, "\n%s\n", bindingLine(binding))
		}
		failed := false
		record := func(r stepResult) {
			rr.Steps = append(rr.Steps, r)
			if !r.OK {
				failed = true
			}
			if !c.JSON && !c.Quiet {
				mark := "ok  "
				if !r.OK {
					mark = "FAIL"
				}
				fmt.Fprintf(c.Out, "  %s %-52s %5.1fs%s\n", mark, r.What, r.Seconds, whyOf(r))
			}
		}
		record(c.step(len(rr.Steps)+1, "cluster create", create, Step{}))
		for _, st := range s.Steps {
			if failed {
				break
			}
			b := st
			b.Run = substituteAll(st.Run, binding)
			b.Note = substitute(st.Note, binding)
			record(c.runStep(len(rr.Steps)+1, b, name))
		}
		if !failed && len(s.Measure) > 0 {
			rr.Measure = c.measurements(s.Measure)
		}
		rr.Passed = !failed
		anyFailed = anyFailed || failed

		keep := failed && c.str("keep") == "true"
		if !keep {
			_, _ = dispatchArgs([]string{"cluster", "destroy", "--purge"}, name, c.Timeout, nil)
		} else if !c.JSON && !c.Quiet {
			fmt.Fprintf(c.Out, "  kept %s; csb cluster destroy --cluster %s --purge\n", name, name)
		}
		all = append(all, rr)
	}

	if !c.JSON && !c.Quiet && len(s.Measure) > 0 {
		printMeasureTable(c, s, all)
	}

	out := map[string]any{"scenario": s.Name, "build": build, "runs": all, "passed": !anyFailed}
	if anyFailed {
		return out, Failed("scenario_failed", "%s did not behave as %s says it should", build, c.Args[0])
	}
	if !c.JSON && !c.Quiet {
		fmt.Fprintf(c.Out, "\nPASS — %d run(s)\n", len(all))
	}
	return out, nil
}

// points expands the matrix into one binding per combination, in a stable order,
// repeated. A scenario with no matrix is one point, which is the reproduction
// case and needs no special path.
func points(m map[string][]any, repeats int) []map[string]string {
	if repeats < 1 {
		repeats = 1
	}
	out := []map[string]string{{}}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var next []map[string]string
		for _, base := range out {
			for _, v := range m[k] {
				b := map[string]string{}
				for bk, bv := range base {
					b[bk] = bv
				}
				b[k] = fmt.Sprint(v)
				next = append(next, b)
			}
		}
		out = next
	}
	var all []map[string]string
	for r := 1; r <= repeats; r++ {
		for _, p := range out {
			q := map[string]string{"repeat": fmt.Sprint(r)}
			for k, v := range p {
				q[k] = v
			}
			all = append(all, q)
		}
	}
	return all
}

func substitute(s string, binding map[string]string) string {
	for k, v := range binding {
		s = strings.ReplaceAll(s, "${"+k+"}", v)
	}
	return s
}

func substituteAll(in []string, binding map[string]string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = substitute(v, binding)
	}
	return out
}

// measurements reads the closed list from the verbs that already report them.
func (c *Ctx) measurements(want []string) map[string]any {
	got := map[string]any{}
	need := func(prefix string) bool {
		for _, w := range want {
			if strings.HasPrefix(w, prefix) {
				return true
			}
		}
		return false
	}
	if need("role_change.") {
		m, p := c.roleChange()
		got["role_change.measured"], got["role_change.predicted"] = m, p
	}
	if need("load.") {
		// Both roles, first answer wins.
		//
		// By NAME, not by role. `master` is a query answered by the engine rather
		// than a label, which is a feature of this tool -- and it means a
		// failover moves it. A load started on the master and measured after the
		// failover was read from the node that never ran it, and the table came
		// back empty twice before this was the answer. A measurement must not be
		// addressed by something that moves while it is being taken.
		for _, sel := range []string{c.Cluster + "-n1", c.Cluster + "-n2"} {
			var buf bytes.Buffer
			code, _ := dispatchArgs([]string{"load", "status", "--json", "--node", sel}, c.Cluster, c.Timeout, &buf)
			if code != 0 {
				continue
			}
			var env struct {
				Data load.Status `json:"data"`
			}
			if json.Unmarshal(buf.Bytes(), &env) != nil || env.Data.Sent == 0 {
				continue
			}
			got["load.achieved"] = env.Data.AchievedV
			got["load.held"] = env.Data.Held
			if env.Data.Latency != nil {
				got["load.p50_ms"] = env.Data.Latency.P50
				got["load.p90_ms"] = env.Data.Latency.P90
				got["load.p99_ms"] = env.Data.Latency.P99
			}
			break
		}
	}
	if need("canary.") {
		var buf bytes.Buffer
		if code, _ := dispatchArgs([]string{"repl", "check", "--json"}, c.Cluster, c.Timeout, &buf); code == 0 {
			var env struct {
				Data struct {
					Seconds float64 `json:"seconds"`
				} `json:"data"`
			}
			if json.Unmarshal(buf.Bytes(), &env) == nil {
				got["canary.seconds"] = env.Data.Seconds
			}
		}
	}
	if need("diff.") {
		code, _ := dispatchArgs([]string{"repl", "diff", "--json"}, c.Cluster, c.Timeout, nil)
		got["diff.differs"] = code != 0
	}
	out := map[string]any{}
	for _, w := range want {
		if v, ok := got[w]; ok {
			out[w] = v
		} else {
			out[w] = nil
		}
	}
	return out
}

func (c *Ctx) roleChange() (measured, predicted any) {
	out := filepath.Join(os.TempDir(), fmt.Sprintf("csb-scn-%d.json", time.Now().UnixNano()))
	defer os.Remove(out)
	if code, _ := dispatchArgs([]string{"record", "export", "--out", out}, c.Cluster, c.Timeout, nil); code != 0 {
		return nil, nil
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return nil, nil
	}
	var doc struct {
		RoleChanges []struct {
			Measured  string `json:"measured"`
			Predicted string `json:"predicted"`
		} `json:"role_changes"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return nil, nil
	}
	// The LAST measured change, not the first. The first one in any record is the
	// promotion that happens while the cluster is being created, and reporting it
	// as the scenario's result produced a table of create-time numbers that
	// looked like failover numbers -- 32.3 s against a shell harness's 6.9 s for
	// the same setting, which is how it was caught.
	for i := len(doc.RoleChanges) - 1; i >= 0; i-- {
		if doc.RoleChanges[i].Measured != "" {
			return doc.RoleChanges[i].Measured, doc.RoleChanges[i].Predicted
		}
	}
	return nil, nil
}

func whyOf(r stepResult) string {
	if r.Why == "" {
		return ""
	}
	return "  " + r.Why
}

// step runs one verb and judges it by its exit code and by what it printed.
func (c *Ctx) step(n int, what string, argv []string, st Step) stepResult {
	start := time.Now()
	var buf bytes.Buffer
	code, _ := dispatchArgs(argv, c.Cluster, c.Timeout, &buf)
	r := stepResult{N: n, What: what, Exit: code, Seconds: time.Since(start).Seconds()}
	want := 0
	if st.ExpectExit != nil {
		want = *st.ExpectExit
	}
	if code != want {
		r.Why = fmt.Sprintf("exit %d, want %d", code, want)
		return r
	}
	out := buf.String()
	for _, want := range st.Contains {
		if !strings.Contains(out, want) {
			r.Why = fmt.Sprintf("did not print %q", want)
			return r
		}
	}
	for _, no := range st.Absent {
		if strings.Contains(out, no) {
			r.Why = fmt.Sprintf("printed %q, which it should not", no)
			return r
		}
	}
	if st.RoleChange != "" {
		if why := c.roleChangeWithin(st.RoleChange); why != "" {
			r.Why = why
			return r
		}
	}
	r.OK = true
	return r
}

// roleChangeWithin reads the record rather than timing the step, because the
// interval that matters is from this tool's own event to the engine's own line,
// and the record is where those two are already in one timeline.
func (c *Ctx) roleChangeWithin(bound string) string {
	limit, err := time.ParseDuration(bound)
	if err != nil {
		return "role_change_within is not a duration: " + bound
	}
	var buf bytes.Buffer
	if _, derr := dispatchArgs([]string{"record", "show", "--json"}, c.Cluster, c.Timeout, &buf); derr != nil {
		return "the record could not be read: " + derr.Error()
	}
	// record show carries the timeline; the measured intervals are in export's
	// document, so the bound is checked against the same builder.
	buf.Reset()
	out := filepath.Join(os.TempDir(), fmt.Sprintf("csb-scn-%d.json", time.Now().UnixNano()))
	defer os.Remove(out)
	if code, _ := dispatchArgs([]string{"record", "export", "--out", out}, c.Cluster, c.Timeout, &buf); code != 0 {
		return "the record could not be exported"
	}
	b, rerr := os.ReadFile(out)
	if rerr != nil {
		return "the record could not be read back"
	}
	var doc struct {
		RoleChanges []struct {
			Node     string `json:"node"`
			Result   string `json:"result"`
			Measured string `json:"measured"`
		} `json:"role_changes"`
	}
	if jerr := json.Unmarshal(b, &doc); jerr != nil {
		return "the record is not readable: " + jerr.Error()
	}
	seen := false
	for _, rc := range doc.RoleChanges {
		if rc.Measured == "" {
			continue
		}
		d, perr := time.ParseDuration(rc.Measured)
		if perr != nil {
			continue
		}
		seen = true
		if d > limit {
			return fmt.Sprintf("%s took %s, longer than %s", rc.Node, rc.Measured, bound)
		}
	}
	if !seen {
		return "no role change was measured, so " + bound + " is not a claim about anything"
	}
	return ""
}

func (c *Ctx) runStep(n int, st Step, cluster string) stepResult {
	switch {
	case len(st.Run) > 0 && st.Await == nil:
		what := st.Note
		if what == "" {
			what = strings.Join(st.Run, " ")
		}
		return c.step(n, what, st.Run, st)
	case st.Await != nil:
		within := 60 * time.Second
		if st.Within != "" {
			if d, err := time.ParseDuration(st.Within); err == nil {
				within = d
			}
		}
		what := st.Note
		if what == "" {
			what = "await " + st.Await.String()
		}
		start := time.Now()
		if len(st.Run) > 0 {
			if code, _ := dispatchArgs(st.Run, cluster, c.Timeout, nil); code != 0 {
				return stepResult{N: n, What: what, Exit: code, Seconds: time.Since(start).Seconds(),
					Why: fmt.Sprintf("the step before the wait exited %d", code)}
			}
		}
		deadline := time.Now().Add(within)
		for {
			m, sb, createdAs, err := rolesFor(cluster, c.Timeout)
			if err == nil && st.Await.met(m, sb, createdAs) {
				return stepResult{N: n, What: what, Seconds: time.Since(start).Seconds(), OK: true}
			}
			if time.Now().After(deadline) {
				return stepResult{N: n, What: what, Seconds: time.Since(start).Seconds(),
					Why: fmt.Sprintf("still masters=%d standbys=%d master_is=%s after %s", m, sb, createdAs, within)}
			}
			time.Sleep(2 * time.Second)
		}
	}
	return stepResult{N: n, What: "empty step", Why: "a step must have run, await, or both"}
}

func (c Condition) met(masters, standbys int, masterCreatedAs string) bool {
	if c.Masters != nil && *c.Masters != masters {
		return false
	}
	if c.Standbys != nil && *c.Standbys != standbys {
		return false
	}
	if c.MasterIs != "" && c.MasterIs != masterCreatedAs {
		return false
	}
	return true
}

// rolesFor reads the roles through `ha status`, which is the same answer a
// person gets, rather than through a private path.
func rolesFor(cluster string, timeout time.Duration) (masters, standbys int, masterCreatedAs string, err error) {
	var buf bytes.Buffer
	if _, derr := dispatchArgs([]string{"ha", "status", "--json"}, cluster, timeout, &buf); derr != nil {
		return 0, 0, "", derr
	}
	var env struct {
		Data struct {
			Nodes []struct {
				Server  string `json:"server_state"`
				Created string `json:"created_role"`
			} `json:"nodes"`
		} `json:"data"`
	}
	if jerr := json.Unmarshal(buf.Bytes(), &env); jerr != nil {
		return 0, 0, "", jerr
	}
	for _, n := range env.Data.Nodes {
		switch n.Server {
		case "registered_and_active":
			masters++
			masterCreatedAs = n.Created
		case "registered_and_standby":
			standbys++
		}
	}
	return masters, standbys, masterCreatedAs, nil
}

// dispatchArgs runs one verb through the ordinary command path.
//
// The globals go BEFORE a bare `--`, because everything after it belongs to the
// program being run on a node rather than to this tool.
func dispatchArgs(argv []string, cluster string, timeout time.Duration, capture *bytes.Buffer) (int, error) {
	if len(argv) < 2 {
		return ExitUsage, fmt.Errorf("a step needs a noun and a verb")
	}
	cmd, ok := lookup(argv[0], argv[1])
	if !ok {
		return ExitUsage, fmt.Errorf("no such command: %s %s", argv[0], argv[1])
	}
	globals := []string{"--cluster", cluster, "--timeout", timeout.String()}
	rest := argv[2:]
	full := []string{}
	if i := indexOfArg(rest, "--"); i >= 0 {
		full = append(full, rest[:i]...)
		full = append(full, globals...)
		full = append(full, rest[i:]...)
	} else {
		full = append(full, rest...)
		full = append(full, globals...)
	}
	var out io.Writer = io.Discard
	if capture != nil {
		out = capture
	}
	return dispatch(cmd, full, out, io.Discard)
}

func indexOfArg(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

// bindingLine names the point a run is at, which is the row's identity in the
// table and the first thing somebody reading a failure needs.
func bindingLine(b map[string]string) string {
	keys := make([]string, 0, len(b))
	for k := range b {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var p []string
	for _, k := range keys {
		p = append(p, k+"="+b[k])
	}
	return strings.Join(p, " ")
}

// printMeasureTable is the point of a sweep. A scenario with a matrix is not
// asking pass or fail, it is asking for a table -- one row per point per repeat,
// which is the shape every measurement in `docs/findings/` was written from.
func printMeasureTable(c *Ctx, s Scenario, runs []runResult) {
	if len(runs) == 0 {
		return
	}
	var vars []string
	for k := range runs[0].Binding {
		vars = append(vars, k)
	}
	sort.Strings(vars)

	fmt.Fprintln(c.Out)
	for _, v := range vars {
		fmt.Fprintf(c.Out, "%-14s", v)
	}
	for _, m := range s.Measure {
		fmt.Fprintf(c.Out, "%-22s", m)
	}
	fmt.Fprintln(c.Out)
	for _, r := range runs {
		for _, v := range vars {
			fmt.Fprintf(c.Out, "%-14s", r.Binding[v])
		}
		for _, m := range s.Measure {
			val := "—"
			if r.Measure != nil && r.Measure[m] != nil {
				val = fmt.Sprint(r.Measure[m])
			}
			if !r.Passed {
				val = "(failed)"
			}
			fmt.Fprintf(c.Out, "%-22s", val)
		}
		fmt.Fprintln(c.Out)
	}
}
