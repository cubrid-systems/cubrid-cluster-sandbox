package cli

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Destroy empties the workdir instead of removing it, and the identity of the
// directory is the whole point. Rootless podman keeps a mount namespace alive
// between commands, so a bind-mount source that is deleted and recreated is
// bound by its OLD directory in the next cluster: /work comes up empty inside
// the node while the host directory has the tree in it, and the first thing
// that fails is `createdb exited 127: cubrid: command not found` -- three
// layers from the cause. docker survives the same destroy-and-create because
// its daemon resolves the path per container, which is why this was invisible
// until a second backend existed.
//
// The assertion holds an open descriptor on the directory across the call,
// which is what podman is doing and is also what makes the check sound: while
// the old directory is pinned its inode number cannot be handed to a
// replacement, so a delete-and-recreate is forced to differ. Comparing bare
// stat before and after does not work -- the kernel hands the number straight
// back, and a RemoveAll+MkdirAll passes.
func TestDestroyEmptiesTheWorkdirRatherThanReplacingIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(filepath.Join(dir, "pmha-n1", "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pmha-n1", "db", "vol"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The reference a node's bind mount is, in miniature.
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		t.Fatalf("could not hold the directory open: %v", err)
	}
	defer syscall.Close(fd)
	var held syscall.Stat_t
	if err := syscall.Fstat(fd, &held); err != nil {
		t.Fatal(err)
	}

	if err := emptyDir(dir); err != nil {
		t.Fatalf("emptyDir: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("the directory must survive its own emptying: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("emptied directory still holds %d entr(ies)", len(entries))
	}

	var now syscall.Stat_t
	if err := syscall.Stat(dir, &now); err != nil {
		t.Fatal(err)
	}
	if held.Ino != now.Ino || held.Dev != now.Dev {
		t.Errorf("the directory at this path was replaced rather than emptied "+
			"(held %d:%d, now %d:%d); a rootless podman node holding the old one "+
			"would come up with an empty /work", held.Dev, held.Ino, now.Dev, now.Ino)
	}
}

// Destroy runs on clusters that never got a workdir, and a missing one is not
// a failure to report -- there is nothing left to leave behind.
func TestEmptyingWhatIsNotThereIsNotAnError(t *testing.T) {
	if err := emptyDir(filepath.Join(t.TempDir(), "never-made")); err != nil {
		t.Errorf("a workdir that was never created is not a destroy failure: %v", err)
	}
}
