package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// The likeliest first failure for the audience this tool is for: `~/cubrid` is
// the source tree a CUBRID developer lives in and `~/cubrid/install.out` is what
// the build puts in it. The tool can see which one it was handed, so it says so.
func TestASourceTreeIsNamedAsOneRatherThanRefusedAsNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "install.out", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "install.out", "bin", "cub_server"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Resolve(context.Background(), root, &run.Runner{})
	if err == nil {
		t.Fatal("a source tree was accepted as an install tree")
	}
	if !strings.Contains(err.Error(), filepath.Join(root, "install.out")) {
		t.Errorf("the message does not name the tree that is there: %v", err)
	}

	// And a directory that is neither still says the plain thing, rather than
	// pointing at an install.out that does not exist.
	bare := t.TempDir()
	_, err = Resolve(context.Background(), bare, &run.Runner{})
	if err == nil || !strings.Contains(err.Error(), "no bin/cub_server") {
		t.Errorf("a directory with no engine in it: %v", err)
	}
}
