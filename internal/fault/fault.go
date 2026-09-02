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
	Cut       []string `json:"cut,omitempty"` // peers made unreachable from Target
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
