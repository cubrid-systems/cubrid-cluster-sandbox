package record

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The engine's own words. Assertions belong on these lines rather than on the
// outcome: the two split-brain flavours are indistinguishable by outcome and
// distinguishable only by the cancel reason (docs/design/04-faults.md §5).
var reHA = regexp.MustCompile(`\[(Failover|Failback)\]\s+\[([A-Za-z]+)\][^\r\n]*`)

// engineTime parses the engine's log timestamp: "Time: 09/02/26 09:04:47.270".
var reTime = regexp.MustCompile(`Time:\s+(\d\d)/(\d\d)/(\d\d)\s+(\d\d):(\d\d):(\d\d)\.(\d+)`)

// offsets remembers how far each log file has been read, so harvesting twice
// does not record the same line twice.
type offsets map[string]int64

func (r *Record) offsetPath() string {
	return filepath.Join(filepath.Dir(r.path), "harvest-offsets.json")
}

func (r *Record) loadOffsets() offsets {
	o := offsets{}
	if b, err := os.ReadFile(r.offsetPath()); err == nil {
		_ = json.Unmarshal(b, &o)
	}
	return o
}

func (r *Record) saveOffsets(o offsets) {
	if b, err := json.MarshalIndent(o, "", "  "); err == nil {
		_ = os.WriteFile(r.offsetPath(), b, 0o644)
	}
}

// Harvest reads every node's master log for HA lines written since the last
// harvest and appends them to the timeline as engine events.
//
// The tool's own entries and the engine's are kept apart by `actor`, because
// "the tool cut the route at 07:12:00" and "the engine logged a failover at
// 07:12:09" are different classes of fact and the interval between them is the
// measurement (docs/design/07-record.md §3).
func (r *Record) Harvest(workdir string, nodes []string) (int, error) {
	off := r.loadOffsets()
	found := 0
	for _, node := range nodes {
		logDir := filepath.Join(workdir, node, "cubrid", "log")
		entries, err := os.ReadDir(logDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), "_master.err") {
				continue
			}
			p := filepath.Join(logDir, e.Name())
			n, err := r.harvestFile(p, e.Name(), node, off)
			if err != nil {
				return found, err
			}
			found += n
		}
	}
	r.saveOffsets(off)
	return found, nil
}

func (r *Record) harvestFile(path, base, node string, off offsets) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil
	}
	defer f.Close()

	start := off[path]
	if fi, err := f.Stat(); err == nil && fi.Size() < start {
		start = 0 // the file was rotated or truncated; read it again
	}
	if _, err := f.Seek(start, 0); err != nil {
		return 0, err
	}

	var (
		sc     = bufio.NewScanner(f)
		read   = start
		lineNo = 0
		lastTS time.Time
		found  int
	)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		read += int64(len(line)) + 1
		lineNo++
		if m := reTime.FindStringSubmatch(line); m != nil {
			lastTS = parseEngineTime(m)
		}
		hit := reHA.FindStringSubmatch(line)
		if hit == nil {
			continue
		}
		t := lastTS
		if t.IsZero() {
			t = time.Now()
		}
		detail := map[string]any{
			"node":   node,
			"kind":   hit[1], // Failover | Failback
			"result": hit[2], // Success | Cancelled | Diagnosis | ...
			"source": fmt.Sprintf("%s:%d", base, lineNo),
			"line":   strings.TrimSpace(hit[0]),
		}
		if err := r.appendAt(t, ActorEngine, "ha."+strings.ToLower(hit[1]), detail); err != nil {
			return found, err
		}
		found++
	}
	off[path] = read
	return found, sc.Err()
}

// parseEngineTime reads the engine's MM/DD/YY local timestamp. The year is
// two-digit, so this is only meaningful for the century the code runs in --
// which is the same limitation the log itself has.
func parseEngineTime(m []string) time.Time {
	// strconv, not fmt.Sscan: the engine zero-pads, and Sscan reads "09" as
	// octal, fails at the 9, and leaves 0 behind -- which time.Date normalises
	// into December of the previous year. The whole record is timestamps; a
	// silent nine-month error in them is not a small bug.
	mo := atoi(m[1])
	d := atoi(m[2])
	y := atoi(m[3])
	h := atoi(m[4])
	mi := atoi(m[5])
	sec := atoi(m[6])
	frac := m[7]
	for len(frac) < 9 {
		frac += "0"
	}
	// UTC, not the host's zone: the timestamp comes from inside a node, whose
	// clock zone the backend pins to UTC. Reading it as local time shifted every
	// engine line by the host's offset -- nine hours, in the run that found it.
	return time.Date(2000+y, time.Month(mo), d, h, mi, sec, atoi(frac[:9]), time.UTC)
}

func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimLeft(s, "0"))
	if err != nil || strings.TrimLeft(s, "0") == "" {
		if strings.Trim(s, "0") == "" {
			return 0
		}
		return 0
	}
	return n
}
