package record

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendAndRead(t *testing.T) {
	dir := t.TempDir()
	// The path's parent does not exist yet: the first state-changing command
	// must open the record without anybody creating it first.
	r := Open(filepath.Join(dir, "clusters", "hadb", "record.jsonl"))

	if err := r.Append(ActorTool, "command.cluster.create", map[string]any{"preset": "ha"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := r.Append(ActorEngine, "role.change", map[string]any{"node": "hadb-n2"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := r.Read(time.Time{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Read returned %d entries, want 2", len(got))
	}
	if got[0].Actor != ActorTool || got[0].Event != "command.cluster.create" {
		t.Errorf("first entry = %+v", got[0])
	}
	if got[1].Detail["node"] != "hadb-n2" {
		t.Errorf("detail lost: %+v", got[1].Detail)
	}
	if got[0].T == "" || got[1].T == "" {
		t.Error("entries must carry their own timestamp")
	}
}

func TestReadMissingIsEmptyNotError(t *testing.T) {
	r := Open(filepath.Join(t.TempDir(), "nope.jsonl"))
	got, err := r.Read(time.Time{})
	if err != nil || got != nil {
		t.Fatalf("Read of a missing record = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestReadSkipsTruncatedTail(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "record.jsonl")
	r := Open(p)
	if err := r.Append(ActorTool, "one", nil); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"t":"2026-09-02T00:00:00Z","actor":"tool","ev`) // killed mid-write
	f.Close()

	got, err := r.Read(time.Time{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("a truncated final line must not lose the rest: got %d entries", len(got))
	}
}

func TestReadSince(t *testing.T) {
	r := Open(filepath.Join(t.TempDir(), "record.jsonl"))
	if err := r.Append(ActorTool, "old", nil); err != nil {
		t.Fatal(err)
	}
	cut := time.Now().Add(50 * time.Millisecond)
	time.Sleep(80 * time.Millisecond)
	if err := r.Append(ActorTool, "new", nil); err != nil {
		t.Fatal(err)
	}

	got, err := r.Read(cut)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Event != "new" {
		t.Fatalf("Read(since) = %+v, want only the newer entry", got)
	}
}
