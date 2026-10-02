package store_test

import (
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

// TestStatusWireLiterals pins the four strings the frontend indexes its
// statusStyles map with. If any of them changes, VerificationBadge renders an
// undefined className and the row reads "Not On-Chain" forever, silently.
func TestStatusWireLiterals(t *testing.T) {
	cases := []struct {
		got  store.Status
		want string
	}{
		{store.StatusPending, "pending"},
		{store.StatusProcessing, "processing"},
		{store.StatusCompleted, "completed"},
		{store.StatusFailed, "failed"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("status literal = %q, want %q", string(c.got), c.want)
		}
	}

	all := []string{
		string(store.StatusPending),
		string(store.StatusProcessing),
		string(store.StatusCompleted),
		string(store.StatusFailed),
	}
	sort.Strings(all)
	want := []string{"completed", "failed", "pending", "processing"}
	if len(all) != len(want) {
		t.Fatalf("status count = %d, want %d", len(all), len(want))
	}
	for i := range want {
		if all[i] != want[i] {
			t.Fatalf("sorted statuses = %v, want %v", all, want)
		}
	}
}

// TestStatusValid is the allowlist behind the ?status= query parameter. The
// TypeScript silently drops an unknown value and answers 200 with unfiltered
// results; the Go API returns 400, which needs this to be exact.
func TestStatusValid(t *testing.T) {
	valid := []store.Status{
		store.StatusPending, store.StatusProcessing,
		store.StatusCompleted, store.StatusFailed,
	}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("Status(%q).Valid() = false, want true", string(s))
		}
	}

	invalid := []store.Status{
		"", "PENDING", "Pending", "pending ", " pending", "unknown",
		"arc-accepted", "mined", "0", "pending,failed",
	}
	for _, s := range invalid {
		if s.Valid() {
			t.Errorf("Status(%q).Valid() = true, want false", string(s))
		}
	}
}

// TestChainStatusWireLiterals pins the four chain-detail literals. These are a
// different column and a different type from Status; the SPA never sees them.
func TestChainStatusWireLiterals(t *testing.T) {
	cases := []struct {
		got  store.ChainStatus
		want string
	}{
		{store.ChainARCAccepted, "arc-accepted"},
		{store.ChainUnmined, "unmined"},
		{store.ChainMined, "mined"},
		{store.ChainAborted, "aborted"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("chain status literal = %q, want %q", string(c.got), c.want)
		}
		// The POSITIVE half of Valid(). Without it the invalid loop below is
		// satisfied by a Valid() that returns false for everything, including all
		// four real statuses — which would make the whole enum unusable and this
		// test green.
		if !c.got.Valid() {
			t.Errorf("ChainStatus(%q).Valid() = false, want true", string(c.got))
		}
	}

	invalid := []store.ChainStatus{"", "ARC-ACCEPTED", "arc_accepted", "pending", "unknown"}
	for _, c := range invalid {
		if c.Valid() {
			t.Errorf("ChainStatus(%q).Valid() = true, want false", string(c))
		}
	}
}

// TestValidTextRejectsOnlyWhatPostgresCannotBind is the single definition of
// "bindable text" in this module.
//
// MEASURED against postgres:17-alpine with pgx v5.10.0: binding a string
// containing 0x00, OR any malformed UTF-8 byte sequence, to even `SELECT
// $1::text` fails with `invalid byte sequence for encoding "UTF8": ...
// (SQLSTATE 22021)`. That includes a bare high bit ("\x80"), an out-of-range
// byte ("\xff"), a truncated multibyte sequence ("\xe2\x98"), a lone UTF-16
// surrogate re-encoded as bytes ("\xed\xa0\x80"), and an overlong encoding of
// NUL ("\xc0\x80"). The extended query protocol prevents INJECTION; it does
// not make the value legal. Without this gate `GET /api/stations?search=%00`
// and `GET /api/weather/%80` are both 500s, because Go's query and path
// decoding hand the raw bytes straight through without validating them.
//
// Nothing else is rejected. A tab, a newline, an emoji and any well-formed
// UTF-8 string are all bindable, and rejecting them would break real search
// terms.
func TestValidTextRejectsOnlyWhatPostgresCannotBind(t *testing.T) {
	bad := []string{
		"\x00", "\x00nul", "nul\x00", "a\x00b", string([]byte{0}),
		"\xff", "\x80", "\xe2\x98", "\xed\xa0\x80", "\xc0\x80",
	}
	for _, s := range bad {
		if err := store.ValidText(s); !errors.Is(err, store.ErrInvalidText) {
			t.Errorf("ValidText(%q) = %v, want store.ErrInvalidText", s, err)
		}
	}

	ok := []string{
		"", " ", "bristol", "1001", "\t\n", "Ünïcödé", "🌦",
		"'; DROP TABLE stations; --", strings.Repeat("a", 4096),
	}
	for _, s := range ok {
		if err := store.ValidText(s); err != nil {
			t.Errorf("ValidText(%q) = %v, want nil", s, err)
		}
	}
}

// TestStatsTotalDataPoints proves the multiplier comes from
// weather.DataFieldsPerRecord and is not a second hand-written 33.
func TestStatsTotalDataPoints(t *testing.T) {
	cases := []struct {
		records int64
		want    int64
	}{
		{0, 0},
		{1, 33},
		{7, 231},
		{3178, 104874},
	}
	for _, c := range cases {
		s := store.Stats{TotalRecords: c.records}
		if got := s.TotalDataPoints(); got != c.want {
			t.Errorf("Stats{TotalRecords: %d}.TotalDataPoints() = %d, want %d", c.records, got, c.want)
		}
	}
}

// TestNullableTimestampsArePointers is a compile-time-shaped guard on the rule
// that every nullable timestamp is a *time.Time. A zero time.Time marshals to
// "0001-01-01T00:00:00Z", which the frontend renders as 01/01/0001 where it
// intends an em dash, so the API layer needs a real nil to project to null.
func TestNullableTimestampsArePointers(t *testing.T) {
	var r store.Record
	if r.ClaimedAt != nil || r.MinedAt != nil || r.ProcessedAt != nil {
		t.Fatal("zero Record must have nil nullable timestamps")
	}
	var st store.Station
	if st.LastReading != nil || st.LastTemp != nil || st.LastBlockHeight != nil {
		t.Fatal("zero Station must have nil nullable fields")
	}
	var s store.Stats
	if s.LastRecordWrite != nil {
		t.Fatal("zero Stats must have a nil LastRecordWrite")
	}
	// Assigning through the pointers must compile and round-trip.
	now := time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)
	r.ProcessedAt = &now
	if !r.ProcessedAt.Equal(now) {
		t.Fatal("ProcessedAt did not round-trip")
	}
}
