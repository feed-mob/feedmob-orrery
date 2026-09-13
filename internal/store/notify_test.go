package store

import (
	"context"
	"testing"
)

// A workflow that has been failing for six hours should have produced one
// message, not twelve.
func TestNoteResultOnlyReportsChanges(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	const scope = "feed-mob/app\x00.github/workflows/deploy.yml\x00refs/heads/main"

	// A first-ever success is not news: nobody needs to be told that a workflow
	// they just set up worked.
	if prev, changed, err := st.NoteResult(ctx, scope, "success"); err != nil || changed {
		t.Fatalf("first success: prev=%q changed=%v err=%v", prev, changed, err)
	}
	// Going red is.
	prev, changed, err := st.NoteResult(ctx, scope, "failure")
	if err != nil || !changed || prev != "success" {
		t.Fatalf("first failure: prev=%q changed=%v err=%v", prev, changed, err)
	}
	// Staying red is not.
	for i := 0; i < 3; i++ {
		if _, changed, err := st.NoteResult(ctx, scope, "failure"); err != nil || changed {
			t.Fatalf("repeat failure %d re-notified", i)
		}
	}
	// Going green again is.
	prev, changed, err = st.NoteResult(ctx, scope, "success")
	if err != nil || !changed || prev != "failure" {
		t.Fatalf("recovery: prev=%q changed=%v err=%v", prev, changed, err)
	}

	// A different ref is a different story; main going red must not be masked
	// by a branch that is always red.
	other := "feed-mob/app\x00.github/workflows/deploy.yml\x00refs/heads/feature"
	if _, changed, err := st.NoteResult(ctx, other, "failure"); err != nil || !changed {
		t.Fatalf("another ref: changed=%v err=%v", changed, err)
	}
}

// A first-ever failure is news even with nothing to compare against.
func TestNoteResultReportsAFirstFailure(t *testing.T) {
	st := testStore(t)
	_, changed, err := st.NoteResult(context.Background(), "s", "failure")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
}
