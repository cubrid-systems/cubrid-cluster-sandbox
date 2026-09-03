package inspect

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/backend"
	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/topology"
)

// TableDiff is one table on both sides.
type TableDiff struct {
	Table   string `json:"table"`
	Master  int    `json:"master_rows"`
	Standby int    `json:"standby_rows"`
	Same    bool   `json:"same"`
}

// Diff is what a healed split brain needs and no gauge provides.
//
// Every other reader in this package asks the engine how replication is doing.
// This asks the two databases what they contain, because the two questions have
// different answers: after a healed split brain the standby was left permanently
// missing a row while apply_lag read 0, fail_counter read 0, and a canary written
// at that moment arrived (docs/findings/active-active-window.md). Replication was
// not broken and is not now. It simply never carried one row, and the engine
// keeps no view that remembers it.
//
// Row counts are the field's own instrument for this and they are a weak one:
// equal counts are not equal data. They are what can be asked of two databases
// cheaply and without a schema-aware comparison, and saying which is which is
// part of the answer.
type Diff struct {
	Master  string      `json:"master"`
	Standby string      `json:"standby"`
	Tables  []TableDiff `json:"tables"`
	Differ  []string    `json:"differ"`
	Skipped []string    `json:"skipped,omitempty"`
}

// UserTables reads the catalog rather than an error log.
//
// ha resync takes its table list from the applier's failures, which is the right
// source when something failed to apply. A split brain fails nothing -- each side
// wrote its own log and both succeeded -- so that list is empty exactly when the
// divergence is largest. The catalog does not depend on anything having gone
// wrong.
func UserTables(ctx context.Context, d *backend.Docker, t *topology.Topology, node string) ([]string, error) {
	res, err := d.Exec(ctx, node, t.DB,
		"csql -u dba -t -N -c \"SELECT class_name FROM db_class WHERE is_system_class='NO' AND class_type='CLASS' ORDER BY class_name\" "+t.DB+" 2>/dev/null")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("%s: the catalog could not be read", node)
	}
	var out []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || strings.ContainsAny(name, " \t()") {
			continue // csql's own notification lines carry spaces; table names here do not
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func countRows(ctx context.Context, d *backend.Docker, t *topology.Topology, node, table string) (int, error) {
	res, err := d.Exec(ctx, node, t.DB,
		"csql -u dba -t -N -c \"SELECT count(*) FROM ["+table+"]\" "+t.DB+" 2>/dev/null")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		f := strings.TrimSpace(line)
		if f == "" {
			continue
		}
		// Parsed strictly, not scanned out of the middle of a sentence: csql
		// writes a NOTIFICATION line carrying a pid and a port, and a loose match
		// against it answers a question it was never asked.
		if n, cerr := strconv.Atoi(f); cerr == nil {
			return n, nil
		}
	}
	return 0, fmt.Errorf("%s: no row count for %s", node, table)
}

// CompareTables asks both databases what they hold. Tables that exist on only one
// side are a difference too, and are reported rather than skipped.
func CompareTables(ctx context.Context, d *backend.Docker, t *topology.Topology, master, standby string, only []string) (*Diff, error) {
	tables := only
	if len(tables) == 0 {
		var err error
		tables, err = UserTables(ctx, d, t, master)
		if err != nil {
			return nil, err
		}
	}
	out := &Diff{Master: master, Standby: standby}
	for _, name := range tables {
		m, merr := countRows(ctx, d, t, master, name)
		if merr != nil {
			out.Skipped = append(out.Skipped, name+" ("+merr.Error()+")")
			continue
		}
		s, serr := countRows(ctx, d, t, standby, name)
		if serr != nil {
			// Missing on the standby is not a table we failed to read: it is the
			// largest difference there is, so it counts as zero and differs.
			s = 0
		}
		td := TableDiff{Table: name, Master: m, Standby: s, Same: m == s}
		out.Tables = append(out.Tables, td)
		if !td.Same {
			out.Differ = append(out.Differ, name)
		}
	}
	return out, nil
}
