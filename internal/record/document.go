package record

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// RoleChange pairs what the engine did with what the settings in force predict.
// Putting the two numbers side by side is the whole point: the field measured a
// role change at 8-11 s against an arithmetic 2.5 s, three times, and could not
// say which of three explanations it was looking at because nothing recorded
// enough to separate them (docs/design/07-record.md §1).
type RoleChange struct {
	Node      string         `json:"node"`
	Kind      string         `json:"kind"`   // Failover | Failback
	Result    string         `json:"result"` // Success | Cancelled | Diagnosis
	At        string         `json:"at"`
	Trigger   string         `json:"trigger,omitempty"`  // the tool event it is measured from
	Measured  string         `json:"measured,omitempty"` // trigger -> the engine's own line
	Predicted string         `json:"predicted"`          // arithmetic from the settings
	DecidedBy map[string]any `json:"decided_by"`
	Line      string         `json:"line"`
	Source    string         `json:"source"`
}

type Validity struct {
	Valid   bool     `json:"valid"`
	Reasons []string `json:"reasons"`
}

type Document struct {
	Schema      string       `json:"schema"`
	Cluster     string       `json:"cluster"`
	Opened      string       `json:"opened"`
	Describe    any          `json:"describe"`
	Timeline    []Entry      `json:"timeline"`
	RoleChanges []RoleChange `json:"role_changes"`
	Validity    Validity     `json:"validity"`
}

// Heartbeat settings that decide when a role change happens. The defaults are
// the engine's, restated by the lab to a customer in 2023; none of the three is
// in paramdump, which is why the topology carries them as hidden parameters.
const (
	defHeartbeatInterval = 500  // ms
	defMaxHeartbeatGap   = 5    // consecutive misses
	defCalcScoreInterval = 3000 // ms
)

// Settings are the values in force, as far as the artifact records them.
type Settings map[string]any

// PredictedRoleChange is the arithmetic the documented behaviour implies. It is
// NOT a claim about what the engine does -- the field measured three to four
// times this -- and the record's job is to make that disagreement visible.
func (s Settings) PredictedRoleChange() time.Duration {
	interval := s.intOr("ha_heartbeat_interval_in_msecs", defHeartbeatInterval)
	gap := s.intOr("ha_max_heartbeat_gap", defMaxHeartbeatGap)
	return time.Duration(interval*gap) * time.Millisecond
}

func (s Settings) intOr(key string, def int) int {
	v, ok := s[key]
	if !ok {
		return def
	}
	switch t := v.(type) {
	case int:
		return t
	case float64:
		return int(t)
	case string:
		var n int
		if _, err := fmt.Sscan(t, &n); err == nil {
			return n
		}
	}
	return def
}

func (s Settings) decidedBy() map[string]any {
	out := map[string]any{
		"ha_heartbeat_interval_in_msecs":  s.intOr("ha_heartbeat_interval_in_msecs", defHeartbeatInterval),
		"ha_max_heartbeat_gap":            s.intOr("ha_max_heartbeat_gap", defMaxHeartbeatGap),
		"ha_calc_score_interval_in_msecs": s.intOr("ha_calc_score_interval_in_msecs", defCalcScoreInterval),
	}
	if v, ok := s["ha_ping_hosts"]; ok {
		out["ha_ping_hosts"] = v
	} else {
		out["ha_ping_hosts"] = nil
	}
	return out
}

// SettingsFrom pulls the parameters in force out of a describe artifact.
func SettingsFrom(describe []byte) Settings {
	s := Settings{}
	var doc struct {
		Parameters struct {
			HA     map[string]string `json:"ha"`
			Hidden map[string]string `json:"hidden"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(describe, &doc); err != nil {
		return s
	}
	for k, v := range doc.Parameters.HA {
		s[k] = v
	}
	for k, v := range doc.Parameters.Hidden {
		s[k] = v // hidden wins: it is what was actually written last
	}
	return s
}

// Build assembles the export document. extraReasons are the invalidity findings
// only the caller can make -- clock skew across nodes, a condition already in
// force when the record opened.
func Build(cluster string, entries []Entry, describe []byte, extraReasons []string) *Document {
	// Harvesting appends engine lines when they are noticed, not when they
	// happened, so the timeline is sorted before anything is measured from it.
	sorted := append([]Entry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].T < sorted[j].T })
	entries = sorted

	d := &Document{
		Schema: "csb/v1", Cluster: cluster,
		Timeline: entries, RoleChanges: []RoleChange{},
		Validity: Validity{Valid: true, Reasons: []string{}},
	}
	if len(entries) > 0 {
		d.Opened = entries[0].T
	}
	if len(describe) > 0 {
		var raw any
		if err := json.Unmarshal(describe, &raw); err == nil {
			d.Describe = raw
		}
	}
	settings := SettingsFrom(describe)
	predicted := settings.PredictedRoleChange()

	for i, e := range entries {
		if e.Actor != ActorEngine {
			continue
		}
		// Only a Success is a role change. The engine also logs [Diagnosis] and
		// [Cancelled] every few seconds while it is deciding, and those belong in
		// the timeline -- the cancel reason is the only thing that tells the two
		// split-brain flavours apart -- but counting them as transitions would
		// turn one failover into a dozen.
		if str(e.Detail["result"]) != "Success" {
			continue
		}
		rc := RoleChange{
			Node:      str(e.Detail["node"]),
			Kind:      str(e.Detail["kind"]),
			Result:    str(e.Detail["result"]),
			At:        e.T,
			Predicted: predicted.String(),
			DecidedBy: settings.decidedBy(),
			Line:      str(e.Detail["line"]),
			Source:    str(e.Detail["source"]),
		}
		// Measured from the most recent thing the tool did, because that is the
		// only end of the interval this project controls.
		for j := i - 1; j >= 0; j-- {
			if entries[j].Actor != ActorTool {
				continue
			}
			t0, err0 := time.Parse(time.RFC3339Nano, entries[j].T)
			t1, err1 := time.Parse(time.RFC3339Nano, e.T)
			if err0 == nil && err1 == nil && !t1.Before(t0) {
				rc.Trigger = entries[j].Event
				rc.Measured = t1.Sub(t0).Round(100 * time.Millisecond).String()
			}
			break
		}
		d.RoleChanges = append(d.RoleChanges, rc)
	}

	if len(extraReasons) > 0 {
		sort.Strings(extraReasons)
		d.Validity.Valid = false
		d.Validity.Reasons = extraReasons
	}
	return d
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
