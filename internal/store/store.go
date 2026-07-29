// Package store is the persistence seam.
//
// It holds the domain types and the interfaces every layer above persistence
// programs against, and it imports NO database driver. That is deliberate and
// load-bearing: internal/api and internal/proof are testable against
// internal/store/fake with no database at all, which is what keeps the bulk of
// the HTTP test suite running in the ordinary CI job. Nothing in this package
// may ever import internal/store/postgres.
package store

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
)

// The three sentinel errors every implementation returns instead of a driver
// error. A *pgconn.PgError carries SQL text, column names, constraint names
// and sometimes column VALUES in its Error() string, so it must never travel
// far enough to reach an HTTP response body.
var (
	// ErrNotFound is returned by Get when no row matches.
	ErrNotFound = errors.New("store: not found")

	// ErrConflict is returned when a write collided in a way that is NOT the
	// expected steady state. Note that Insert's dedupe collision is not a
	// conflict: a station re-reporting the same observation_time is normal and
	// reports inserted=false with a nil error.
	ErrConflict = errors.New("store: conflicting row")

	// ErrInvalidText reports a string that a Postgres text parameter cannot
	// carry at all. See ValidText.
	ErrInvalidText = errors.New("store: text value contains a NUL byte or malformed UTF-8")
)

// ValidText reports whether s can be bound to a text parameter.
//
// MEASURED against postgres:17-alpine with pgx v5.10.0: binding a string
// containing 0x00, OR any malformed UTF-8 byte sequence, to `SELECT
// $1::text` fails with `invalid byte sequence for encoding "UTF8": ...
// (SQLSTATE 22021)`. That includes a bare high bit (0x80), an out-of-range
// byte (0xff), a truncated multibyte sequence, a lone UTF-16 surrogate
// re-encoded as bytes, and an overlong encoding of NUL. The extended query
// protocol prevents INJECTION — it does not make the value legal — so a
// request carrying %00 or %80 is a 500 unless something rejects it first,
// and Go's query-string and path decoding both hand the raw bytes straight
// through without validating them.
//
// NUL needs its own check IN ADDITION to utf8.ValidString: NUL is valid
// UTF-8, so utf8.ValidString("\x00") reports true. Replacing the NUL check
// with utf8.ValidString alone would silently let 0x00 back through.
//
// This is deliberately a leaf function in the datastore-agnostic package,
// because BOTH layers need it and for different reasons: the API layer maps a
// failure to 400 (the right status), and internal/store/postgres treats a
// failure as a MISS (so the store is total and no future caller can produce a
// 500 from user input). Nothing else is rejected: tabs, newlines and any
// well-formed UTF-8 are legitimate search terms.
func ValidText(s string) error {
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return ErrInvalidText
	}
	return nil
}

// Status is the lifecycle status of a weather record, and it is the exact
// string stored in the status text column and emitted on the wire.
//
// It is a named string type rather than an int enum because it encodes and
// scans directly against a text column, and because the four values are the
// four keys the frontend indexes its statusStyles map with. An unknown value
// does not crash the SPA but makes the row read "Not On-Chain" forever, so the
// set is closed and the API rejects anything outside it with a 400.
type Status string

// The only four status values that exist. There are no others.
const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
)

// Valid reports whether s is one of the four known statuses. It is the
// allowlist behind the ?status= query parameter.
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusProcessing, StatusCompleted, StatusFailed:
		return true
	default:
		return false
	}
}

// ChainStatus is the on-chain settlement detail of a published record. It is a
// SEPARATE column and a separate type from Status: the SPA never sees it, and
// conflating the two is how an "aborted" transaction ends up reported as a
// "failed" record.
type ChainStatus string

// The only four chain statuses that exist.
const (
	ChainARCAccepted ChainStatus = "arc-accepted"
	ChainUnmined     ChainStatus = "unmined"
	ChainMined       ChainStatus = "mined"
	ChainAborted     ChainStatus = "aborted"
)

// Valid reports whether c is one of the four known chain statuses.
func (c ChainStatus) Valid() bool {
	switch c {
	case ChainARCAccepted, ChainUnmined, ChainMined, ChainAborted:
		return true
	default:
		return false
	}
}

// Record is one row of weather_records.
//
// Every nullable column is a POINTER. That is not a style choice: a zero
// time.Time marshals to "0001-01-01T00:00:00Z", which the frontend's
// formatDateTime renders as 01/01/0001 rather than the em dash it intends for
// an absent value, and a zero string is indistinguishable from an empty error
// message. The db tags are read by pgx.RowToStructByName and must match the
// column names in every explicit SELECT and RETURNING list exactly.
type Record struct {
	ID              string              `db:"id"`
	StationID       int64               `db:"station_id"`
	Timestamp       time.Time           `db:"timestamp"`
	ObservationTime time.Time           `db:"observation_time"`
	Data            weather.WeatherData `db:"data"`
	Status          Status              `db:"status"`
	Attempts        int32               `db:"attempts"`
	ClaimRef        *uuid.UUID          `db:"claim_ref"`
	AdoptRequired   bool                `db:"adopt_required"`
	ClaimedAt       *time.Time          `db:"claimed_at"`
	TxID            *string             `db:"txid"`
	OutputIndex     *int32              `db:"output_index"`
	BlockHeight     *int64              `db:"block_height"`
	ChainStatus     *ChainStatus        `db:"chain_status"`
	MinedAt         *time.Time          `db:"mined_at"`
	Error           *string             `db:"error"`
	CreatedAt       time.Time           `db:"created_at"`
	ProcessedAt     *time.Time          `db:"processed_at"`
}

// NewRecord is a record as the poller can supply it.
//
// It is a separate type from Record on purpose: a caller inserting a fresh
// reading has no business setting status, attempts, claim_ref or any of the
// publication columns, and giving it a Record would let it try.
type NewRecord struct {
	ID              string              `db:"id"`
	StationID       int64               `db:"station_id"`
	Timestamp       time.Time           `db:"timestamp"`
	ObservationTime time.Time           `db:"observation_time"`
	Data            weather.WeatherData `db:"data"`
}

// Station is one row of stations.
//
// Note what is NOT here: the derived "online" flag. The store returns IsActive
// and LastReading and the API derives online from the poll rate, which keeps
// the freshness window in config, keeps the DTO test deterministic without
// freezing now(), and stops the poll rate leaking into persistence.
type Station struct {
	StationID       int64      `db:"station_id"`
	Name            string     `db:"name"`
	Location        string     `db:"location"`
	Latitude        *float64   `db:"latitude"`
	Longitude       *float64   `db:"longitude"`
	IsActive        bool       `db:"is_active"`
	TxRecords       int64      `db:"tx_records"`
	LastReading     *time.Time `db:"last_reading"`
	LastTemp        *float64   `db:"last_temp"`
	LastConditions  string     `db:"last_conditions"`
	LastBlockHeight *int64     `db:"last_block_height"`
	CreatedAt       time.Time  `db:"created_at"`
	UpdatedAt       time.Time  `db:"updated_at"`
}

// Stats is the four-value dashboard summary, plus the record count the fourth
// value is derived from.
type Stats struct {
	ActiveStations  int64      `db:"active_stations"`
	TotalTx         int64      `db:"total_tx"`
	TotalRecords    int64      `db:"total_records"`
	LastRecordWrite *time.Time `db:"last_record_write"`
}

// TotalDataPoints is the frontend's totalDataPoints tile.
//
// The multiplier is weather.DataFieldsPerRecord and never a literal 33. There
// is exactly one definition of the field count in this module and a second
// copy is how the two drift.
func (s Stats) TotalDataPoints() int64 {
	return s.TotalRecords * weather.DataFieldsPerRecord
}

// Snapshot is the row-count half of the operational heartbeat.
type Snapshot struct {
	PendingRows             int64 `db:"pending_rows"`
	ProcessingRows          int64 `db:"processing_rows"`
	FailedRows              int64 `db:"failed_rows"`
	StillUnminedOlderThan1h int64 `db:"still_unmined_older_than_1h"`
	MinedCount              int64 `db:"mined_count"`
	AbortedCount            int64 `db:"aborted_count"`
}

// ListFilter is the record list query. A nil StationID or Status means the
// filter is absent, which the SQL expresses as a NULL-able bind parameter
// rather than a conditionally assembled WHERE clause.
type ListFilter struct {
	StationID *int64
	Status    *Status
	Limit     int
	Offset    int
}

// StationFilter is the station list query. Search is already trimmed by the
// caller; an empty Search means no filter and no ranking.
type StationFilter struct {
	Search string
	Limit  int
	Offset int
}

// Publication is one record's placement in a published transaction.
type Publication struct {
	RecordID    string
	OutputIndex int32
}

// BlockHeightUpdate refreshes the mined height of every record sharing a txid.
//
// BlockHeight is deliberately a plain int64 with no way for an HTTP caller to
// supply it: on the unauthenticated verify path the caller chooses only WHICH
// rows are refreshed, and the value comes from the block explorer.
type BlockHeightUpdate struct {
	TxID        string
	BlockHeight int64
	MinedAt     *time.Time
}

// RequeueFilter is the operator-driven bulk requeue. DryRun counts without
// writing.
type RequeueFilter struct {
	Status    Status
	Since     time.Duration
	StationID *int64
	Limit     int
	DryRun    bool
}

// Deposit is one operator funding deposit awaiting internalization.
type Deposit struct {
	Suffix         string     `db:"suffix"`
	Prefix         string     `db:"prefix"`
	Address        string     `db:"address"`
	LockingScript  string     `db:"locking_script"`
	CreatedAt      time.Time  `db:"created_at"`
	TxID           *string    `db:"txid"`
	Vout           *int32     `db:"vout"`
	Satoshis       *int64     `db:"satoshis"`
	InternalizedAt *time.Time `db:"internalized_at"`
}
