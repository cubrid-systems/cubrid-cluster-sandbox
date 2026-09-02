// Package inspect reads what a cluster is doing.
//
// Tier 1 is the container runtime; tier 2 is the engine's own surfaces --
// cubrid changemode and cubrid heartbeat status, which are one-line stable text,
// and db_ha_apply_info, which is SQL. Tier 3 is a seam and nothing here parses
// statdump (docs/design/05-inspect.md §1).
//
// The rule this package exists to keep: never report a number its source cannot
// support. db_ha_apply_info is written by applylogdb, so it freezes during an
// apply stall, falls during a copy stall, and is absent across a role change --
// so every figure carries where it came from and when it was read, and absence
// is a reason rather than a zero.
package inspect

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/backend"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/topology"
)

type Note struct{ Code, Message string }

// Repl is one node's replication position. Two stages, never one number: copy
// progress and apply progress have separate provenance, and there is no field
// called "delay".
type Repl struct {
	Copied    *int   `json:"copied_pageid"`  // eof_lsa: how far copylogdb fetched
	Applied   *int   `json:"applied_pageid"` // final_lsa: how far applylogdb applied
	ApplyLag  *int   `json:"apply_lag_pages"`
	Fail      *int   `json:"fail_counter"` // separates broken from behind
	Source    string `json:"source"`
	SampledAt string `json:"sampled_at"`
	Rows      int    `json:"rows"`
}

type Node struct {
	Name    string   `json:"name"`
	Live    bool     `json:"live"`
	Role    string   `json:"role"`         // active | standby | to_be_active | maintenance | unknown
	Server  string   `json:"server_state"` // registered_and_*
	Created string   `json:"created_role"` // the role at create time
	Repl    *Repl    `json:"replication,omitempty"`
	Faults  []string `json:"faults,omitempty"`
}

type Status struct {
	Cluster string `json:"cluster"`
	Nodes   []Node `json:"nodes"`
	Notes   []Note `json:"-"`
}

var (
	reServer = regexp.MustCompile(`registered_and_[a-z_]+`)
	reMode   = regexp.MustCompile(`(?i)running mode is ([a-z-]+)`)
)

// Read gathers tier 1 and tier 2 for every node in the topology.
func Read(ctx context.Context, d *backend.Docker, t *topology.Topology) (*Status, error) {
	live := map[string]bool{}
	if states, err := d.Nodes(ctx, t.Cluster); err == nil {
		for _, s := range states {
			live[s.Name] = s.Running
		}
	}

	st := &Status{Cluster: t.Cluster}
	for _, n := range t.Nodes {
		node := Node{Name: n.Name, Created: n.Role, Live: live[n.Name], Role: "unknown"}
		if node.Live {
			if res, err := d.Exec(ctx, n.Name, t.DB, "cubrid heartbeat status 2>/dev/null"); err == nil && res.ExitCode == 0 {
				node.Server = reServer.FindString(res.Stdout)
			}
			if res, err := d.Exec(ctx, n.Name, t.DB, "cubrid changemode "+t.DB+" 2>/dev/null"); err == nil && res.ExitCode == 0 {
				if m := reMode.FindStringSubmatch(res.Stdout); m != nil {
					node.Role = strings.ReplaceAll(strings.ToLower(m[1]), "-", "_")
				}
			}
			node.Repl = readRepl(ctx, d, t, n.Name, &st.Notes)
		}
		st.Nodes = append(st.Nodes, node)
	}
	return st, nil
}

// readRepl reads db_ha_apply_info over SQL. Everything it can go wrong in is a
// note with a code, because each corresponds to something that was measured.
func readRepl(ctx context.Context, d *backend.Docker, t *topology.Topology, node string, notes *[]Note) *Repl {
	res, err := d.Exec(ctx, node, t.DB,
		"csql -u dba -t -N -c 'SELECT eof_lsa_pageid, final_lsa_pageid, fail_counter FROM db_ha_apply_info' "+t.DB+" 2>/dev/null")
	if err != nil || res.ExitCode != 0 {
		return nil
	}
	r := &Repl{Source: "db_ha_apply_info", SampledAt: time.Now().UTC().Format(time.RFC3339)}
	for _, line := range strings.Split(res.Stdout, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) != 3 {
			continue
		}
		eof, e1 := strconv.Atoi(f[0])
		fin, e2 := strconv.Atoi(f[1])
		fail, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		r.Rows++
		if r.Rows == 1 {
			lag := eof - fin
			r.Copied, r.Applied, r.ApplyLag, r.Fail = &eof, &fin, &lag, &fail
		}
	}
	switch {
	case r.Rows == 0:
		// Measured: a just-demoted node has no row at all until its applier
		// writes one, so the check an operator most needs during a role change is
		// empty exactly when they need it.
		*notes = append(*notes, Note{"no_apply_info_row",
			node + " has no db_ha_apply_info row yet; a just-demoted node has none until its applier writes one"})
		return r
	case r.Rows > 1:
		*notes = append(*notes, Note{"ambiguous_apply_info",
			node + " has more than one db_ha_apply_info row; the figures are not attributable to one source"})
	}
	// The copy stage cannot be measured from here. eof_lsa is written by
	// applylogdb, so a stalled applier freezes it at a healthy-looking value and
	// a stalled copier makes the apply lag *fall*. A true copy figure needs the
	// master's append position as a second source, which is M2.2.
	*notes = append(*notes, Note{"no_master_reference",
		node + ": copy progress is not reported, because db_ha_apply_info alone cannot support it"})
	return r
}

// Serving reports whether the cluster has exactly one active node.
func (s *Status) Serving() bool {
	active := 0
	for _, n := range s.Nodes {
		if n.Server == "registered_and_active" {
			active++
		}
	}
	return active == 1
}
