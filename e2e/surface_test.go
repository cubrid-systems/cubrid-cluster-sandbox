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
	// The globals go BEFORE a bare `--`, because everything after it belongs to
	// the program running on the node rather than to csb.
	globals := []string{"--json", "--cluster", c.cluster}
	full := append([]string{}, args...)
	if i := indexOf(full, "--"); i >= 0 {
		full = append(append(append([]string{}, full[:i]...), globals...), full[i:]...)
	} else {
		full = append(full, globals...)
	}
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

// runNoCluster is for a verb that makes its own cluster, so the suite must not
// hand it one.
func (c *csb) runNoCluster(args ...string) (*cli.Envelope, int) {
	c.t.Helper()
	full := append(append([]string{}, args...), "--json")
	cmd := exec.Command(c.bin, full...)
	cmd.Env = append(os.Environ(), "CSB_HOME="+c.home)
	out, err := cmd.Output()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	var e cli.Envelope
	if jerr := json.Unmarshal(out, &e); jerr != nil {
		c.t.Fatalf("csb %s produced no envelope: %v\n%s", strings.Join(args, " "), jerr, out)
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

// currentPair is who is serving RIGHT NOW, which is not the create-time pair
// once anything in this suite has moved the roles.
func (c *csb) currentPair() (active, standby string) {
	c.t.Helper()
	for n, st := range c.roles() {
		switch st {
		case "registered_and_active":
			active = n
		case "registered_and_standby":
			standby = n
		}
	}
	return active, standby
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

	// The tools directory belongs to the whole run: a subtest's TempDir is
	// removed when that subtest ends, and the client would then be mounting a
	// directory that no longer exists.
	tools := filepath.Join(home, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "e2e.sql"), []byte("SELECT 1 FROM db_root;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The real example, from the repository. The suite's traffic comes from the
	// same file a newcomer runs, so a change that breaks the example breaks the
	// suite rather than being discovered by them.
	example, err := os.ReadFile("../examples/load-client/example.sh")
	if err != nil {
		t.Fatalf("the example loader is missing: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tools, "example.sh"), example, 0o755); err != nil {
		t.Fatal(err)
	}

	// The cluster is destroyed on success and KEPT on failure. Leaving containers
	// behind costs the next run; throwing away the only copy of a cluster that
	// just failed costs more, and this suite exists to find things that are hard
	// to reproduce. Set CSB_E2E_KEEP=0 to destroy either way.
	defer func() {
		c.t = t // subtests moved it; the cleanup belongs to the outer test
		if t.Failed() && os.Getenv("CSB_E2E_KEEP") != "0" {
			t.Logf("KEEPING cluster %s under %s for inspection; csb cluster destroy --cluster %s --purge",
				c.cluster, c.home, c.cluster)
			return
		}
		if _, code := c.run("cluster", "destroy", "--purge", "--timeout", "300s"); code != cli.ExitOK {
			t.Errorf("destroy exited %d", code)
		}
	}()

	t.Run("create", func(t *testing.T) {
		c.t = t
		// Two clients, because the client surface is part of the tool now and a
		// cluster that has one behaves differently from a cluster that does not:
		// `load` runs somewhere else, and the broker is reachable from inside.
		d := c.must("cluster", "create", "--name", c.cluster, "--build", build,
			"--with-broker", "--clients", "2", "--tools", tools,
			"--timeout", "900s")
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

	t.Run("diff compares the databases, not the gauges", func(t *testing.T) {
		c.t = t
		// On a healthy pair this must find nothing and exit 0. The interesting
		// case -- a healed split brain where every gauge reads healthy and the
		// standby is missing a row -- is measured in
		// harness/calc-score-window.sh, because it needs writes on both sides of
		// a partition. What is guarded here is the catalog query underneath it:
		// if db_class ever stops answering, this verb silently compares nothing
		// and reports agreement, which is the worst thing it could do.
		d := c.must("repl", "diff", "--timeout", "120s")
		tables, _ := d["tables"].([]any)
		if len(tables) == 0 {
			t.Fatalf("repl diff found no user table to compare: %v", d)
		}
		if differ, _ := d["differ"].([]any); len(differ) != 0 {
			t.Errorf("a healthy pair reported a divergence: %v", differ)
		}
	})

	t.Run("watch sees the stage that is stalled, not the one that says so", func(t *testing.T) {
		c.t = t
		// A batch of fifty per statement, because the copy stage is measured in
		// log PAGES and single-row inserts barely move one.
		master, _ := c.currentPair()
		c.startTraffic(master)
		defer c.stopTraffic()
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

	t.Run("a cleanly stopped group comes back", func(t *testing.T) {
		c.t = t
		was, _ := c.currentPair()
		// It runs BEFORE the partition subtest on purpose. Run after one, it
		// failed for reasons that had nothing to do with a clean stop: a group
		// that has just healed a split brain sometimes needs a forced promotion
		// to come back, and sometimes leaves the original master unregistered.
		// That is worth knowing and it is a different question from this one.
		//
		// This is here because of what it once said. For a while this project
		// reported that `down` then `up` left the original master stuck in
		// registered_and_to_be_active; ten runs across five conditions never
		// reproduced it and the claim was withdrawn without a cause. An
		// unexplained observation deserves a standing check, not a paragraph.
		t.Logf("roles before down: %v", c.roles())
		c.must("cluster", "down", "--timeout", "300s")
		d := c.must("cluster", "up", "--timeout", "600s")
		if d["state"] != "serving" {
			t.Fatalf("up reached %v, not serving", d["state"])
		}
		if d["promotion_forced"] == true {
			t.Errorf("the group needed a forced promotion to come back: %v", d)
		}
		if m := mastersOf(c.roles()); len(m) != 1 || m[0] != was {
			t.Errorf("after down/up the master is %v, want [%s] — the node that was serving before it", m, was)
		}
	})

	t.Run("a dropped-packet partition leaves the route in place", func(t *testing.T) {
		c.t = t
		c.exec(master, "csql -u dba -c \"CREATE TABLE csb_e2e_split (i INT PRIMARY KEY)\" "+c.cluster)
		c.exec(master, "csql -u dba -c \"INSERT INTO csb_e2e_split VALUES (1)\" "+c.cluster)

		c.must("fault", "partition", standby, "--mechanism", "drop", "--timeout", "60s")
		c.until("two masters", 90*time.Second, func() bool {
			return len(mastersOf(c.roles())) == 2
		})
		// One row per side while neither can see the other. What the promoted
		// standby writes comes back to the master on the heal; what the master
		// writes never reaches the standby, and no gauge afterwards says so
		// (docs/findings/active-active-window.md).
		bothWrote := c.exec(master, "csql -u dba -c \"INSERT INTO csb_e2e_split VALUES (101)\" "+c.cluster) == 0 &&
			c.exec(standby, "csql -u dba -c \"INSERT INTO csb_e2e_split VALUES (201)\" "+c.cluster) == 0

		c.must("fault", "clear", "--timeout", "60s")
		// One master is not enough to compare on: the other node can still be in
		// to_be_active, and `repl diff` needs a settled pair rather than a
		// settling one. Asking too early exits 3, which is the verb being right.
		c.until("a settled pair", 180*time.Second, func() bool {
			r := c.roles()
			active, standby := 0, 0
			for _, st := range r {
				switch st {
				case "registered_and_active":
					active++
				case "registered_and_standby":
					standby++
				}
			}
			return active == 1 && standby == 1
		})
		if !bothWrote {
			t.Log("one side refused its write, so there is no divergence to repair here")
			return
		}

		// The divergence, and then the rebuild that is the only thing that closes
		// it. This is the most destructive verb in the tool and it runs every
		// time the suite does.
		if _, code := c.run("repl", "diff", "--timeout", "120s"); code != cli.ExitFailed {
			t.Fatalf("a healed split brain reported no divergence (exit %d)", code)
		}
		d := c.must("ha", "resync", "--path", "slave", "--timeout", "900s")
		t.Logf("rebuilt %v from %v; roles now %v", d["slave"], d["master"], c.roles())
		if after, _ := d["diverged_after"].([]any); len(after) != 0 {
			t.Fatalf("the rebuild left %v still differing", after)
		}
		if _, code := c.run("repl", "diff", "--timeout", "120s"); code != cli.ExitOK {
			t.Errorf("repl diff still reports a divergence after the rebuild (exit %d)", code)
		}

	})

	t.Run("promote makes the heartbeat decide", func(t *testing.T) {
		c.t = t
		// Whoever is standby now, not whoever was created as one: the roles have
		// moved twice by this point in the suite.
		_, standby := c.currentPair()
		if standby == "" {
			t.Fatalf("no standby to promote (roles: %v)", c.roles())
		}
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
		d2 := c.must("ha", "promote", standby, "--timeout", "120s")
		if d2["changed"] != false {
			t.Errorf("promoting the master again reported a change: %v", d2)
		}
	})

	t.Run("failback returns service and proves replication", func(t *testing.T) {
		c.t = t
		// With traffic, because an idle cluster is a different system here: an
		// applier one page short of drained stays one page short, and the
		// promotion this verb performs will not complete until it drains.
		serving0, _ := c.currentPair()
		c.startTraffic(serving0)
		defer c.stopTraffic()

		// promote left the previous master out of the group; whichever node that
		// was, it is the one service has to return to.
		// A DB node, not a client: clients have no role and would never come
		// "back in the group", which is what this waited three minutes for.
		serving, _ := c.currentPair()
		var away string
		for n, st := range c.roles() {
			if n != serving && st != "" {
				away = n
			}
		}
		if away == "" {
			for _, n := range c.nodes("ha status") {
				name, _ := n["name"].(string)
				if role, _ := n["created_role"].(string); role != "" && name != serving {
					away = name
				}
			}
		}
		// Putting a node back after a role change is a REBUILD, not a restart,
		// and that is the field's answer rather than this project's: the rejoin
		// path in their tracker is ha_make_slavedb.sh. `node start` tries the
		// cheap repair first and says so when it is not enough
		// (docs/design/03-assembly.md §3).
		if _, code := c.run("node", "start", away, "--timeout", "300s"); code != cli.ExitOK {
			t.Logf("%s could not be restarted into the group; rebuilding it, which is what the field does", away)
			c.must("ha", "resync", away, "--path", "slave", "--timeout", "900s")
		}
		c.until(away+" back in the group", 180*time.Second, func() bool {
			return strings.HasPrefix(c.roles()[away], "registered_and_")
		})

		// --dry-run changes nothing, and says so.
		d := c.must("ha", "failback", "--to", away, "--dry-run", "--timeout", "120s")
		if d["changed"] != false {
			t.Fatalf("--dry-run reported a change: %v", d)
		}
		if c.roles()[away] == "registered_and_active" {
			t.Fatalf("--dry-run moved the roles: %v", c.roles())
		}

		e, code := c.run("ha", "failback", "--to", away, "--yes", "--quiesce", "--timeout", "400s")
		if code != cli.ExitOK {
			t.Fatalf("failback exited %d: %s", code, notes(e))
		}
		fb, _ := e.Data.(map[string]any)
		if fb["to"] != away {
			t.Fatalf("service went to %v, want %s", fb["to"], away)
		}
		// Roles alone say the group agrees, not that replication carries
		// anything, so the verb has to have proved it with a write.
		if _, ok := fb["canary_seconds"].(float64); !ok {
			t.Errorf("failback did not verify replication: %v", fb)
		}
		if c.roles()[away] != "registered_and_active" {
			t.Errorf("roles after failback: %v", c.roles())
		}
	})

	t.Run("a client node has two spaces and reaches the broker", func(t *testing.T) {
		c.t = t
		client := c.cluster + "-c1"
		// /tools is the user's and read-only: a run must not be able to damage
		// the scripts it was given.
		if code := c.exec(client, "test -r /tools/e2e.sql"); code != 0 {
			t.Errorf("the tools directory is not readable from the client")
		}
		if code := c.exec(client, "touch /tools/nope"); code == 0 {
			t.Errorf("/tools accepted a write; it is supposed to be read-only")
		}
		// /results is ours and writable, and it outlives the cluster.
		if code := c.exec(client, "touch /results/e2e-was-here"); code != 0 {
			t.Errorf("/results refused a write")
		}
		// The broker, from inside, with no port published on the host. This is
		// the path JDBC and CCI use.
		master, _ := c.currentPair()
		// The statement comes from /tools rather than the command line, which
		// keeps a quoted SQL string out of a helper that splits on whitespace
		// and proves the two mounts work together. dba has no password, so the
		// empty one is the correct one -- passing anything else fails the
		// connection and says nothing about reachability.
		if code := c.exec(client, "broker_tester "+master+":33000 -D "+c.cluster+" -u dba -p '' -i /tools/e2e.sql"); code != 0 {
			t.Errorf("the client could not reach the broker at %s:33000", master)
		}
	})

	t.Run("contention is a condition, not a workload", func(t *testing.T) {
		c.t = t
		// It used to be `load --profile host-cpu`, which was the wrong noun: it
		// is held until cleared and it belongs in `fault ls`.
		master, _ := c.currentPair()
		c.must("fault", "contend", master, "--kind", "cpu", "--workers", "2", "--timeout", "60s")
		d := c.must("fault", "ls", "--timeout", "30s")
		if !strings.Contains(fmt.Sprint(d), "contention") {
			t.Errorf("fault ls does not show the contention: %v", d)
		}
		c.must("fault", "clear", master, "--timeout", "60s")
	})

	t.Run("the CTP fragment carries the pair and refuses a key it does not know", func(t *testing.T) {
		c.t = t
		out := filepath.Join(c.home, "ha_repl.conf")
		c.must("cluster", "describe", "--format", "ctp", "--out", out, "--timeout", "60s")
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("no fragment written: %v", err)
		}
		frag := string(b)
		for _, want := range []string{
			"env." + c.cluster + ".master.ssh.host=",
			"env." + c.cluster + ".slave.ssh.host=",
			"CONTAINER NAMES",     // the transport is named rather than implied
			"cubrid_download_url", // and so is what does not apply
		} {
			if !strings.Contains(frag, want) {
				t.Errorf("the fragment does not carry %q", want)
			}
		}
		// A client node is not part of the HA group and must not appear as one.
		if strings.Contains(frag, c.cluster+"-c1") {
			t.Errorf("a client node was written into an ha_repl conf")
		}
		// And validation is kept on the way in.
		bad := filepath.Join(c.home, "bad.conf")
		if err := os.WriteFile(bad, []byte("env.i1.ha.ha_no_such_thing=1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		c.wantExit(cli.ExitUsage, "cluster", "create", "--name", "nope", "--from-ctp", bad, "--build", build, "--timeout", "30s")
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
		// The page is the same document rendered, and it has to survive being
		// opened on a machine with no network.
		page := filepath.Join(c.home, "run.html")
		c.must("record", "export", "--out", page, "--timeout", "60s")
		b, rerr := os.ReadFile(page)
		if rerr != nil {
			t.Fatalf("no page written: %v", rerr)
		}
		html := string(b)
		if !strings.Contains(html, c.cluster) || !strings.Contains(html, "ha.") {
			t.Errorf("the page carries neither the cluster nor an engine event")
		}
		for _, forbidden := range []string{"http://", "https://", "<script"} {
			if strings.Contains(html, forbidden) {
				t.Errorf("the page reaches outside itself: %q", forbidden)
			}
		}
	})

	t.Run("a tailnet cluster addresses itself there and can still be cut", func(t *testing.T) {
		c.t = t
		key := os.Getenv("CSB_TS_AUTHKEY")
		if key == "" {
			t.Skip("set CSB_TS_AUTHKEY to exercise the tailnet option; it joins real machines to a real tailnet")
		}
		// Its own cluster, and destroyed at the end whatever happens, because
		// these nodes become devices in somebody's tailnet.
		name := c.cluster + "ts"
		defer func() {
			e, _ := c.runNoCluster("cluster", "destroy", "--cluster", name, "--purge", "--timeout", "300s")
			if hasNote(e, "tailnet_devices_remain") {
				t.Logf("tailnet devices remain and need removing from the admin console; use an ephemeral key")
			}
		}()
		if e, code := c.runNoCluster("cluster", "create", "--name", name, "--network", "tailnet",
			"--build", build, "--timeout", "900s"); code != cli.ExitOK {
			t.Fatalf("tailnet create failed: %s", notes(e))
		}
		ts := &csb{t: t, bin: c.bin, home: c.home, cluster: name}
		// The names have to mean the tailnet, or the cluster keeps talking over
		// the bridge while believing otherwise and every cut becomes a no-op.
		d := ts.must("node", "exec", name+"-n2", "--timeout", "60s", "--",
			"ss", "-tn", "state", "established")
		m, _ := d[name+"-n2"].(map[string]any)
		if !strings.Contains(fmt.Sprint(m["stdout"]), "100.") {
			t.Errorf("no connection is on a tailnet address:\n%v", m["stdout"])
		}
		// And the cut has to bite. On a tailnet a route in the main table does
		// not: tailscale sends 100.64/10 to its own table at a lower priority.
		ts.must("fault", "partition", name+"-n2", "--timeout", "60s")
		ts.until("two masters on the tailnet", 120*time.Second, func() bool {
			return len(mastersOf(ts.roles())) == 2
		})
		ts.must("fault", "clear", "--timeout", "60s")
	})

	t.Run("a scenario runs against a build and judges it", func(t *testing.T) {
		c.t = t
		// Its own cluster, because that is what a scenario does: it stands one
		// up, walks the steps, and destroys it. Kept small -- what is under test
		// is the runner, not the engine.
		file := filepath.Join(c.home, "smoke.json")
		body := `{
  "name": "the pair serves and replication carries a write",
  "cluster": { "preset": "ha" },
  "steps": [
    { "await": { "masters": 1, "standbys": 1 }, "within": "120s" },
    { "run": ["repl", "check", "--wait", "60s"] },
    { "note": "and a verb that should refuse", "run": ["ha", "promote", "all"], "expect_exit": 3 }
  ]
}`
		if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		e, code := c.runNoCluster("scenario", "run", file, "--build", build, "--timeout", "900s")
		if code != cli.ExitOK {
			t.Fatalf("the scenario failed: %s", notes(e))
		}
		d, _ := e.Data.(map[string]any)
		if d["passed"] != true {
			t.Errorf("scenario data says it did not pass: %v", d)
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

// exec runs one command on a node and returns its exit status, which is how the
// suite tells "the node accepted this write" from "it refused".
func (c *csb) exec(node, command string) int {
	c.t.Helper()
	e, _ := c.run(append([]string{"node", "exec", node, "--timeout", "60s", "--"}, strings.Fields(command)...)...)
	d, _ := e.Data.(map[string]any)
	m, _ := d[node].(map[string]any)
	code, _ := m["exit"].(float64)
	return int(code)
}

// startTraffic and stopTraffic bracket the subtests that need a cluster which is
// not idle. The old built-in driver could be stopped precisely; a program on a
// client cannot, so the suite has to do it -- and it has to, because traffic
// changes what the fault verbs mean. A row-count comparison under writes reports
// a difference that is only lag, and a promotion on an idle cluster can wait
// forever for an applier that is one page short with nothing to push it.
func (c *csb) startTraffic(master string) {
	c.t.Helper()
	c.exec("client[1]", "setsid nohup sh /tools/example.sh "+c.cluster+" "+master+" 1000000 20 50 >/dev/null 2>&1 &")
	time.Sleep(5 * time.Second)
}

func (c *csb) stopTraffic() {
	c.t.Helper()
	c.exec("client[1]", "pkill -f example.sh; pkill -x csql; true")
	time.Sleep(2 * time.Second)
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

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
