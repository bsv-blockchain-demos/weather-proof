package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestClassifyStripsDetailFromACanceledContextError is a fast,
// Postgres-independent unit test directly on classify (package-internal,
// since classify is unexported).
//
// It exists because driving classify's context.Canceled branch through a
// real Insert call with an already-canceled context — see
// TestClassifierRejectsAnAlreadyCanceledContext in records_insert_test.go —
// is NOT sufficient on its own: measured against the real driver, an
// already-canceled context reaching pgx synchronously (before any query is
// sent) already returns the bare context.Canceled sentinel with nothing
// attached, so a mutation that makes classify return err UNCHANGED in that
// branch is observationally identical to the fix and the black-box test
// cannot tell them apart. Confirmed by deliberately introducing that exact
// mutation in a scratch copy: the black-box test still passed.
//
// The real leak this branch guards against is documented on classify itself:
// a pgxpool connection acquire racing a context cancellation can produce a
// *pgconn.ConnectError wrapping `failed to connect to `user=... database=...`:
// ...`, which still satisfies errors.Is(err, context.Canceled) via Unwrap.
// Reproducing THAT race deterministically through a live connection is not
// possible (confirmed: puddle's Acquire races the constructor's error against
// ctx.Done() over the same expiring context, so which one a caller observes
// is inherently nondeterministic). Constructing the equivalent shape directly
// and calling classify with it is the only deterministic way to prove the
// stripping behavior.
func TestClassifyStripsDetailFromACanceledContextError(t *testing.T) {
	leaky := fmt.Errorf(
		"failed to connect to `host=prod-db.internal:5432 user=produser database=proddb`: %w", context.Canceled,
	)

	got := classify(leaky)
	if !errors.Is(got, context.Canceled) {
		t.Fatalf("classify(leaky) = %v, want context.Canceled", got)
	}
	if got.Error() != context.Canceled.Error() {
		t.Fatalf("classify(leaky) = %q, want exactly %q and nothing more", got.Error(), context.Canceled.Error())
	}
}

// TestClassifyStripsDetailFromAnExpiredDeadlineError is
// TestClassifyStripsDetailFromACanceledContextError's counterpart for
// classify's context.DeadlineExceeded branch. See that test's comment for
// why a live, black-box reproduction cannot discriminate this mutation.
func TestClassifyStripsDetailFromAnExpiredDeadlineError(t *testing.T) {
	leaky := fmt.Errorf(
		"failed to connect to `host=prod-db.internal:5432 user=produser database=proddb`: %w", context.DeadlineExceeded,
	)

	got := classify(leaky)
	if !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("classify(leaky) = %v, want context.DeadlineExceeded", got)
	}
	if got.Error() != context.DeadlineExceeded.Error() {
		t.Fatalf("classify(leaky) = %q, want exactly %q and nothing more", got.Error(), context.DeadlineExceeded.Error())
	}
}
