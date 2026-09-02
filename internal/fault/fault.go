// Package fault injects failures and records what is in force.
//
// Two shapes, and conflating them is a design error: kill is an event -- it
// happens and it is over -- while partition is a condition, entered by one
// command and held until something clears it. A condition needs an owner, a
// reversal and a record (docs/design/04-faults.md §1).
package fault

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/backend"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/topology"
)

// Active is one condition in force. Events are not stored: they are over.
type Active struct {
	Kind      string   `json:"kind"`
	Target    string   `json:"target"`
	Mechanism string   `json:"mechanism,omitempty"`
	Cut       []string `json:"cut,omitempty"`   // peers made unreachable from Target
	Stage     string   `json:"stage,omitempty"` // copy | apply | both
	Pid       string   `json:"pid,omitempty"`   // the suspended process, so clear can resume it
	Delay     string   `json:"delay,omitempty"`
	Mode      string   `json:"mode,omitempty"` // quiesce: RO | SO
	Since     string   `json:"since"`
}

type Set struct {
	path string
	List []Active `json:"faults"`
}

func Open(clusterDir string) (*Set, error) {
	s := &Set{path: filepath.Join(clusterDir, "faults.json")}
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, json.Unmarshal(b, s)
}

func (s *Set) save() error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, append(b, '\n'), 0o644)
}

func (s *Set) add(a Active) error { s.List = append(s.List, a); return s.save() }

// Injector runs the mechanisms.
type Injector struct {
	D *backend.Docker
	T *topology.Topology
}

// Stop is graceful: the server flushes. That is a different scenario from a
// crash and produces different engine behaviour -- a clean stop runs the serial
// cache write-back and a crash does not, which is the pair the CBRD-26983
// verification had to build by hand.
func (i *Injector) Stop(ctx context.Context, node string) error {
	res, err := i.D.Exec(ctx, node, i.T.DB, "cubrid service stop")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 && !strings.Contains(res.Stdout, "success") {
		return fmt.Errorf("%s: service stop exited %d: %s", node, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// Kill is a crash: SIGKILL to the engine's processes, nothing flushed. The
// heartbeat process goes last, because killing it first makes the peer react to
// a node whose server is still running.
func (i *Injector) Kill(ctx context.Context, node string) error {
	// Matched by process NAME, never with pkill -f. A pattern matched against
	// full command lines also matches the shell that is running the pkill --
	// its own command line contains the pattern -- so the shell dies first and
	// nothing after the first pkill ever runs. This project has paid for that
	// once already; the comment is here so it does not pay again.
	_, err := i.D.Exec(ctx, node, i.T.DB,
		"pkill -9 cub_admin; pkill -9 cub_server; pkill -9 cub_master; sleep 1; pgrep cub_master >/dev/null && exit 9; true")
	return err
}

// Start brings a node back. After anything that stopped the heartbeat, a bare
// heartbeat start fails with "CUBRID heartbeat feature is being deactivated" --
// a full service stop/start is required in between (docs/design/03-assembly.md §3).
func (i *Injector) Start(ctx context.Context, node string) error {
	_, _ = i.D.Exec(ctx, node, i.T.DB, "cubrid service stop >/dev/null 2>&1; true")
	logPath := "/work/" + node + "/heartbeat-start.log"
	_, err := i.D.Exec(ctx, node, i.T.DB, "cubrid heartbeat start > "+logPath+" 2>&1; true")
	return err
}

// Partition cuts routes rather than interfaces.
//
// Disconnecting an interface cuts everything, and the entire content of the
// split-brain finding is the difference between a partition where the ping host
// survives and one where it does not. An interface-level cut cannot express
// --keep (docs/design/04-faults.md §3).
func (i *Injector) Partition(ctx context.Context, s *Set, target string, peers []string, mechanism string) error {
	if mechanism == "" {
		mechanism = "blackhole"
	}
	var cut []string
	for _, p := range peers {
		if p == target {
			continue
		}
		ip, err := i.addr(ctx, p)
		if err != nil {
			return err
		}
		if err := i.cut(ctx, target, p, ip, mechanism, false); err != nil {
			return err
		}
		if err := i.cut(ctx, p, target, mustAddr(ctx, i, target), mechanism, false); err != nil {
			return err
		}
		cut = append(cut, p)
	}
	return s.add(Active{Kind: "partition", Target: target, Mechanism: mechanism, Cut: cut,
		Since: time.Now().UTC().Format(time.RFC3339)})
}

// Clear reverses every condition in force, or the ones matching target.
func (i *Injector) Clear(ctx context.Context, s *Set, target string) ([]Active, error) {
	var kept, cleared []Active
	for _, a := range s.List {
		if target != "" && a.Target != target {
			kept = append(kept, a)
			continue
		}
		if a.Kind == "lag" {
			i.clearLag(ctx, a)
		}
		if a.Kind == "partition" {
			for _, p := range a.Cut {
				ip, err := i.addr(ctx, p)
				if err == nil {
					_ = i.cut(ctx, a.Target, p, ip, a.Mechanism, true)
				}
				if tip, err := i.addr(ctx, a.Target); err == nil {
					_ = i.cut(ctx, p, a.Target, tip, a.Mechanism, true)
				}
			}
		}
		cleared = append(cleared, a)
	}
	s.List = kept
	return cleared, s.save()
}

func (i *Injector) addr(ctx context.Context, node string) (string, error) {
	res, err := i.D.R.Run(ctx, "docker", "inspect", "-f",
		"{{(index .NetworkSettings.Networks \""+i.T.Network+"\").IPAddress}}", node)
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(res.Stdout)
	if ip == "" {
		return "", fmt.Errorf("%s has no address on %s", node, i.T.Network)
	}
	return ip, nil
}

func mustAddr(ctx context.Context, i *Injector, node string) string {
	ip, _ := i.addr(ctx, node)
	return ip
}

// cut adds or removes one direction. Route operations need NET_ADMIN and run as
// root inside the node, which is why the container carries the capability.
func (i *Injector) cut(ctx context.Context, from, to, ip, mechanism string, undo bool) error {
	if ip == "" {
		return fmt.Errorf("no address for %s", to)
	}
	var cmd string
	switch mechanism {
	case "drop":
		// The route stays; the packets do not. connect() then hangs and times
		// out, which is a different engine code path from "no route at all".
		verb := "-A"
		if undo {
			verb = "-D"
		}
		cmd = "iptables " + verb + " OUTPUT -d " + ip + " -j DROP"
	default:
		verb := "add"
		if undo {
			verb = "del"
		}
		cmd = "ip route " + verb + " blackhole " + ip
	}
	args := []string{"exec", "-u", "0", from, "sh", "-c", cmd}
	res, err := i.D.R.Run(ctx, "docker", args...)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 && !undo {
		return fmt.Errorf("%s on %s: %s", cmd, from, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// ---- conditions ----------------------------------------------------------

// stagePid finds the pid of one replication stage on a node.
//
// Matched on the process's comm, never with pgrep -f: a pattern matched against
// full command lines also matches the shell running the pgrep, whose own command
// line contains the pattern. Both stages run as cub_admin, so comm alone is not
// enough -- the args say which is which.
func (i *Injector) stagePid(ctx context.Context, node, stage string) (string, error) {
	res, err := i.D.Exec(ctx, node, i.T.DB,
		"ps -eo pid=,comm=,args= | awk '$2==\"cub_admin\" && $0 ~ /"+stage+"logdb/ {print $1; exit}'")
	if err != nil {
		return "", err
	}
	pid := strings.TrimSpace(res.Stdout)
	if pid == "" {
		return "", fmt.Errorf("%s has no %slogdb running; only a standby copies and applies", node, stage)
	}
	return pid, nil
}

// Lag holds a replication stage back.
//
// Stage-targeted because CUBRID's pipeline is two processes that stall
// independently, and the engine's own report makes the same split -- "Delay in
// Copying Active Log" versus "Delay in Applying Copied Log". Suspension is the
// default mechanism because it is the only one that separates them, it is
// instant, it reverses on resume, and the heartbeat does not interfere: it
// watches process existence, not progress (measured, docs/findings/replication-lag.md).
func (i *Injector) Lag(ctx context.Context, s *Set, node, stage, mechanism, delay string) error {
	switch mechanism {
	case "", "suspend":
		pid, err := i.stagePid(ctx, node, stage)
		if err != nil {
			return err
		}
		if _, err := i.D.Exec(ctx, node, i.T.DB, "kill -STOP "+pid); err != nil {
			return err
		}
		return s.add(Active{Kind: "lag", Target: node, Mechanism: "suspend",
			Stage: stage, Pid: pid, Since: time.Now().UTC().Format(time.RFC3339)})
	case "delay":
		if delay == "" {
			delay = "200ms"
		}
		// netem is the realism mechanism and cannot say which stage it slows,
		// which is exactly why it is not the default.
		res, err := i.D.R.Run(ctx, "docker", "exec", "-u", "0", node, "sh", "-c",
			"tc qdisc add dev eth0 root netem delay "+delay)
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("tc on %s: %s", node, strings.TrimSpace(res.Stderr))
		}
		return s.add(Active{Kind: "lag", Target: node, Mechanism: "delay",
			Stage: "both", Delay: delay, Since: time.Now().UTC().Format(time.RFC3339)})
	}
	return fmt.Errorf("unknown mechanism %q (want suspend or delay)", mechanism)
}

// clearLag reverses one lag condition.
func (i *Injector) clearLag(ctx context.Context, a Active) {
	switch a.Mechanism {
	case "suspend":
		if a.Pid != "" {
			_, _ = i.D.Exec(ctx, a.Target, i.T.DB, "kill -CONT "+a.Pid)
		}
	case "delay":
		_, _ = i.D.R.Run(ctx, "docker", "exec", "-u", "0", a.Target, "sh", "-c",
			"tc qdisc del dev eth0 root")
	}
}

// FailCount moves fail_counter deliberately.
//
// The recipe is the field's own, written down in the team's HA study notes:
// create a table, insert rows, THEN add the primary key, then delete the pre-key
// rows on the master. The pre-key rows never replicated, so the applier meets a
// delete for a row it does not have. fail_counter is what separates broken
// replication from slow replication, and until this verb existed the tool had no
// way to move it -- an inspector claim nobody can test is not a claim.
func (i *Injector) FailCount(ctx context.Context, master, table string, rows int) error {
	if table == "" {
		table = "csb_failcount"
	}
	vals := make([]string, 0, rows)
	for n := 1; n <= rows; n++ {
		vals = append(vals, fmt.Sprintf("(%d,'r%d')", n, n))
	}
	steps := []string{
		fmt.Sprintf("DROP TABLE IF EXISTS %s;", table),
		fmt.Sprintf("CREATE TABLE %s (i INT, s VARCHAR(20));", table),
		fmt.Sprintf("INSERT INTO %s VALUES %s;", table, strings.Join(vals, ",")),
		fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT pk_%s PRIMARY KEY (i);", table, table),
		fmt.Sprintf("DELETE FROM %s;", table),
	}
	for _, sql := range steps {
		res, err := i.D.Exec(ctx, master, i.T.DB,
			"csql -u dba -t -N -c \""+sql+"\" "+i.T.DB)
		if err != nil {
			return err
		}
		if res.ExitCode != 0 && !strings.Contains(res.Stdout+res.Stderr, "does not exist") {
			return fmt.Errorf("%s: %s", sql, strings.TrimSpace(tailLine(res.Stdout+res.Stderr)))
		}
	}
	return nil
}

func tailLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// CancelReason reads the engine's own verdict out of a node's master log. Both
// split-brain flavours give two masters and are distinguishable only by this
// line, so an assertion belongs on it rather than on the outcome
// (docs/design/04-faults.md §5).
func (i *Injector) CancelReason(ctx context.Context, node string) (string, error) {
	res, err := i.D.Exec(ctx, node, i.T.DB,
		// `.*` and not a negated class: inside single quotes the shell passes
		// [^\r\n] to grep as "not a backslash, r or n", which truncates the
		// reason at the first r -- and the reason is the assertion.
		"grep -ho '\\[Fail[a-z]*\\] \\[[A-Za-z]*\\].*' /work/"+node+"/cubrid/log/*master.err 2>/dev/null | tail -1")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}

// ---- quiesce -------------------------------------------------------------

// Quiesce blocks writes. It is not a fault -- it is an operational state the
// tool enters on purpose -- but it has every property of a condition: entered,
// held, cleared, visible in status, and carried in describe.
//
// The mechanism is the field's own. Before anyone touches replicated data they
// move the broker's ACCESS_MODE to RO or SO; broker_changer applies it to a
// running broker rather than needing a restart.
func (i *Injector) Quiesce(ctx context.Context, s *Set, nodes []string, mode, broker string) error {
	if mode == "" {
		mode = "ro"
	}
	up := strings.ToUpper(mode)
	if up != "RO" && up != "SO" {
		return fmt.Errorf("unknown --mode %q (want ro or so)", mode)
	}
	for _, n := range nodes {
		res, err := i.D.Exec(ctx, n, i.T.DB, "broker_changer "+broker+" ACCESS_MODE "+up)
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("%s: broker_changer exited %d: %s", n, res.ExitCode,
				strings.TrimSpace(tailLine(res.Stdout+res.Stderr)))
		}
	}
	return s.add(Active{Kind: "quiesce", Target: strings.Join(nodes, ","), Mechanism: "broker",
		Mode: up, Since: time.Now().UTC().Format(time.RFC3339)})
}

// Resume puts the door back. Whoever closed it is not necessarily whoever
// reopens it, which is one of the things the field was asked about and has not
// answered -- so the tool records both events rather than assuming.
func (i *Injector) Resume(ctx context.Context, s *Set, broker string) ([]Active, error) {
	var kept, cleared []Active
	for _, a := range s.List {
		if a.Kind != "quiesce" {
			kept = append(kept, a)
			continue
		}
		for _, n := range strings.Split(a.Target, ",") {
			if n == "" {
				continue
			}
			_, _ = i.D.Exec(ctx, n, i.T.DB, "broker_changer "+broker+" ACCESS_MODE RW")
		}
		cleared = append(cleared, a)
	}
	s.List = kept
	return cleared, s.save()
}

// ---- resync --------------------------------------------------------------

// FailRows reads the applier's error log for the tables it could not apply to.
//
// This is where the field starts too: fail_counter says a number, and
// applylogdb.err says which table and which key. A count with no reason attached
// is the state the field has an open request about.
func (i *Injector) FailRows(ctx context.Context, node string) (map[string]int, error) {
	res, err := i.D.Exec(ctx, node, i.T.DB,
		"grep -ho 'class: \"[^\"]*\"' /work/"+node+"/cubrid/log/*applylogdb*.err 2>/dev/null | sort | uniq -c")
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 2 {
			continue
		}
		var n int
		if _, err := fmt.Sscan(f[0], &n); err != nil {
			continue
		}
		name := strings.Trim(strings.TrimPrefix(strings.Join(f[1:], " "), "class:"), " \"")
		out[name] = n
	}
	return out, nil
}

// CompareTable answers the question the field says it has no way to answer: is
// this fail count a scar, or is the data actually different?
//
// From their own account -- "fail count는 있지만 오류 데이터를 조회하면 문제가 없어
// 엔지니어 판단에 의해 fail count만 초기화하는 경우도 많이 발생" -- a counter with no
// divergence under it is the common case, and today it is found by hand, one key
// at a time. A row count is a coarse comparison and it is not nothing: it
// separates "nothing to repair" from "something to repair" without an engineer
// reading a log.
func (i *Injector) CompareTable(ctx context.Context, master, slave, table string) (int, int, error) {
	count := func(node string) (int, error) {
		res, err := i.D.Exec(ctx, node, i.T.DB,
			"csql -u dba -t -N -c \"SELECT count(*) FROM "+table+"\" "+i.T.DB+" 2>/dev/null")
		if err != nil {
			return 0, err
		}
		for _, line := range strings.Split(res.Stdout, "\n") {
			f := strings.TrimSpace(line)
			if f == "" {
				continue
			}
			var n int
			if _, serr := fmt.Sscan(f, &n); serr == nil {
				return n, nil
			}
		}
		return 0, fmt.Errorf("%s: no count for %s", node, table)
	}
	m, err := count(master)
	if err != nil {
		return 0, 0, err
	}
	s, err := count(slave)
	if err != nil {
		return m, 0, err
	}
	return m, s, nil
}
