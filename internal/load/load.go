// Package load drives a workload against a cluster.
//
// A sixth component, and the design did not have it (docs/design/06-load.md).
// Two kinds that must not be conflated -- transactions against the master, and
// contention on the node -- and one contract that decides whether anything
// measured beside it means anything: the driver states a rate, holds it, and
// reports when it could not.
package load

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/backend"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/topology"
)

//go:embed driver.py
var driverPy []byte

// Spec is what was asked for. It travels in describe, because a cluster
// reproducing a bug under 2000 inserts a second is not the same cluster as an
// idle one.
type Spec struct {
	Profile     string  `json:"profile"`
	Rate        float64 `json:"rate,omitempty"` // statements per second; 0 means max
	Concurrency int     `json:"concurrency"`
	ForSeconds  float64 `json:"for_seconds,omitempty"`
	Table       string  `json:"table,omitempty"`
	Seed        int     `json:"seed"`
	Batch       int     `json:"batch,omitempty"`
	DB          string  `json:"db"`
	Node        string  `json:"node"`
	RequireRate bool    `json:"require_rate,omitempty"`

	// KeyLo is where this driver's slice of the key space starts. Several
	// clients writing one table each take a disjoint range and resume inside it,
	// which needs no coordination between them and survives a restart.
	KeyLo int `json:"key_lo,omitempty"`
}

// Status is what happened. achieved sits next to requested always, because a
// driver that quietly under-delivers turns every figure measured beside it into
// a figure about the driver.
type Status struct {
	Profile   string         `json:"profile"`
	Kind      string         `json:"kind"`
	Requested string         `json:"requested"`
	Achieved  string         `json:"achieved"`
	AchievedV float64        `json:"achieved_value"`
	RequestV  float64        `json:"requested_value"`
	Held      bool           `json:"held"`
	Sent      int            `json:"sent"`
	Errors    int            `json:"errors"`
	LastError string         `json:"last_error,omitempty"`
	Elapsed   float64        `json:"elapsed_s"`
	Workers   int            `json:"concurrency"`
	Seed      int            `json:"seed"`
	Batch     int            `json:"batch,omitempty"`
	RowsPerS  *float64       `json:"rows_per_second,omitempty"`
	Running   bool           `json:"running"`
	Cost      map[string]any `json:"driver_cost,omitempty"`
	Node      string         `json:"node,omitempty"`

	// Latency is per STATEMENT, not per row: with --batch one statement carries
	// many rows and the two are different questions. It is absent below twenty
	// samples, because a percentile from three measurements is not a percentile
	// and reporting one would be the same class of lie as a lag figure with no
	// source. LatencyComplete says whether every statement is in it or the cap
	// was reached and the distribution became a sample.
	Latency         *Latency `json:"latency,omitempty"`
	LatencyComplete bool     `json:"latency_complete,omitempty"`
}

type Latency struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50_ms"`
	P90   float64 `json:"p90_ms"`
	P99   float64 `json:"p99_ms"`
	Min   float64 `json:"min_ms"`
	Max   float64 `json:"max_ms"`
}

// Profiles the driver implements. bulkload is deliberately absent: the field has
// a written reproduction of a bulk load outrunning the applier, and it is a
// named case rather than the general driver.
var Profiles = map[string]string{
	"insert":   "db",
	"update":   "db",
	"mixed":    "db",
	"host-cpu": "host",
	"host-io":  "host",
}

type Driver struct {
	D       *backend.Docker
	T       *topology.Topology
	Workdir string
}

func (d *Driver) specPath(node string) string {
	return filepath.Join(d.Workdir, node, "load-spec.json")
}
func (d *Driver) statusPath(node string) string {
	return filepath.Join(d.Workdir, node, "load-status.json")
}

// Start writes the driver and the spec into the node's work directory and
// launches it detached.
//
// Detached, and with its output redirected inside the node: a process that
// outlives the command holds the caller's stdout open, and capturing that
// through a pipe waits for a pipe nothing will close. The assembly paid for that
// once already (docs/design/03-assembly.md, T8).
func (d *Driver) Start(ctx context.Context, s Spec) error {
	kind, ok := Profiles[s.Profile]
	if !ok {
		return fmt.Errorf("unknown profile %q (want %s)", s.Profile, strings.Join(profileNames(), ", "))
	}
	if kind == "host" && s.Rate > 0 {
		return fmt.Errorf("a rate is meaningless for %s: it saturates, and what bounds it is the node's CPU quota", s.Profile)
	}
	nodeDir := filepath.Join(d.Workdir, s.Node)
	if err := os.MkdirAll(nodeDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(nodeDir, "load-driver.py"), driverPy, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(d.specPath(s.Node), b, 0o644); err != nil {
		return err
	}
	_ = os.Remove(d.statusPath(s.Node))

	w := "/work/" + s.Node
	cmd := fmt.Sprintf(
		"setsid nohup python3 %s/load-driver.py %s/load-spec.json %s/load-status.json > %s/load.log 2>&1 < /dev/null & echo $!",
		w, w, w, w)
	res, err := d.D.Exec(ctx, s.Node, d.T.DB, cmd)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("could not start the driver: %s", strings.TrimSpace(res.Stderr))
	}
	// Keep the pid. Stopping by pattern would mean pkill -f, and a pattern
	// matched against full command lines also matches the shell running the
	// pkill -- its own command line contains the pattern. The shell dies first
	// and everything after it in the command never runs.
	pid := strings.TrimSpace(lastLine(res.Stdout))
	if pid != "" {
		_ = os.WriteFile(filepath.Join(nodeDir, "load.pid"), []byte(pid+"\n"), 0o644)
	}
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// Stop signals the driver by pid, and gives it a moment to write its final
// status: the last line of a run is the one that says whether the rate held.
func (d *Driver) Stop(ctx context.Context, node string) error {
	b, err := os.ReadFile(filepath.Join(d.Workdir, node, "load.pid"))
	if err != nil {
		return nil // nothing was started here
	}
	pid := strings.TrimSpace(string(b))
	if pid == "" {
		return nil
	}
	_, err = d.D.Exec(ctx, node, d.T.DB, "kill "+pid+" 2>/dev/null; sleep 2; kill -9 "+pid+" 2>/dev/null; true")
	return err
}

// Status reads what the driver last wrote. It is a file the driver rewrites
// atomically once a second, so reading it never blocks the load.
func (d *Driver) Status(node string) (*Status, error) {
	b, err := os.ReadFile(d.statusPath(node))
	if err != nil {
		return nil, err
	}
	var s Status
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	s.Node = node
	return &s, nil
}

func (d *Driver) Spec(node string) (*Spec, error) {
	b, err := os.ReadFile(d.specPath(node))
	if err != nil {
		return nil, err
	}
	var s Spec
	return &s, json.Unmarshal(b, &s)
}

func profileNames() []string {
	out := make([]string, 0, len(Profiles))
	for k := range Profiles {
		out = append(out, k)
	}
	return out
}
