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
	"fmt"
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

	// The second source. Everything above is written by applylogdb, so it cannot
	// report a stall of the process writing it; these two come from the master
	// and are what make the copy stage measurable at all.
	MasterAppend *int   `json:"master_append_pageid,omitempty"`
	CopyLag      *int   `json:"copy_lag_pages,omitempty"`
	RefSource    string `json:"reference_source,omitempty"`
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
	// "Append LSA                     : 171 | 13976"
	reAppend = regexp.MustCompile(`Append LSA\s*:\s*(\d+)\s*\|`)
)

// MasterAppend reads how far the master has written its own log.
//
// This is the second source docs/design/05-inspect.md §3 requires, and it comes
// from `cubrid applyinfo -r`, which is text. The design said the tool would read
// the position itself rather than parse that output; the engine turns out to
// expose it nowhere else -- db_ha_apply_info is the only HA catalog view, and it
// describes the applier rather than the log. So this parses one labelled line,
// `Append LSA`, and nothing else. It does NOT read applyinfo's Estimated Delay,
// which is the field the design's objection was actually about: that one prints
// "-" on its first sample because process_rate is zero until a second iteration.
func MasterAppend(ctx context.Context, d *backend.Docker, t *topology.Topology, from, master string) (int, error) {
	res, err := d.Exec(ctx, from, t.DB, "cubrid applyinfo -r "+master+" "+t.DB+" 2>/dev/null")
	if err != nil {
		return 0, err
	}
	m := reAppend.FindStringSubmatch(res.Stdout)
	if m == nil {
		return 0, fmt.Errorf("no Append LSA in applyinfo -r %s", master)
	}
	return strconv.Atoi(m[1])
}

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
	attachMasterReference(ctx, d, t, st)
	return st, nil
}

// attachMasterReference turns the copy stage from unmeasurable into measured.
// Without it a stalled copier shows a falling or zero apply lag -- the
// reassuring direction -- because the applier keeps draining what is already on
// disk while nothing new arrives.
func attachMasterReference(ctx context.Context, d *backend.Docker, t *topology.Topology, st *Status) {
	master := ""
	for _, n := range st.Nodes {
		if n.Server == "registered_and_active" {
			if master != "" {
				return // two masters: "the master's position" does not name one
			}
			master = n.Name
		}
	}
	if master == "" {
		return
	}
	for i := range st.Nodes {
		n := &st.Nodes[i]
		if n.Name == master || n.Repl == nil || n.Repl.Copied == nil || !n.Live {
			continue
		}
		append_, err := MasterAppend(ctx, d, t, n.Name, master)
		if err != nil {
			st.Notes = append(st.Notes, Note{"no_master_reference",
				n.Name + ": the master's append position could not be read, so copy progress is not reported"})
			continue
		}
		lag := append_ - *n.Repl.Copied
		n.Repl.MasterAppend, n.Repl.CopyLag = &append_, &lag
		n.Repl.RefSource = "applyinfo -r " + master
	}
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
