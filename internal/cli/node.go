package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/backend"
)

func logsFlags(fs *flag.FlagSet) {
	fs.String("which", "all", "server, master, copylogdb, applylogdb, broker, or all")
	fs.Int("lines", 20, "lines from the end of each file")
	fs.Bool("follow", false, "keep printing as the files grow, until --timeout")
}

// kindOf names the four places CUBRID scatters one node's logs.
//
// This function is the entire reason `--which` exists. A copier's failure lands
// in `<db>_<peer>_copylogdb.err`, an applier's in
// `<db>@localhost_applylogdb_<db>_<peer>.err`, the heartbeat's decisions in
// `<host>_master.err`, and the server's under `server/<db>_<date>.err`. A
// developer reading a failure should not have to know that naming
// (docs/design/01-cli.md §3), so they name the process and the tool finds the
// file.
func kindOf(rel string) string {
	base := filepath.Base(rel)
	dir := filepath.Dir(rel)
	switch {
	case strings.Contains(base, "copylogdb"):
		return "copylogdb"
	case strings.Contains(base, "applylogdb"):
		return "applylogdb"
	case strings.HasSuffix(base, "_master.err"):
		return "master"
	case dir == "server":
		return "server"
	case dir == "broker" || strings.Contains(base, "broker"):
		return "broker"
	}
	return "other"
}

type logFile struct {
	Kind string `json:"kind"`
	Node string `json:"node"`
	Rel  string `json:"file"`
	Path string `json:"-"`
	Size int64  `json:"bytes"`
}

// logFiles lists what a node has written, newest last within a kind.
func logFiles(logDir, node, which string) ([]logFile, error) {
	var out []logFile
	err := filepath.Walk(logDir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil //nolint:nilerr // a missing subdirectory is not an error, it is a node that has not written one yet
		}
		// CUBRID keeps a <db>_latest.err/.access symlink pointing at the current
		// dated file. Following it would print the same log twice, and a stale
		// one points at a file that has been rotated away -- which is how this
		// verb first failed, on a dangling link in a healthy cluster.
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		rel, _ := filepath.Rel(logDir, p)
		k := kindOf(rel)
		if which != "all" && k != which {
			return nil
		}
		if fi.Size() == 0 {
			return nil
		}
		out = append(out, logFile{Kind: k, Node: node, Rel: rel, Path: p, Size: fi.Size()})
		return nil
	})
	sort.Slice(out, func(a, b int) bool {
		if out[a].Kind != out[b].Kind {
			return out[a].Kind < out[b].Kind
		}
		return out[a].Rel < out[b].Rel
	})
	return out, err
}

// tail returns the last n lines of a file and the offset it read to.
func tail(path string, n int) ([]string, int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, int64(len(b)), nil
}

func cmdNodeLogs(c *Ctx) (any, error) {
	sel, err := selectorArg(c)
	if err != nil {
		return nil, err
	}
	// The flag shape is settled before the cluster is touched: a combination
	// that cannot work should not first cost a round of docker inspect.
	which := c.str("which")
	switch which {
	case "all", "server", "master", "copylogdb", "applylogdb", "broker", "other":
	default:
		return nil, Usage("unknown --which %q (server, master, copylogdb, applylogdb, broker, all)", which)
	}
	lines, _ := strconv.Atoi(c.str("lines"))
	follow := c.str("follow") == "true"
	if follow && c.JSON {
		return nil, Usage("--follow streams; it has no envelope to close, so it cannot be combined with --json")
	}

	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	names, rerr := a.Resolve(c.Ctx, sel)
	if rerr != nil {
		return nil, Precondition("unresolved_selector", "%v", rerr)
	}

	var found []logFile
	for _, n := range names {
		dir := filepath.Join(a.Workdir, n, "cubrid", "log")
		fs, err := logFiles(dir, n, which)
		if err != nil {
			return nil, Failed("log_read_failed", "%v", err)
		}
		found = append(found, fs...)
	}
	if len(found) == 0 {
		c.Note("no_logs", SevWarn,
			fmt.Sprintf("no %s log has been written yet under %s", which, filepath.Join(a.Workdir, "<node>", "cubrid", "log")))
		return map[string]any{"files": []logFile{}, "db": t.DB}, nil
	}

	offsets := make([]int64, len(found))
	for i, f := range found {
		ls, end, err := tail(f.Path, lines)
		if err != nil {
			// One unreadable file is a note, not the end of the command: the
			// user asked to read a failure and the other logs may hold it.
			c.Note("log_unreadable", SevWarn, err.Error())
			continue
		}
		offsets[i] = end
		if !c.JSON && !c.Quiet {
			fmt.Fprintf(c.Out, "== %s  %s (%s)\n", f.Node, f.Rel, f.Kind)
			for _, l := range ls {
				fmt.Fprintln(c.Out, l)
			}
		}
	}
	if follow {
		followFiles(c, found, offsets)
	}
	if !c.JSON && !c.Quiet {
		printNotes(c)
	}
	return map[string]any{"files": found, "db": t.DB}, nil
}

// followFiles prints what arrives after the tail, until the context ends.
//
// The bound is the global --timeout rather than a signal, because every other
// verb in this tool is bounded and a developer who has to reach for Ctrl-C to
// stop a command cannot put that command in a script.
func followFiles(c *Ctx, files []logFile, offsets []int64) {
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-c.Ctx.Done():
			return
		case <-tick.C:
			for i, f := range files {
				fh, err := os.Open(f.Path)
				if err != nil {
					continue
				}
				if fi, err := fh.Stat(); err == nil && fi.Size() < offsets[i] {
					offsets[i] = 0 // rotated or truncated: start again rather than seek past the end
				}
				if _, err := fh.Seek(offsets[i], io.SeekStart); err == nil {
					b, _ := io.ReadAll(fh)
					if len(b) > 0 {
						offsets[i] += int64(len(b))
						for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
							fmt.Fprintf(c.Out, "%s %s| %s\n", f.Node, f.Kind, l)
						}
					}
				}
				fh.Close()
			}
		}
	}
}

// cmdNodeShell replaces this process with `docker exec -it`.
//
// A shell is the one verb that cannot answer through the envelope: it hands the
// terminal over and does not come back. Replacing the process rather than
// wrapping it is what gives a real TTY -- job control, signals and line editing
// all come from docker's own stdin instead of from a pipe this tool would be
// sitting in the middle of.
func cmdNodeShell(c *Ctx) (any, error) {
	if c.JSON {
		return nil, Usage("node shell hands the terminal over; there is no envelope for --json to carry")
	}
	sel, err := selectorArg(c)
	if err != nil {
		return nil, err
	}
	a, t, err := loadCluster(c)
	if err != nil {
		return nil, err
	}
	names, rerr := a.Resolve(c.Ctx, sel)
	if rerr != nil {
		return nil, Precondition("unresolved_selector", "%v", rerr)
	}
	if len(names) != 1 {
		return nil, Precondition("ambiguous_selector",
			"node shell needs exactly one node; %q resolved to %d", sel, len(names))
	}
	bin, err := exec.LookPath("docker")
	if err != nil {
		return nil, Failed("no_docker", "%v", err)
	}
	argv := []string{"docker", "exec", "-it"}
	for _, e := range backend.NodeEnv(names[0], t.DB) {
		argv = append(argv, "-e", e)
	}
	argv = append(argv, names[0], "bash", "-l")
	// Nothing after this line runs on success.
	return nil, Failed("exec_failed", "%v", syscall.Exec(bin, argv, os.Environ()))
}
