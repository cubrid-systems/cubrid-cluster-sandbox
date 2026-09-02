//go:build e2e

// Package e2e runs the whole command surface against a real CUBRID build.
//
// It exists because two mechanisms in this tool were written, documented, and
// never executed -- `partition --mechanism drop` and `ping-unavailable
// --mechanism icmp` both needed an iptables the base image did not carry, and
// both failed the first time anybody ran them, months after they were merged.
// A mechanism nobody has run is a mechanism nobody has.
//
// It asserts on the JSON envelope rather than on printed text, because the
// envelope is the contract a consumer holds (docs/design/01-cli.md §4) and
// prose is free to change.
//
//	CSB_E2E_BUILD=~/cubrid/install.out go test -tags e2e -timeout 30m ./e2e/
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/cli"
)

type csb struct {
	t       *testing.T
	bin     string
	home    string
	cluster string
}

// run invokes one command in --json and decodes the envelope. A command that
// produces no envelope at all is a failure of the contract, not of the verb.
func (c *csb) run(args ...string) (*cli.Envelope, int) {
	c.t.Helper()
	full := append([]string{}, args...)
	full = append(full, "--json", "--cluster", c.cluster)
	cmd := exec.Command(c.bin, full...)
	cmd.Env = append(os.Environ(), "CSB_HOME="+c.home)
	out, err := cmd.Output()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		c.t.Fatalf("csb %s: %v", strings.Join(args, " "), err)
	}
	var e cli.Envelope
	if jerr := json.Unmarshal(out, &e); jerr != nil {
		c.t.Fatalf("csb %s exited %d and did not produce an envelope: %v\n%s",
			strings.Join(args, " "), code, jerr, out)
	}
	if e.Schema != cli.SchemaVersion {
		c.t.Errorf("csb %s: schema %q, want %q", strings.Join(args, " "), e.Schema, cli.SchemaVersion)
	}
	if (code == cli.ExitOK) != e.OK {
		c.t.Errorf("csb %s: exit %d disagrees with ok=%v", strings.Join(args, " "), code, e.OK)
	}
	return &e, code
}

func (c *csb) must(args ...string) map[string]any {
	c.t.Helper()
	e, code := c.run(args...)
	if code != cli.ExitOK {
		c.t.Fatalf("csb %s exited %d: %s", strings.Join(args, " "), code, notes(e))
	}
	m, _ := e.Data.(map[string]any)
	return m
}

func (c *csb) wantExit(want int, args ...string) *cli.Envelope {
	c.t.Helper()
	e, code := c.run(args...)
	if code != want {
		c.t.Errorf("csb %s exited %d, want %d: %s", strings.Join(args, " "), code, want, notes(e))
	}
	return e
}

func notes(e *cli.Envelope) string {
	var s []string
	for _, n := range e.Notes {
		s = append(s, n.Code+": "+n.Message)
	}
	return strings.Join(s, "; ")
}

func hasNote(e *cli.Envelope, code string) bool {
	for _, n := range e.Notes {
		if n.Code == code {
			return true
		}
	}
	return false
}

// nodes reads the per-node list that `cluster status`, `node status` and
// `ha status` return. `repl status` keys its nodes by name instead, which is
// why replOf is separate rather than one clever helper over both shapes.
func (c *csb) nodes(verb string) []map[string]any {
	c.t.Helper()
	d := c.must(strings.Fields(verb)...)
	raw, _ := d["nodes"].([]any)
	var out []map[string]any
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func (c *csb) replOf(node string) map[string]any {
	c.t.Helper()
	d := c.must("repl", "status", "--timeout", "60s")
	per, _ := d["nodes"].(map[string]any)
	m, _ := per[node].(map[string]any)
	return m
}

func (c *csb) roles() map[string]string {
	c.t.Helper()
	out := map[string]string{}
	for _, n := range c.nodes("ha status") {
		name, _ := n["name"].(string)
		st, _ := n["server_state"].(string)
		out[name] = st
	}
	return out
}

// until polls a condition, because every wait in this tool is bounded and a
// test that waits forever is a test that hangs CI.
func (c *csb) until(what string, d time.Duration, cond func() bool) {
	c.t.Helper()
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("timed out after %s waiting for %s (roles: %v)", d, what, c.roles())
		}
		time.Sleep(time.Second)
	}
}

func mastersOf(roles map[string]string) []string {
	var m []string
	for n, s := range roles {
		if s == "registered_and_active" {
			m = append(m, n)
		}
	}
	return m
}

func TestSurface(t *testing.T) {
	build := os.Getenv("CSB_E2E_BUILD")
	if build == "" {
		t.Skip("set CSB_E2E_BUILD to a CUBRID install tree to run the surface against a real engine")
	}
	bin, err := filepath.Abs("../bin/csb")
	if err != nil || !exists(bin) {
		t.Fatalf("build the binary first (make build): %v", err)
	}

	// Two nodes are about a gigabyte of volumes. t.TempDir() puts that under
	// TMPDIR, which is how this project once filled a disk to zero bytes free,
	// so there is an explicit way to put it somewhere with room.
	home := os.Getenv("CSB_E2E_HOME")
	if home == "" {
		home = t.TempDir()
	}
	c := &csb{t: t, bin: bin, home: home,
		cluster: fmt.Sprintf("e2e%d", time.Now().Unix()%100000)}
	t.Logf("cluster %s, state under %s", c.cluster, c.home)

	// The cluster outlives every subtest and is removed even when one fails,
	// because a failed run that leaves containers behind costs the next run.
	defer func() {
		c.t = t // subtests moved it; the cleanup belongs to the outer test
		if _, code := c.run("cluster", "destroy", "--purge", "--timeout", "300s"); code != cli.ExitOK {
			t.Errorf("destroy exited %d", code)
		}
	}()

	t.Run("create", func(t *testing.T) {
		c.t = t
		d := c.must("cluster", "create", "--name", c.cluster, "--build", build,
			"--with-broker", "--timeout", "900s")
		if d["state"] != "serving" {
			t.Fatalf("state = %v, want serving", d["state"])
		}
		roles := c.roles()
		if len(mastersOf(roles)) != 1 {
			t.Fatalf("roles = %v, want exactly one master", roles)
		}
	})
	c.t = t
	if t.Failed() {
		return
	}

	master, standby := c.pair()

	t.Run("inspect reports absence rather than zero", func(t *testing.T) {
		c.t = t
		// A standby has a replication position; a master has none. Printing 0
		// for the master is the class of lie 05-inspect.md §3 is about.
		if r := c.replOf(standby); r["apply_lag_pages"] == nil {
			t.Errorf("%s reports no apply_lag while replicating: %v", standby, r)
		}
		if r := c.replOf(master); r["copy_lag_pages"] != nil {
			t.Errorf("%s is the master and reported a copy position: %v", master, r)
		}
	})

	t.Run("node logs names the process, not the file", func(t *testing.T) {
		c.t = t
		d := c.must("node", "logs", "master", "--lines", "2", "--timeout", "60s")
		files, _ := d["files"].([]any)
		kinds := map[string]bool{}
		for _, f := range files {
			if m, ok := f.(map[string]any); ok {
				kinds[fmt.Sprint(m["kind"])] = true
			}
		}
		if !kinds["master"] {
			t.Errorf("no master log found among %v", kinds)
		}
		// A gap and a typo are different things to a consumer.
		c.wantExit(cli.ExitUsage, "node", "logs", "master", "--which", "nonsense")
	})

	t.Run("the canary arrives", func(t *testing.T) {
		c.t = t
		d := c.must("repl", "check", "--timeout", "60s")
		if d["arrived"] != true {
			t.Fatalf("the canary did not arrive: %v", d)
		}
	})

	t.Run("watch sees the stage that is stalled, not the one that says so", func(t *testing.T) {
		c.t = t
		c.must("load", "start", "--profile", "insert", "--rate", "40/s", "--for", "60s", "--timeout", "60s")
		c.must("fault", "lag", standby, "--stage", "apply", "--mechanism", "suspend", "--timeout", "60s")

		e, code := c.run("repl", "watch", "--for", "10s", "--interval", "1s", "--timeout", "60s")
		if code != cli.ExitOK {
			t.Fatalf("watch exited %d: %s", code, notes(e))
		}
		// The suspended stage cannot report its own stall, so the tool must say
		// which process it suspended rather than let a flat line speak.
		if !hasNote(e, "stale_apply_info") {
			t.Errorf("watch did not warn that the view is frozen: %s", notes(e))
		}
		d, _ := e.Data.(map[string]any)
		per, _ := d["nodes"].(map[string]any)
		st, _ := per[standby].(map[string]any)
		copyT, _ := st["copy"].(map[string]any)
		applyT, _ := st["apply"].(map[string]any)
		if copyT["rose_at"] == nil {
			t.Errorf("the copy stage did not move while the applier was suspended: %v", copyT)
		}
		if applyT["rose_at"] != nil {
			t.Errorf("the frozen view reported movement: %v", applyT)
		}
		c.must("fault", "clear", standby, "--timeout", "60s")
		c.must("load", "stop", "--timeout", "60s")
	})

	t.Run("ping-unavailable breaks the check, not the network", func(t *testing.T) {
		c.t = t
		for _, mech := range []string{"binary", "icmp"} {
			c.must("fault", "ping-unavailable", standby, "--mechanism", mech, "--timeout", "60s")
			if rc := c.pingRC(standby); rc == 0 {
				t.Errorf("--mechanism %s: ping still succeeds", mech)
			}
			c.must("fault", "clear", standby, "--timeout", "60s")
			if rc := c.pingRC(standby); rc != 0 {
				t.Errorf("--mechanism %s: clear left ping at rc=%d", mech, rc)
			}
		}
	})

	t.Run("a dropped-packet partition leaves the route in place", func(t *testing.T) {
		c.t = t
		c.must("fault", "partition", standby, "--mechanism", "drop", "--timeout", "60s")
		c.until("two masters", 90*time.Second, func() bool {
			return len(mastersOf(c.roles())) == 2
		})
		c.must("fault", "clear", "--timeout", "60s")
		c.until("one master again", 120*time.Second, func() bool {
			return len(mastersOf(c.roles())) == 1
		})
	})

	t.Run("a cleanly stopped group comes back", func(t *testing.T) {
		c.t = t
		// This is here because of what it once said. For a while this project
		// reported that `down` then `up` left the original master stuck in
		// registered_and_to_be_active; ten runs across five conditions never
		// reproduced it and the claim was withdrawn without a cause. An
		// unexplained observation deserves a standing check, not a paragraph.
		c.must("cluster", "down", "--timeout", "300s")
		d := c.must("cluster", "up", "--timeout", "600s")
		if d["state"] != "serving" {
			t.Fatalf("up reached %v, not serving", d["state"])
		}
		if d["promotion_forced"] == true {
			t.Errorf("the group needed a forced promotion to come back: %v", d)
		}
		if m := mastersOf(c.roles()); len(m) != 1 || m[0] != master {
			t.Errorf("after down/up the master is %v, want [%s]", m, master)
		}
	})

	t.Run("promote makes the heartbeat decide", func(t *testing.T) {
		c.t = t
		e, code := c.run("ha", "promote", standby, "--timeout", "300s")
		if code != cli.ExitOK {
			t.Fatalf("promote exited %d: %s", code, notes(e))
		}
		if !hasNote(e, "not_a_demotion") {
			t.Errorf("promote did not say what it actually did: %s", notes(e))
		}
		d, _ := e.Data.(map[string]any)
		if secs, _ := d["seconds"].(float64); secs <= 0 || secs > 120 {
			t.Errorf("promotion took %v s", d["seconds"])
		}
		// Asking again is a no-op rather than a second promotion.
		d2 := c.must("ha", "promote", standby, "--timeout", "60s")
		if d2["changed"] != false {
			t.Errorf("promoting the master again reported a change: %v", d2)
		}
	})

	t.Run("failback returns service and proves replication", func(t *testing.T) {
		c.t = t
		c.must("node", "start", master, "--timeout", "200s")
		c.until(master+" back in the group", 120*time.Second, func() bool {
			return strings.HasPrefix(c.roles()[master], "registered_and_")
		})

		// --dry-run changes nothing, and says so.
		d := c.must("ha", "failback", "--dry-run", "--timeout", "120s")
		if d["changed"] != false {
			t.Fatalf("--dry-run reported a change: %v", d)
		}
		if len(mastersOf(c.roles())) != 1 || c.roles()[master] == "registered_and_active" {
			t.Fatalf("--dry-run moved the roles: %v", c.roles())
		}

		e, code := c.run("ha", "failback", "--yes", "--quiesce", "--timeout", "400s")
		if code != cli.ExitOK {
			t.Fatalf("failback exited %d: %s", code, notes(e))
		}
		fb, _ := e.Data.(map[string]any)
		if fb["to"] != master {
			t.Fatalf("service went to %v, want %s", fb["to"], master)
		}
		// Roles alone say the group agrees, not that replication carries
		// anything, so the verb has to have proved it with a write.
		if _, ok := fb["canary_seconds"].(float64); !ok {
			t.Errorf("failback did not verify replication: %v", fb)
		}
		if c.roles()[master] != "registered_and_active" {
			t.Errorf("roles after failback: %v", c.roles())
		}
	})

	t.Run("the record carries the run without being switched on", func(t *testing.T) {
		c.t = t
		d := c.must("record", "show", "--timeout", "60s")
		tl, _ := d["timeline"].([]any)
		if len(tl) < 10 {
			t.Fatalf("timeline has %d entries", len(tl))
		}
		// Append order is not time order: the engine's own lines are harvested
		// after the fact, and two views of one run must not disagree about when
		// things happened.
		last := ""
		for _, raw := range tl {
			m, _ := raw.(map[string]any)
			at, _ := m["t"].(string)
			if last != "" && at < last {
				t.Fatalf("timeline is out of order at %s (after %s)", at, last)
			}
			last = at
		}
		out := filepath.Join(c.home, "run.json")
		c.must("record", "export", "--out", out, "--timeout", "60s")
		if !exists(out) {
			t.Fatalf("export wrote nothing to %s", out)
		}
	})

	t.Run("exit codes tell a gap from a typo", func(t *testing.T) {
		c.t = t
		c.wantExit(cli.ExitUsage, "cluster", "frobnicate")
		c.wantExit(cli.ExitUsage, "ha", "failback") // --json without --yes or --dry-run
		// A selector that resolves to two nodes is not a promotion target, and
		// that is a precondition rather than a typo.
		c.wantExit(cli.ExitPrecondition, "ha", "promote", "all", "--timeout", "60s")
	})
}

// pair returns the node created as master and the one created as slave, which
// is not the same question as who is master right now.
func (c *csb) pair() (master, standby string) {
	c.t.Helper()
	for _, n := range c.nodes("ha status") {
		name, _ := n["name"].(string)
		switch n["created_role"] {
		case "master":
			master = name
		case "slave":
			standby = name
		}
	}
	if master == "" || standby == "" {
		c.t.Fatalf("could not tell the pair apart: %v", c.nodes("ha status"))
	}
	return master, standby
}

func (c *csb) pingRC(node string) int {
	c.t.Helper()
	d := c.must("node", "exec", node, "--timeout", "60s", "--",
		"ping -c1 -W2 8.8.8.8 >/dev/null 2>&1; echo rc=$?")
	m, _ := d[node].(map[string]any)
	out := fmt.Sprint(m["stdout"])
	var rc int
	fmt.Sscanf(strings.TrimSpace(out), "rc=%d", &rc)
	return rc
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
