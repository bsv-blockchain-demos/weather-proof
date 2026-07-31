package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

// TestClassifyStripsDetailFromACanceledContextError is a fast,
// Postgres-independent unit test directly on classify (package-internal,
// since classify is unexported).
//
// It exists because driving classify's context.Canceled branch through a
// real Insert call with an already-canceled context is NOT sufficient on its
// own: measured against the real driver, an already-canceled context
// reaching pgx synchronously (before any query is sent) already returns the
// bare context.Canceled sentinel with nothing attached, so a mutation that
// makes classify return err UNCHANGED in that branch is observationally
// identical to the fix and such a black-box test cannot tell them apart.
// Confirmed by deliberately introducing that exact mutation in a scratch
// copy: a black-box test doing exactly this
// (TestClassifierRejectsAnAlreadyCanceledContext, added at Task 7) still
// passed; it and its TestClassifierRejectsAnExpiredDeadline counterpart were
// deleted at Task 17 as redundant against this test. See
// records_insert_test.go's note at the same location for the full account,
// including the one flake that made deletion, rather than a further
// hardening pass, the right call.
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

// TestClassifyIsIdempotent is a fast, Postgres-independent unit test directly
// on classify, and it exists for the same reason as the two tests above: the
// thing it guards cannot be reproduced deterministically through a live
// connection. Complete and SetBlockHeights each now wrap pgx.BeginFunc's own
// return value in classify, on top of the classify calls already inside their
// closures — because BeginFunc's own Begin/Commit failure is a driver error
// that reaches neither of those inner calls. But the closure's own statement
// errors (a unique_violation from completeRecordsSQL, say) are ALREADY
// classified once before BeginFunc hands that same value back as its own
// return value, so the wrapping call classifies it a second time.
//
// None of classify's wrapped return values re-embeds a *pgconn.PgError in
// their chain — the SQLSTATE travels as a %s-formatted string, never as a
// wrapped error object — so a naive (non-idempotent) classify fails
// errors.As(err, &pgErr) on the second pass and falls through to the opaque
// default, silently turning store.ErrNotFound, store.ErrConflict or
// ErrTransient into ErrOperation. That mutation is invisible to
// TestNoDriverErrorEscapesTheStore's unreachable-pool case, which only
// classifies a genuinely UNCLASSIFIED connect error and never exercises the
// double-classify path at all, so this direct unit test is the only place
// that closes it.
func TestClassifyIsIdempotent(t *testing.T) {
	cases := []struct {
		name string
		want error
	}{
		{"ErrNotFound", store.ErrNotFound},
		{"ErrConflict", classify(&pgconn.PgError{Code: sqlStateUniqueViolation})},
		{"ErrTransient", classify(&pgconn.PgError{Code: sqlStateDeadlockDetected})},
		{"ErrOperation", classify(&pgconn.PgError{Code: "42601"})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			once := c.want
			twice := classify(once)
			if !errors.Is(twice, once) {
				t.Fatalf("classify(classify(err)) = %v, want errors.Is(…, %v): a second classify pass must not "+
					"change what the first pass already decided", twice, once)
			}
			if twice.Error() != once.Error() {
				t.Fatalf("classify(classify(err)).Error() = %q, want unchanged %q", twice.Error(), once.Error())
			}
		})
	}

	// store.ErrNotFound itself (the exact value classify returns for
	// pgx.ErrNoRows, not merely something matching errors.Is against it) is
	// the sharpest case: it carries no SQLSTATE and unwraps to nothing, so
	// there is no signal LEFT for a naive classify to react to except the
	// idempotency guard itself.
	if got := classify(store.ErrNotFound); !errors.Is(got, store.ErrNotFound) {
		t.Fatalf("classify(store.ErrNotFound) = %v, want store.ErrNotFound unchanged", got)
	}
}

// TestIntervalArgClampsANegativeDuration is Postgres-independent: it asserts the
// rendered VALUE, not a query result.
//
// The interval is always used as an age threshold (`now() - $n`), so a negative
// duration would push that threshold into the future and make ReapExpired
// reclaim unexpired leases. The zero and positive rows are the negative control:
// a clamp that flattened everything to "0 microseconds" would satisfy the
// negative row alone.
func TestIntervalArgClampsANegativeDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{in: -time.Hour, want: "0 microseconds"},
		{in: -1, want: "0 microseconds"},
		{in: 0, want: "0 microseconds"},
		{in: time.Second, want: "1000000 microseconds"},
		{in: 5 * time.Minute, want: "300000000 microseconds"},
	}

	for _, tc := range cases {
		if got := intervalArg(tc.in); got != tc.want {
			t.Errorf("intervalArg(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
