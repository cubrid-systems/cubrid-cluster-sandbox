// Package record is the evidence artifact: what happened to a cluster, as an
// append-only timeline. There is no "record start" -- every command that changes
// cluster state appends, from cluster create onward, because a record a user has
// to remember to switch on is a record that is missing from the run that
// mattered (docs/design/07-record.md §2).
package record

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Actor separates the tool from the engine. "The tool cut the route at 07:12:00"
// and "the engine logged a failover at 07:12:09" are different classes of fact,
// and the interval between them is the measurement.
const (
	ActorTool   = "tool"
	ActorEngine = "engine"
	ActorLoad   = "load"
)

type Entry struct {
	T      string         `json:"t"`
	Actor  string         `json:"actor"`
	Event  string         `json:"event"`
	Detail map[string]any `json:"detail,omitempty"`
}

type Record struct{ path string }

func Open(path string) *Record { return &Record{path: path} }

// Append adds one entry. It creates the file and its directory on first write,
// so the first state-changing command opens the record without being asked.
func (r *Record) Append(actor, event string, detail map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	e := Entry{T: time.Now().UTC().Format(time.RFC3339Nano), Actor: actor, Event: event, Detail: detail}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// Read returns the timeline, oldest first. A zero since returns everything.
func (r *Record) Read(since time.Time) ([]Entry, error) {
	f, err := os.Open(r.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue // a truncated final line is not a reason to lose the rest
		}
		if !since.IsZero() {
			t, perr := time.Parse(time.RFC3339Nano, e.T)
			if perr == nil && t.Before(since) {
				continue
			}
		}
		out = append(out, e)
	}
	return out, sc.Err()
}
