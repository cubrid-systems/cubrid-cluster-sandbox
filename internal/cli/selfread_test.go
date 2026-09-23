package cli

import (
	"os"
	"strings"
	"testing"
)

func readSelf(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("cannot read %s: %v", name, err)
	}
	return string(b)
}

func between(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i+len(from):], to)
	if j < 0 {
		return s[i:]
	}
	return s[i : i+len(from)+j]
}
