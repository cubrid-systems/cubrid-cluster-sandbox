package cli

import (
	"strings"
	"testing"
)

// A selected destroy must do what a named one does. The first version called
// the dispatcher instead of the body, so every per-cluster call bounced off the
// guard that refuses --label together with --cluster: the command printed the
// list it was about to destroy, destroyed nothing, and exited 0.
func TestASelectedDestroyDoesNotDispatchBackToItself(t *testing.T) {
	src := readSelf(t, "cluster.go")
	body := between(src, "func destroyByLabel(", "\nfunc ")
	if body == "" {
		t.Fatal("destroyByLabel not found")
	}
	if strings.Contains(body, "cmdClusterDestroy(") {
		t.Error("destroyByLabel calls the dispatcher, which will re-read --label and refuse every cluster")
	}
	if !strings.Contains(body, "destroyOne(") {
		t.Error("destroyByLabel should call the single-cluster body")
	}
}

// Destroying nothing is a failure, whatever was printed on the way. An operator
// who asked for several clusters to go cannot see which did.
func TestDestroyingNoneOfThemIsAFailure(t *testing.T) {
	body := between(readSelf(t, "cluster.go"), "func destroyByLabel(", "\nfunc ")
	if !strings.Contains(body, "removed == 0") {
		t.Error("destroyByLabel must report a failure when nothing was removed")
	}
}
