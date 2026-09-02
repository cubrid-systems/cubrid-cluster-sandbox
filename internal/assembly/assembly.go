// Package assembly carries a cluster from an empty directory to serving.
//
// Every ordering constraint and configuration subtlety in CUBRID's HA assembly
// is this layer's problem rather than the user's (docs/design/03-assembly.md).
// The traps are named where they are handled, because the comment is the only
// thing that will stop someone "simplifying" the handling away.
package assembly

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/backend"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/topology"
)

// The states, in order. Every transition decides on observed state rather than
// on the exit code of the command that was supposed to cause it.
const (
	StateAbsent  = "absent"
	StateDefined = "defined" // config written, containers exist, nothing started
	StateSeeded  = "seeded"  // the slave has a copy of the master's volumes
	StateForming = "forming" // the group is assembling; nobody serves yet
	StateServing = "serving" // queries work; the only state a scenario may start from
)

type Assembler struct {
	D       *backend.Docker
	T       *topology.Topology
	Workdir string
	Log     io.Writer // step narration; nil for silence

	// Forced records that the assembly had to complete a promotion the engine
	// left unfinished, so the caller can say so rather than reporting a clean run.
	Forced bool
}

func (a *Assembler) step(format string, args ...any) {
	if a.Log != nil {
		fmt.Fprintf(a.Log, "   "+format+"\n", args...)
	}
}

func (a *Assembler) nodeDir(node string) string { return filepath.Join(a.Workdir, node) }
func (a *Assembler) dbDir(node string) string   { return filepath.Join(a.nodeDir(node), "db") }
func (a *Assembler) confDir(node string) string {
	return filepath.Join(a.nodeDir(node), "cubrid", "conf")
}

// Master is the node the topology created as master. After a failover the roles
// have swapped; this is the create-time role, which is what the assembly means.
func (a *Assembler) Master() topology.Node { return a.T.Nodes[0] }

// ---- state -------------------------------------------------------------

// State derives where the cluster is from the world rather than from a lock
// file, so an interrupted run resumes instead of needing to be cleaned up.
func (a *Assembler) State(ctx context.Context) (string, error) {
	nodes, err := a.D.Nodes(ctx, a.T.Cluster)
	if err != nil || len(nodes) == 0 {
		return StateAbsent, err
	}
	running := 0
	for _, n := range nodes {
		if n.Running {
			running++
		}
	}
	if running == 0 {
		return StateDefined, nil
	}
	if !a.seeded() {
		return StateDefined, nil
	}
	st, _ := a.serverState(ctx, a.Master().Name)
	switch {
	case st == "registered_and_active":
		return StateServing, nil
	case st != "":
		return StateForming, nil
	}
	return StateSeeded, nil
}

func (a *Assembler) seeded() bool {
	if len(a.T.Nodes) < 2 {
		return true // a single node has nothing to seed
	}
	_, err := os.Stat(filepath.Join(a.dbDir(a.T.Nodes[1].Name), a.T.DB+"_lgat"))
	return err == nil
}

var regState = regexp.MustCompile(`registered_and_[a-z_]+`)

func (a *Assembler) serverState(ctx context.Context, node string) (string, error) {
	res, err := a.D.Exec(ctx, node, a.T.DB, "cubrid heartbeat status 2>/dev/null")
	if err != nil || res.ExitCode != 0 {
		return "", err
	}
	return regState.FindString(res.Stdout), nil
}

// ---- defined: configuration ---------------------------------------------

// engineDirs are linked from the read-only tree into the node's writable
// $CUBRID. Everything the engine ships stays where it is; only what the node
// writes to is local.
var engineDirs = []string{"bin", "lib", "cci", "msg", "locales", "timezones", "share", "include", "3rdparty", "demo", "vm", "java"}

// WriteConfig assembles the node's writable $CUBRID over the read-only tree and
// writes both configuration files. It runs on the host: the node's work
// directory is the container's /work, so nothing here needs a running container.
func (a *Assembler) WriteConfig(node topology.Node, enginePath string) error {
	root := filepath.Join(a.nodeDir(node.Name), "cubrid")
	for _, d := range []string{"conf", "databases", "log", "var", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return err
		}
	}
	// The symlinks point at the container's path for the engine tree, so they
	// dangle on the host and resolve inside the node. That is intended.
	for _, d := range engineDirs {
		if _, err := os.Stat(filepath.Join(enginePath, d)); err != nil {
			continue
		}
		link := filepath.Join(root, d)
		_ = os.Remove(link)
		if err := os.Symlink("/opt/cubrid-ro/"+d, link); err != nil {
			return err
		}
	}

	base, err := os.ReadFile(filepath.Join(enginePath, "conf", "cubrid.conf"))
	if err != nil {
		return fmt.Errorf("the engine tree has no conf/cubrid.conf: %w", err)
	}
	if err := os.WriteFile(filepath.Join(root, "conf", "cubrid.conf"), a.cubridConf(string(base)), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "conf", "cubrid_ha.conf"), a.haConf(), 0o644)
}

// cubridConf takes the engine's own defaults and adds what HA needs.
//
// T7 -- it never writes a [@dbname] section. A per-database ha_mode overrides
// the process-wide parameter and is not restored before the heartbeat starts,
// which makes `cubrid service start` bring up the local server and then decline
// HA with "The server was not configured for HA." Reported upstream, rejected as
// not a product issue, and closed for compatibility: it is current behaviour.
func (a *Assembler) cubridConf(shipped string) []byte {
	var b strings.Builder
	for _, line := range strings.Split(shipped, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "ha_mode") || strings.HasPrefix(t, "cubrid_port_id") {
			continue // set below, from the topology
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n# written by csb\n[common]\nha_mode=on\ncubrid_port_id=31523\n")
	for _, k := range sortedKeys(a.T.Parameters.Common) {
		fmt.Fprintf(&b, "%s=%s\n", k, a.T.Parameters.Common[k])
	}
	return []byte(b.String())
}

// haConf writes cubrid_ha.conf.
//
// T1 -- ha_copy_sync_mode is deliberately absent. It takes one colon-separated
// entry per node in ha_node_list, so a value correct for one node is a hard
// "Invalid Parameter" startup failure for two. Unset, every node defaults to
// sync and the value cannot go out of step with the node count.
func (a *Assembler) haConf() []byte {
	var b strings.Builder
	b.WriteString("# written by csb\n[common]\n")
	fmt.Fprintf(&b, "ha_port_id=59901\n")
	fmt.Fprintf(&b, "ha_node_list=%s\n", a.T.HANodeList())
	fmt.Fprintf(&b, "ha_db_list=%s\n", a.T.DB)
	b.WriteString("ha_apply_max_mem_size=300\nha_copy_log_max_archives=10\n")
	for _, k := range sortedKeys(a.T.Parameters.HA) {
		if k == "ha_copy_sync_mode" {
			continue // T1: never, whatever was asked for
		}
		fmt.Fprintf(&b, "%s=%s\n", k, a.T.Parameters.HA[k])
	}
	for _, k := range sortedKeys(a.T.Parameters.Hidden) {
		fmt.Fprintf(&b, "%s=%s\n", k, a.T.Parameters.Hidden[k])
	}
	return []byte(b.String())
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ---- seeded --------------------------------------------------------------

// CreateDB runs createdb on the master and waits for it.
//
// T6 -- the wait is the point. `databases.txt` appears before createdb has
// finished, and seeding on that signal copies a database with a live transaction
// still in it: the slave's recovery then dies in its UNDO phase on "fetching
// deallocated pageid". Running createdb synchronously and checking its exit
// status is the explicit completion signal that trap asks for.
func (a *Assembler) CreateDB(ctx context.Context) error {
	m := a.Master().Name
	if _, err := os.Stat(filepath.Join(a.dbDir(m), a.T.DB+"_lgat")); err == nil {
		a.step("%s already has %s", m, a.T.DB)
		return nil
	}
	a.step("createdb %s on %s", a.T.DB, m)
	res, err := a.D.Exec(ctx, m, a.T.DB, fmt.Sprintf(
		"cd /db && cubrid createdb --db-volume-size=512M --log-volume-size=256M %s en_US.utf8", a.T.DB))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("createdb exited %d: %s", res.ExitCode, tail(res.Stderr+res.Stdout))
	}
	return nil
}

// Seed copies the master's volumes to every other node.
//
// T3 -- createdb writes volumes as files beside the database name rather than
// into a directory of their own, so the copy is ${DB}*; and the master holds
// ${DB}_lgat__lock, which vanishes mid-copy and breaks the chain, so it is
// excluded rather than copied and deleted afterwards.
func (a *Assembler) Seed(ctx context.Context) error {
	if len(a.T.Nodes) < 2 {
		return nil
	}
	src := a.dbDir(a.Master().Name)
	names, err := filepath.Glob(filepath.Join(src, a.T.DB+"*"))
	if err != nil {
		return err
	}
	names = append(names, filepath.Join(src, "databases.txt"))

	for _, n := range a.T.Nodes[1:] {
		dst := a.dbDir(n.Name)
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		// Seeding is how a slave is built, once. Doing it again on a restart
		// would copy the master's volumes over a database that has been serving
		// and replicating since -- so a node that already has the database is
		// left alone, and rebuilding it is ha resync's decision, not up's.
		if _, err := os.Stat(filepath.Join(dst, a.T.DB+"_lgat")); err == nil {
			a.step("%s already has %s; not re-seeding", n.Name, a.T.DB)
			continue
		}
		copied := 0
		for _, f := range names {
			if strings.HasSuffix(f, "__lock") {
				continue // T3
			}
			if err := copyFile(f, filepath.Join(dst, filepath.Base(f))); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			copied++
		}
		a.step("seeded %s with %d files from %s", n.Name, copied, a.Master().Name)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return nil
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fi.Mode())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ---- forming and serving -------------------------------------------------

// StartHeartbeat starts the heartbeat on every node at once.
//
// T4 -- `cubrid heartbeat start` blocks until the group forms. Run it on one
// node and it waits for a peer that is not starting, so the nodes are started
// concurrently and joined.
//
// T8 -- and its output must go to a file, not to us. The command starts daemons
// (cub_master, cub_server, copylogdb, applylogdb) that inherit the caller's
// stdout and stderr, so a caller that captures them through a pipe waits for a
// pipe the daemons hold open for the life of the cluster: the command has long
// since exited and the call has not returned. Redirecting inside the node hands
// the daemons a file instead, and the log stays for the failure case, which is
// the only case anybody reads it in.
func (a *Assembler) StartHeartbeat(ctx context.Context) error {
	var wg sync.WaitGroup
	errs := make([]error, len(a.T.Nodes))
	for i, n := range a.T.Nodes {
		wg.Add(1)
		go func(i int, node string) {
			defer wg.Done()
			logPath := "/work/" + node + "/heartbeat-start.log"
			_, err := a.D.Exec(ctx, node, a.T.DB,
				"cubrid heartbeat start > "+logPath+" 2>&1; echo rc=$?")
			if err != nil {
				errs[i] = err
			}
		}(i, n.Name)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// StartLog returns what heartbeat start wrote on a node, for the failure path.
func (a *Assembler) StartLog(node string) string {
	b, err := os.ReadFile(filepath.Join(a.nodeDir(node), "heartbeat-start.log"))
	if err != nil {
		return ""
	}
	return tail(string(b))
}

// WaitServing waits for the master to reach registered_and_active.
//
// T5 -- the master is not writable the moment it is up. It passes through
// registered_and_to_be_active, and a write in that window fails with "Attempted
// to update the database when updates are disabled", so the first DDL of any
// scenario fails. Waiting for the observed state is what makes "serving" mean
// something a scenario can start from.
func (a *Assembler) WaitServing(ctx context.Context) (map[string]string, error) {
	deadline := time.Now().Add(90 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d // the caller's --timeout wins; ours is only a floor
	}
	states := map[string]string{}
	stuck := 0
	var blocked error
	for {
		st, _ := a.serverState(ctx, a.Master().Name)
		states[a.Master().Name] = st
		if st == "registered_and_active" {
			break
		}
		// T9 -- a cleanly stopped group does not come back on its own. The
		// promotion to active is requested by applylogdb when it meets the "dead"
		// record copylogdb writes on detecting the peer's death. Stop both nodes
		// gracefully and nobody died, so there is no dead record, nothing to meet,
		// and the node holds registered_and_to_be_active indefinitely -- refusing
		// writes, with a fully caught-up applier. Measured 2026-09-02; it is the
		// same symptom as the field's eight-hour outage by a different route.
		if st == "registered_and_to_be_active" {
			stuck++
			if stuck >= 5 { // ~10 s of it, which is well past the normal transit
				forced, err := a.completePromotion(ctx)
				if err != nil {
					// Not safe *yet* is the usual case: the applier still has a
					// page or two to drain. Keep the reason and keep waiting --
					// it only becomes the answer if the deadline arrives first.
					blocked = err
				} else {
					blocked = nil
				}
				if forced {
					continue
				}
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			if blocked != nil {
				return states, blocked
			}
			return states, fmt.Errorf("master %s did not reach registered_and_active (last: %q)",
				a.Master().Name, st)
		}
		time.Sleep(2 * time.Second)
	}
	for _, n := range a.T.Nodes[1:] {
		st, _ := a.serverState(ctx, n.Name)
		states[n.Name] = st
		if st == "" {
			return states, fmt.Errorf("%s never registered; its start log says: %s", n.Name, a.StartLog(n.Name))
		}
	}
	return states, nil
}

func tail(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, "; ")
}

// Up carries the cluster from wherever it is to serving. It is resumable: each
// step checks what the world already shows before doing anything, so a run that
// died half way is continued rather than cleaned up first.
func (a *Assembler) Up(ctx context.Context) (map[string]string, error) {
	if err := a.CreateDB(ctx); err != nil {
		return nil, err
	}
	if err := a.Seed(ctx); err != nil {
		return nil, err
	}
	a.step("heartbeat start on %d node(s), concurrently", len(a.T.Nodes))
	if err := a.StartHeartbeat(ctx); err != nil {
		return nil, err
	}
	a.step("waiting for %s to reach registered_and_active", a.Master().Name)
	return a.WaitServing(ctx)
}

// Down stops every node gracefully: the server flushes, which is a different
// scenario from a crash and produces different engine behaviour.
func (a *Assembler) Down(ctx context.Context) error {
	for _, n := range a.T.Nodes {
		res, err := a.D.Exec(ctx, n.Name, a.T.DB, "cubrid service stop")
		if err != nil {
			return err
		}
		a.step("%s: %s", n.Name, tail(res.Stdout))
	}
	return nil
}

// applyPosition reads the master's own replication row: how far its copier has
// fetched, how far its applier has applied, and whether replication is broken
// rather than merely behind.
func (a *Assembler) applyPosition(ctx context.Context, node string) (eof, final, fail int, ok bool) {
	res, err := a.D.Exec(ctx, node, a.T.DB,
		"csql -u dba -t -N -c 'SELECT eof_lsa_pageid, final_lsa_pageid, fail_counter FROM db_ha_apply_info' "+a.T.DB+" 2>/dev/null")
	if err != nil || res.ExitCode != 0 {
		return 0, 0, 0, false
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) != 3 {
			continue
		}
		var e, fi, fc int
		if _, err := fmt.Sscan(f[0], &e); err != nil {
			continue
		}
		if _, err := fmt.Sscan(f[1], &fi); err != nil {
			continue
		}
		if _, err := fmt.Sscan(f[2], &fc); err != nil {
			continue
		}
		return e, fi, fc, true
	}
	return 0, 0, 0, false
}

// completePromotion finishes a promotion the engine will not finish on its own
// (T9) -- but only when it can show the move is safe.
//
// Forcing to_be_active to active applies nothing and loses nothing when the
// applier has already drained everything it was sent; forcing it while the
// applier is behind is how data written after the switch gets overwritten by
// replication log arriving late, which is the lab's stated reason for refusing
// to force it in general. So the tool checks first and refuses if it cannot
// prove the case, rather than deciding on the operator's behalf.
func (a *Assembler) completePromotion(ctx context.Context) (bool, error) {
	m := a.Master().Name
	eof, final, fail, ok := a.applyPosition(ctx, m)
	if !ok {
		return false, nil // no row yet; keep waiting rather than guessing
	}
	if fail != 0 || eof != final {
		return false, fmt.Errorf(
			"%s is stuck in to_be_active and completing it is not safe: the applier is at %d of %d with fail_counter=%d. "+
				"Replication has to drain first, or the data written after a forced promotion is what the late log overwrites",
			m, final, eof, fail)
	}
	a.step("%s held to_be_active with its applier drained (%d/%d, fail 0); completing the promotion", m, final, eof)
	a.Forced = true
	res, err := a.D.Exec(ctx, m, a.T.DB, "cubrid changemode -m active -f "+a.T.DB)
	if err != nil {
		return false, err
	}
	if res.ExitCode != 0 {
		return false, fmt.Errorf("changemode -m active -f exited %d: %s", res.ExitCode, tail(res.Stdout+res.Stderr))
	}
	return true, nil
}

// Resolve turns a role selector into node names. "master" is a query answered by
// the engine, not a label read from the artifact: after a failover it names the
// other machine, and a scenario that ran before the failover runs unchanged
// after it (docs/design/01-cli.md §2).
func (a *Assembler) Resolve(ctx context.Context, sel string) ([]string, error) {
	switch sel {
	case "all":
		return a.T.NodeNames(), nil
	case "master", "slave":
		var master, standby []string
		for _, n := range a.T.Nodes {
			st, _ := a.serverState(ctx, n.Name)
			switch st {
			case "registered_and_active", "registered_and_to_be_active":
				master = append(master, n.Name)
			case "registered_and_standby":
				standby = append(standby, n.Name)
			}
		}
		if sel == "master" {
			if len(master) == 0 {
				return nil, fmt.Errorf("no node is active right now")
			}
			if len(master) > 1 {
				return nil, fmt.Errorf("%d nodes are active (%s) -- that is split brain, and \"master\" cannot name one of them",
					len(master), strings.Join(master, ", "))
			}
			return master, nil
		}
		if len(standby) == 0 {
			return nil, fmt.Errorf("no node is standby right now")
		}
		if len(standby) > 1 {
			return nil, fmt.Errorf("%d nodes are standby; use slave[n] to name one", len(standby))
		}
		return standby, nil
	}
	for _, n := range a.T.Nodes {
		if n.Name == sel {
			return []string{n.Name}, nil
		}
	}
	return nil, fmt.Errorf("no node %q in cluster %s", sel, a.T.Cluster)
}
