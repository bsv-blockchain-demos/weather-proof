package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// RecordStore is the weather_records seam.
type RecordStore interface {
	// Insert writes one fresh reading. A collision on
	// (station_id, observation_time) reports inserted=false with a NIL error:
	// a station re-reporting the same observation is the intended steady
	// state, not an exception.
	Insert(ctx context.Context, r NewRecord) (bool, error)

	// ClaimPending atomically moves up to n pending rows to processing and
	// returns them.
	//
	// ref is supplied by the CALLER, one per call. It must not be generated
	// inside SQL: gen_random_uuid() is VOLATILE and Postgres evaluates it once
	// per updated row, so a 21-row claim produces 21 distinct refs and the
	// batch label that the adopt design uses as its idempotency key ceases to
	// exist. Rows that were previously claimed and then reaped KEEP their
	// prior ref, so one claim can legitimately return several distinct refs
	// and the caller must partition the batch by ref.
	//
	// A non-positive n returns zero rows with a NIL error — the same rule
	// List's Limit uses, and never treated as unbounded. SQL's own LIMIT $n
	// already behaves this way for n == 0; a negative n is where the two
	// shipped implementations currently disagree, and that is a recorded,
	// deliberate gap rather than an oversight: the fake clamps a negative n to
	// this same "zero rows, nil error" rule for consistency with List, while
	// Postgres's LIMIT rejects a negative value as a runtime error (SQLSTATE
	// 2201W, classified to ErrOperation) that a caller must not rely on.
	// Task 19's conformance suite must not assert n < 0 across both
	// implementations until that gap is closed.
	ClaimPending(ctx context.Context, n int, ref uuid.UUID) ([]Record, error)

	// Complete marks the given publications completed under one txid and
	// returns the refreshed stats. Records, app_stats and the station counters
	// move in ONE transaction. A second call with the same publications finds
	// no processing rows and is a no-op that does not double-count.
	Complete(ctx context.Context, txID string, pubs []Publication) (Stats, error)

	// FailPermanent marks rows failed and spends one attempt each.
	FailPermanent(ctx context.Context, ids []string, reason string) error

	// RequeueInfra returns rows to pending WITHOUT spending an attempt and
	// without requiring an adopt check: the outcome is known to be a
	// non-publish.
	RequeueInfra(ctx context.Context, ids []string, reason string) error

	// MarkUnknown returns rows to pending with adopt_required set, because the
	// publish outcome is ambiguous and a prior action may already exist.
	MarkUnknown(ctx context.Context, ids []string, reason string) error

	// ReapExpired reclaims rows stranded in processing past the lease. It
	// returns whole Records rather than ids because the caller needs
	// claim_ref to log and to reason about the adopt path.
	ReapExpired(ctx context.Context, lease time.Duration, limit int) ([]Record, error)

	// Requeue is the operator-driven bulk requeue. It returns the affected
	// count and writes nothing when f.DryRun is true.
	Requeue(ctx context.Context, f RequeueFilter) (int64, error)

	// List returns one page of records newest-first plus the unpaged total.
	//
	// There is deliberately no separate ListByStation, which the design's
	// interface table names: a station filter is one nullable field of
	// ListFilter, served by one static statement with a NULL-able bind
	// parameter. Two methods would mean two statements with the same ordering
	// and pagination rules to keep in agreement.
	//
	// A Limit of zero or negative returns zero rows. It is never treated as
	// "no limit." Total is unaffected by Limit and still reports the full
	// unpaged count of matching rows, so a caller can distinguish "nothing
	// matched" from "matched, but the page excludes it." Every implementation
	// must agree on this: SQL's own LIMIT $n returns zero rows for n=0 and
	// raises a runtime error for a negative n, so an implementation clamps at
	// this boundary rather than letting either behavior leak through as a
	// driver error.
	List(ctx context.Context, f ListFilter) ([]Record, int64, error)

	// Get returns one record, or ErrNotFound.
	Get(ctx context.Context, id string) (Record, error)

	// TxIDExists reports whether any record carries this txid. It is the
	// anti-amplification gate in front of the BEEF proof endpoint: without it,
	// an attacker iterating 64-hex values drives one upstream call per
	// request and burns the shared keyless block-explorer budget the storage
	// server also needs.
	TxIDExists(ctx context.Context, txID string) (bool, error)

	// SetBlockHeights refreshes the mined height of every record sharing each
	// txid, and the corresponding stations.last_block_height, in ONE
	// transaction. It no-ops for a txid that matches no row.
	SetBlockHeights(ctx context.Context, ups []BlockHeightUpdate) error

	// ReconcileCandidates returns completed rows that are not yet known mined
	// and were processed longer ago than olderThan.
	ReconcileCandidates(ctx context.Context, olderThan time.Duration, limit int) ([]Record, error)

	// Snapshot is the row-count half of the operational heartbeat.
	Snapshot(ctx context.Context) (Snapshot, error)
}

// StationStore is the stations seam.
type StationStore interface {
	// Upsert inserts or refreshes a station's identity columns. It never
	// touches the counters, which only Complete moves.
	Upsert(ctx context.Context, s Station) error

	// List returns one page of stations plus the unpaged total. When
	// f.Search parses as an integer it is an exact station_id lookup ordered
	// by station_id; otherwise it is a full-text match ordered by rank.
	//
	// As with RecordStore.List, a Limit of zero or negative returns zero rows
	// rather than being treated as unbounded, and Total still reports the full
	// unpaged count of matching rows.
	List(ctx context.Context, f StationFilter) ([]Station, int64, error)

	// Get returns one station, or ErrNotFound.
	Get(ctx context.Context, stationID int64) (Station, error)

	// Stats returns the four dashboard values.
	Stats(ctx context.Context) (Stats, error)
}

// DepositStore is the operator funding-deposit seam.
type DepositStore interface {
	NewDeposit(ctx context.Context, d Deposit) error
	PendingDeposits(ctx context.Context) ([]Deposit, error)
	MarkInternalized(ctx context.Context, suffix, txID string, vout int32, sats int64) error
}

// PreflightStore caches the boot-time preflight fingerprint.
//
// Note the ctx: every other store method needs one for the per-call budget
// and for shutdown cancellation, and preflight runs on the boot path behind
// the same deadlines.
type PreflightStore interface {
	PreflightOK(ctx context.Context, fingerprint string) (bool, error)
	RecordPreflight(ctx context.Context, fingerprint string) error
}

// Pinger is the readiness probe's whole dependency. It is separate from the
// four data interfaces so /api/ready can be wired to a pool without being
// handed the ability to write.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Store is the aggregate seam.
//
// It is a STRUCT of interfaces and not one composed interface, and that is
// forced rather than chosen: RecordStore and StationStore both declare List
// and Get with DIFFERENT signatures, and Go permits a duplicate method name
// across embedded interfaces only when the signatures are identical. An
// embedded version does not compile ("duplicate method Get"). The struct is
// also the better test shape, since a test can supply one real member and
// leave the rest nil.
type Store struct {
	Records   RecordStore
	Stations  StationStore
	Deposits  DepositStore
	Preflight PreflightStore
	Health    Pinger
}
