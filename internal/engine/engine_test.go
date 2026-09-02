package engine

import (
	"context"
	"os"
	"testing"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/run"
)

func TestParseRelLine(t *testing.T) {
	const line = "\nCUBRID 11.5.0 (11.5.0.2513-dd15f7f) (64bit release build for Linux) (Aug 26 2026 16:02:58)\n"
	m := relLine.FindStringSubmatch(line)
	if m == nil {
		t.Fatal("cubrid_rel output did not parse")
	}
	if m[1] != "11.5.0" || m[2] != "11.5.0.2513-dd15f7f" {
		t.Fatalf("version=%q build=%q", m[1], m[2])
	}
	if m[4] != "Aug 26 2026 16:02:58" {
		t.Errorf("built_at = %q", m[4])
	}
}

func TestVersionOrdering(t *testing.T) {
	if !(versionNum("2.34") > versionNum("2.14")) {
		t.Error("2.34 must sort above 2.14, not below it as string comparison would")
	}
	if !(versionNum("2.4") < versionNum("2.34")) {
		t.Error("2.4 < 2.34")
	}
}

// Against a real install tree when one is present. CSB_TEST_ENGINE names it.
func TestResolveRealTree(t *testing.T) {
	path := os.Getenv("CSB_TEST_ENGINE")
	if path == "" {
		t.Skip("set CSB_TEST_ENGINE to a CUBRID install tree to run this")
	}
	id, err := Resolve(context.Background(), path, &run.Runner{})
	if err != nil {
		t.Fatal(err)
	}
	if id.Version == "" || id.Commit == "" {
		t.Errorf("identity is thin: %+v", id)
	}
	if id.MinGlibc == "" {
		t.Error("a build tree must report the glibc floor it needs")
	}
}

func TestResolveRejectsSomethingThatIsNotATree(t *testing.T) {
	if _, err := Resolve(context.Background(), t.TempDir(), &run.Runner{}); err == nil {
		t.Fatal("an empty directory is not an install tree")
	}
}
