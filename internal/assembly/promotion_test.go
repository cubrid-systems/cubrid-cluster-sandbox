package assembly

import (
	"strings"
	"testing"
)

// The two conditions used to share one sentence, and the sentence was the
// drain one. A node whose applier is at 187 of 187 was told "replication has
// to drain first", which is advice that cannot be followed: it has drained.
// fail_counter counts rows that could not be applied, the engine leaves it
// standing on purpose, and no amount of waiting moves it.
func TestPromotionBlockerSaysWhichConditionStoppedIt(t *testing.T) {
	cases := []struct {
		name                string
		eof, final, fail    int
		force               bool
		wantErr             bool
		wantHas, wantHasNot []string
	}{
		{
			name: "not drained", eof: 200, final: 187, fail: 0, wantErr: true,
			wantHas:    []string{"at 187 of 200", "13 page(s) have still to drain", "Waiting is the remedy"},
			wantHasNot: []string{"fail_counter"},
		},
		{
			name: "drained but rows failed", eof: 187, final: 187, fail: 5, wantErr: true,
			wantHas:    []string{"drained (187 of 187)", "fail_counter is 5", "ha resync --path slave", "--force"},
			wantHasNot: []string{"have still to drain"},
		},
		{
			name: "drained and clean", eof: 187, final: 187, fail: 0, wantErr: false,
		},
		{
			name: "drained, rows failed, forced", eof: 187, final: 187, fail: 5, force: true, wantErr: false,
		},
		{
			// Draining is never overridden by --force: waiting fixes it, and
			// forcing past it loses the late log for no benefit.
			name: "not drained, forced anyway", eof: 200, final: 187, fail: 0, force: true, wantErr: true,
			wantHas: []string{"have still to drain"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := promotionBlocker("pmha-n2", tc.eof, tc.final, tc.fail, tc.force)
			if tc.wantErr != (err != nil) {
				t.Fatalf("wantErr=%v, got %v", tc.wantErr, err)
			}
			if err == nil {
				return
			}
			for _, want := range tc.wantHas {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("message does not contain %q:\n%s", want, err)
				}
			}
			for _, not := range tc.wantHasNot {
				if strings.Contains(err.Error(), not) {
					t.Errorf("message should not mention %q here:\n%s", not, err)
				}
			}
		})
	}
}

// The remedy has to name a command that exists and works in the state the
// message is printed in. resync needs a master; the whole point of this
// message is that there is none, so it must also name the one that does not.
func TestTheDivergenceMessageNamesAWayOutThatWorksWithNoMaster(t *testing.T) {
	err := promotionBlocker("pmha-n2", 187, 187, 5, false)
	if err == nil {
		t.Fatal("a non-zero fail_counter is not safe to promote over")
	}
	if !strings.Contains(err.Error(), "ha promote pmha-n2 --force") {
		t.Errorf("the message does not name the escape that works without a master:\n%s", err)
	}
	if !strings.Contains(err.Error(), "no master to rebuild from") {
		t.Errorf("the message does not say when resync is the wrong advice:\n%s", err)
	}
}
