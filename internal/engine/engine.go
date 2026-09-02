// Package engine resolves where the engine under test comes from and what it is.
//
// The build under test is an argument (DESIGN.md §2 G2): a locally built tree
// runs by path, bind-mounted read-only, and rebuilding the engine never rebuilds
// an image. What this package produces is the identity that goes into the
// describe artifact, because a build tree does not travel but its identity does
// (docs/design/02-topology.md §4).
package engine

import (
	"context"
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/cubrid-systems/cubrid-cluster-sandbox/internal/run"
)

type Identity struct {
	Kind     string `json:"kind"`               // "build" or "version"
	Path     string `json:"path,omitempty"`     // for kind=build, on this machine only
	Version  string `json:"version,omitempty"`  // 11.5.0
	Build    string `json:"build,omitempty"`    // 11.5.0.2513-dd15f7f
	Commit   string `json:"commit,omitempty"`   // dd15f7f
	BuiltAt  string `json:"built_at,omitempty"` // as the engine reports it
	MinGlibc string `json:"min_glibc,omitempty"`
}

// relLine: CUBRID 11.5.0 (11.5.0.2513-dd15f7f) (64bit release build for Linux) (Aug 26 2026 16:02:58)
var relLine = regexp.MustCompile(`CUBRID\s+(\S+)\s+\(([^)]+)\)\s+\(([^)]*)\)\s+\(([^)]*)\)`)

// Resolve validates a build tree and reads its identity. It runs the tree's own
// cubrid_rel rather than parsing a file, because that is the answer the engine
// gives about itself.
func Resolve(ctx context.Context, path string, r *run.Runner) (*Identity, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	server := filepath.Join(abs, "bin", "cub_server")
	if _, err := os.Stat(server); err != nil {
		return nil, fmt.Errorf("%s does not look like a CUBRID install tree: no bin/cub_server", abs)
	}

	id := &Identity{Kind: "build", Path: abs}
	if g, err := minGlibc(server); err == nil {
		id.MinGlibc = g
	}

	rel := filepath.Join(abs, "bin", "cubrid_rel")
	res, err := r.Run(ctx, rel)
	if err != nil || res.ExitCode != 0 {
		return id, nil // identity is thinner, not absent; the caller notes it
	}
	if m := relLine.FindStringSubmatch(res.Stdout); m != nil {
		id.Version, id.Build, id.BuiltAt = m[1], m[2], m[4]
		if i := strings.LastIndex(m[2], "-"); i >= 0 {
			id.Commit = m[2][i+1:]
		}
	}
	return id, nil
}

// minGlibc reports the highest GLIBC symbol version the binary requires, which
// is the floor a base image has to clear. A tree built on a newer distribution
// than the container fails to load, and 02-topology.md §3 requires that to be
// caught rather than debugged.
func minGlibc(binary string) (string, error) {
	f, err := elf.Open(binary)
	if err != nil {
		return "", err
	}
	defer f.Close()
	syms, err := f.ImportedSymbols()
	if err != nil {
		return "", err
	}
	best := ""
	bestN := -1.0
	for _, s := range syms {
		if !strings.HasPrefix(s.Version, "GLIBC_") {
			continue
		}
		if n := versionNum(strings.TrimPrefix(s.Version, "GLIBC_")); n > bestN {
			bestN, best = n, s.Version
		}
	}
	if best == "" {
		return "", fmt.Errorf("no GLIBC version requirements found")
	}
	return strings.TrimPrefix(best, "GLIBC_"), nil
}

func versionNum(v string) float64 {
	parts := strings.Split(v, ".")
	var out float64
	scale := 1.0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return -1
		}
		out += float64(n) * scale
		scale /= 1000
	}
	return out
}
