package cli

import (
	"encoding/json"
	"io"
	"time"
)

// SchemaVersion identifies the output contract. It changes only when the shape
// changes, which is the whole point of having it.
const SchemaVersion = "csb/v1"

// Severity of a note. Three levels, because a consumer needs to tell "this
// number is missing and here is why" from "this run is not trustworthy".
const (
	SevInfo  = "info"
	SevWarn  = "warn"
	SevError = "error"
)

// Note is machine-readable, never prose. Every code corresponds to something
// that was observed or measured -- see docs/design/01-cli.md §4.
type Note struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Envelope is the --json form of every command.
type Envelope struct {
	Schema  string `json:"schema"`
	Command string `json:"command"`
	Cluster string `json:"cluster,omitempty"`
	At      string `json:"at"`
	OK      bool   `json:"ok"`
	Data    any    `json:"data"`
	Notes   []Note `json:"notes"`
}

func newEnvelope(command, cluster string, now time.Time) *Envelope {
	return &Envelope{
		Schema:  SchemaVersion,
		Command: command,
		Cluster: cluster,
		At:      now.UTC().Format(time.RFC3339),
		OK:      true,
		Data:    map[string]any{},
		Notes:   []Note{},
	}
}

func (e *Envelope) note(code, severity, msg string) {
	e.Notes = append(e.Notes, Note{Code: code, Severity: severity, Message: msg})
}

func (e *Envelope) writeJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(e)
}
