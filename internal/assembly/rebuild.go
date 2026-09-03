package assembly

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RebuildSlave rebuilds a standby from an online backup of the master.
//
// This is the engine's own procedure, from `share/scripts/ha/ha_make_slavedb.sh`
// in the build being used, with the ssh and scp taken out because both nodes'
// database directories are on this host. The steps and their order are the
// script's, not this project's invention:
//
//	online backup on the source     cubrid backupdb -z --no-check -C -D <dir>
//	remove the old database         volumes, log, and the replication log dirs
//	move the backup and _bkvinf     scp there, a file copy here
//	restore                         cubrid restoreslave -s master -m <host>
//	initial replication log         cub_admin copylogdb --start-page-id=-1
//	start the node                  and the heartbeat takes it from there
//
// **`restoreslave` is a separate command from `restoredb` and that is the whole
// reason this can work at all.** A plain restore gives a database that is a copy
// of the master; it does not give one that knows where replication should resume.
// `restoreslave` takes the source's state and the master's host name and writes
// the bookkeeping a slave needs, which is exactly the bookkeeping a healed split
// brain leaves wrong (docs/findings/active-active-window.md).
//
// The order matters in one non-obvious place: `<db>_bkvinf` is copied AFTER the
// old database is removed, because it lives among the files that removal deletes.
// The script has the same ordering for the same reason.
func (a *Assembler) RebuildSlave(ctx context.Context, master, slave string) error {
	db := a.T.DB
	backupIn := func(node string) string { return "/work/" + node + "/backup" }
	backupOn := func(node string) string { return filepath.Join(a.nodeDir(node), "backup") }

	a.step("rebuild: stopping %s", slave)
	if _, err := a.D.Exec(ctx, slave, db, "cubrid service stop"); err != nil {
		return fmt.Errorf("%s: service stop: %w", slave, err)
	}

	// 1. Online backup on the master. -C is client/server mode: the master keeps
	//    serving while this runs, which is the point of doing it this way rather
	//    than copying volumes out from under a live server (T6).
	a.step("rebuild: online backup of %s on %s", db, master)
	if err := os.RemoveAll(backupOn(master)); err != nil {
		return err
	}
	if err := os.MkdirAll(backupOn(master), 0o755); err != nil {
		return err
	}
	res, err := a.D.Exec(ctx, master, db, fmt.Sprintf(
		"cubrid backupdb -z --no-check -C -D %s -o %s/backup.output %s@localhost",
		backupIn(master), backupIn(master), db))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("backupdb on %s exited %d: %s", master, res.ExitCode, tail(res.Stdout+res.Stderr))
	}

	// 2. Remove everything of the old database on the slave -- volumes, log, and
	//    the replication log directory -- keeping databases.txt, which is how the
	//    node still knows the database exists.
	a.step("rebuild: removing the old database on %s", slave)
	slaveDB := a.dbDir(slave)
	entries, err := os.ReadDir(slaveDB)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), db) {
			continue // databases.txt and anything else that is not this database
		}
		if err := os.RemoveAll(filepath.Join(slaveDB, e.Name())); err != nil {
			return err
		}
	}

	// 3. The backup, and then _bkvinf -- which restoreslave reads to find the
	//    backup volumes, and which lives among the files just deleted.
	a.step("rebuild: copying the backup to %s", slave)
	if err := os.RemoveAll(backupOn(slave)); err != nil {
		return err
	}
	if err := os.MkdirAll(backupOn(slave), 0o755); err != nil {
		return err
	}
	if err := copyDir(backupOn(master), backupOn(slave)); err != nil {
		return err
	}
	bkv := db + "_bkvinf"
	if err := copyFile(filepath.Join(a.dbDir(master), bkv), filepath.Join(slaveDB, bkv)); err != nil && !os.IsNotExist(err) {
		return err
	}

	// 4. Restore as a slave, told what the source was and who the master is.
	a.step("rebuild: restoreslave on %s", slave)
	res, err = a.D.Exec(ctx, slave, db, fmt.Sprintf(
		"cd /db && cubrid restoreslave -s master -m %s -B %s %s", master, backupIn(slave), db))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("restoreslave on %s exited %d: %s", slave, res.ExitCode, tail(res.Stdout+res.Stderr))
	}

	// 5. Make the initial replication active log, which is the script's
	//    `copy_active_log_from_master` step and is not optional.
	//
	//    Skipping it produced a node where every process reported success and the
	//    group still failed to start: copylogdb registered, applylogdb registered
	//    and then died in the same second, and `cubrid heartbeat start` reported
	//    "HA processes start: fail" with no line saying which one. restoreslave
	//    writes a db_ha_apply_info row pointing at the LSA the backup ended on,
	//    and the applier needs a replication log that already contains that page.
	//    A log the heartbeat's own copylogdb creates from nothing does not.
	//    It runs in the FOREGROUND and is waited for. With --start-page-id=-1
	//    copylogdb is not a daemon: it copies the master's active log and exits,
	//    measured at 6.5 s for a 256 MB log. Backgrounding it and killing it once
	//    the directory looked non-empty produced a log that had a file in it and
	//    not the page the applier wanted, which the engine reports as
	//    "logical log page N may be corrupted" -- a truthful message about a log
	//    this tool truncated.
	a.step("rebuild: copying the master's active log to %s", slave)
	if err := a.CopyActiveLog(ctx, slave, master); err != nil {
		return err
	}

	// 6. Bring the node back. After a service stop a bare heartbeat start fails
	//    with "CUBRID heartbeat feature is being deactivated"; the full cycle is
	//    what gets past it (docs/design/03-assembly.md §3).
	a.step("rebuild: starting %s", slave)
	_, _ = a.D.Exec(ctx, slave, db, "cubrid service stop >/dev/null 2>&1; true")
	logPath := "/work/" + slave + "/heartbeat-start.log"
	if _, err := a.D.Exec(ctx, slave, db, "cubrid heartbeat start > "+logPath+" 2>&1; true"); err != nil {
		return err
	}
	// And its broker, if the cluster has one. `heartbeat start` does not start
	// it, and a rebuilt node that cannot be reached through the door is only
	// half back.
	if a.T.WithBroker {
		_, _ = a.D.Exec(ctx, slave, db,
			"cubrid broker start > /work/"+slave+"/broker-start.log 2>&1; true")
	}
	return nil
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			sub := filepath.Join(dst, e.Name())
			if err := os.MkdirAll(sub, 0o755); err != nil {
				return err
			}
			if err := copyDir(filepath.Join(src, e.Name()), sub); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// CopyActiveLog recopies a node's replication log from the master.
//
// It is a step of the rebuild above, and it is also a repair on its own, because
// the condition it fixes happens without any rebuild: a node whose heartbeat was
// stopped mid-stream can come back with `db_ha_apply_info` recording a position
// its local replication log does not reach. The applier then starts, asks for
// that page, and exits -- reporting
//
//	log applier: invalid replication record. LSA: 192|496 ...
//	Internal error: logical log page 192 may be corrupted.
//
// which is a true statement about a log that is short rather than damaged. Every
// process reports success and `cubrid heartbeat start` reports
// "HA processes start: fail" without naming which one, so the diagnosis has to
// come from the applier's own log.
//
// With --start-page-id=-1 copylogdb is not a daemon: it copies the master's
// active log and exits, measured at about six seconds for a 256 MB log.
func (a *Assembler) CopyActiveLog(ctx context.Context, node, master string) error {
	repl := "/db/" + a.T.DB + "_" + master
	res, err := a.D.Exec(ctx, node, a.T.DB, fmt.Sprintf(
		"rm -rf %s && mkdir -p %s && cub_admin copylogdb -L %s -m async --start-page-id=-1 %s@%s",
		repl, repl, repl, a.T.DB, master))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s: copying the active log exited %d: %s",
			node, res.ExitCode, tail(res.Stdout+res.Stderr))
	}
	return nil
}

// ApplierLogShort reports whether this node's applier refused to start because
// its replication log does not reach the position it was told to resume from.
func (a *Assembler) ApplierLogShort(ctx context.Context, node string) bool {
	res, err := a.D.Exec(ctx, node, a.T.DB,
		"tail -60 $CUBRID/log/*applylogdb*.err 2>/dev/null | grep -c 'may be corrupted'")
	if err != nil {
		return false
	}
	return strings.TrimSpace(res.Stdout) != "" && strings.TrimSpace(res.Stdout) != "0"
}
