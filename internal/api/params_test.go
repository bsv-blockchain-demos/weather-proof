package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	q, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("ParseQuery(%q): %v", raw, err)
	}
	return q
}

// --- clampInt ---

func TestClampIntTable(t *testing.T) {
	cases := []struct {
		v, lo, hi, want int
	}{
		{5, 1, 10, 5},
		{0, 1, 10, 1},
		{11, 1, 10, 10},
		{1, 1, 10, 1},
		{10, 1, 10, 10},
		{-100, 1, 10, 1},
	}
	for _, c := range cases {
		if got := clampInt(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("clampInt(%d,%d,%d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

// --- parsePage ---

func TestParsePageDefaultsToOneWhenAbsent(t *testing.T) {
	if got := parsePage(mustQuery(t, ""), weatherLimitDef); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
}

func TestParsePageDefaultsToOneWhenEmpty(t *testing.T) {
	if got := parsePage(mustQuery(t, "page="), weatherLimitDef); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
}

func TestParsePageDefaultsToOneWhenUnparseable(t *testing.T) {
	inputs := []string{"page=abc", "page=1.5", "page=0x10", "page=" + strings.Repeat("9", 40)}
	for _, in := range inputs {
		if got := parsePage(mustQuery(t, in), weatherLimitDef); got != 1 {
			t.Errorf("parsePage(%q) = %d, want 1", in, got)
		}
	}
}

func TestParsePageClampsBelowOne(t *testing.T) {
	for _, in := range []string{"page=0", "page=-5"} {
		if got := parsePage(mustQuery(t, in), weatherLimitDef); got != 1 {
			t.Errorf("parsePage(%q) = %d, want 1", in, got)
		}
	}
}

func TestParsePageEchoesAValidPage(t *testing.T) {
	if got := parsePage(mustQuery(t, "page=7"), 20); got != 7 {
		t.Fatalf("got %d, want 7", got)
	}
}

func TestParsePageCapsAtTheOffsetCeiling(t *testing.T) {
	got := parsePage(mustQuery(t, "page=1000000"), weatherLimitDef)
	want := maxOffset / weatherLimitDef
	if got != want {
		t.Fatalf("got %d, want %d", got, want)
	}

	// Same ceiling rule against the stations list's own limit, so the
	// derivation is proven for both callers rather than only the one this
	// task happens to exercise first.
	got = parsePage(mustQuery(t, "page=1000000"), stationsLimitDef)
	want = maxOffset / stationsLimitDef
	if got != want {
		t.Fatalf("got %d, want %d", got, want)
	}
}

// --- parseLimit ---

func TestParseLimitDefaultsWhenAbsentEmptyOrUnparseable(t *testing.T) {
	for _, in := range []string{"", "limit=", "limit=abc"} {
		if got := parseLimit(mustQuery(t, in), 20, 100); got != 20 {
			t.Errorf("parseLimit(%q) = %d, want 20", in, got)
		}
	}
}

func TestParseLimitClampsToItsRange(t *testing.T) {
	cases := []struct {
		in       string
		def, max int
		want     int
	}{
		{"limit=0", 20, 100, 1},
		{"limit=-1", 20, 100, 1},
		{"limit=101", 20, 100, 100},
		{"limit=100", 20, 100, 100},
		{"limit=1", 20, 100, 1},
	}
	for _, c := range cases {
		if got := parseLimit(mustQuery(t, c.in), c.def, c.max); got != c.want {
			t.Errorf("parseLimit(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseLimitUsesTheStationsDefaultsToo(t *testing.T) {
	if got := parseLimit(mustQuery(t, "limit=201"), stationsLimitDef, stationsLimitMax); got != 200 {
		t.Fatalf("got %d, want 200", got)
	}
	if got := parseLimit(mustQuery(t, ""), stationsLimitDef, stationsLimitMax); got != 50 {
		t.Fatalf("got %d, want 50", got)
	}
}

// --- parseStatus ---

func TestParseStatusAcceptsEachOfTheFourLiterals(t *testing.T) {
	cases := map[string]store.Status{
		"pending":    store.StatusPending,
		"processing": store.StatusProcessing,
		"completed":  store.StatusCompleted,
		"failed":     store.StatusFailed,
	}
	for lit, want := range cases {
		t.Run(lit, func(t *testing.T) {
			got, err := parseStatus(mustQuery(t, "status="+lit))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil || *got != want {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}

func TestParseStatusIsNilWhenAbsentOrEmpty(t *testing.T) {
	for _, in := range []string{"", "status="} {
		got, err := parseStatus(mustQuery(t, in))
		if err != nil || got != nil {
			t.Errorf("parseStatus(%q) = (%v, %v), want (nil, nil)", in, got, err)
		}
	}
}

func TestParseStatusRejectsAnUnknownValue(t *testing.T) {
	for _, in := range []string{"status=Completed", "status=COMPLETED", "status=done", "status=completed%20"} {
		_, err := parseStatus(mustQuery(t, in))
		var br badRequestError
		if !errors.As(err, &br) {
			t.Errorf("parseStatus(%q) error = %v, want badRequest", in, err)
		}
	}
}

func TestParseStatusRejectsRatherThanSilentlyDropping(t *testing.T) {
	got, err := parseStatus(mustQuery(t, "status=done"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if got != nil {
		t.Fatalf("expected nil filter alongside the error, got %v", got)
	}
}

// --- parseStationID (query) ---

func TestParseStationIDIsNilWhenAbsentEmptyOrUnparseable(t *testing.T) {
	for _, in := range []string{"", "stationId=", "stationId=abc"} {
		got, err := parseStationID(mustQuery(t, in))
		if err != nil || got != nil {
			t.Errorf("parseStationID(%q) = (%v, %v), want (nil, nil)", in, got, err)
		}
	}
}

func TestParseStationIDAcceptsAValue(t *testing.T) {
	got, err := parseStationID(mustQuery(t, "stationId=12345"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || *got != 12345 {
		t.Fatalf("got %v, want 12345", got)
	}
}

func TestParseStationIDRejectsAnInt32Overflow(t *testing.T) {
	for _, in := range []string{"stationId=2147483648", "stationId=-2147483649"} {
		_, err := parseStationID(mustQuery(t, in))
		var br badRequestError
		if !errors.As(err, &br) {
			t.Errorf("parseStationID(%q) error = %v, want badRequest", in, err)
		}
	}
}

func TestParseStationIDAcceptsTheInt32Boundaries(t *testing.T) {
	for _, in := range []string{"stationId=2147483647", "stationId=-2147483648"} {
		_, err := parseStationID(mustQuery(t, in))
		if err != nil {
			t.Errorf("parseStationID(%q) unexpected error: %v", in, err)
		}
	}
}

// --- parsePathStationID ---

func TestParsePathStationIDRejectsWhatTheQueryParameterTolerates(t *testing.T) {
	for _, in := range []string{"abc", "", "12x"} {
		_, err := parsePathStationID(in)
		var br badRequestError
		if !errors.As(err, &br) {
			t.Errorf("parsePathStationID(%q) error = %v, want badRequest", in, err)
		}
	}
}

func TestParsePathStationIDAcceptsAValue(t *testing.T) {
	got, err := parsePathStationID("12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 12345 {
		t.Fatalf("got %d, want 12345", got)
	}
}

// --- parseSearch ---

func TestParseSearchTrims(t *testing.T) {
	got, err := parseSearch(mustQuery(t, "search=%20brix%20"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "brix" {
		t.Fatalf("got %q, want %q", got, "brix")
	}
}

func TestParseSearchIsEmptyWhenAbsent(t *testing.T) {
	got, err := parseSearch(mustQuery(t, ""))
	if err != nil || got != "" {
		t.Fatalf("got (%q, %v), want (\"\", nil)", got, err)
	}
}

func TestParseSearchRejectsANULByte(t *testing.T) {
	for _, in := range []string{"search=%00", "search=a%00b"} {
		_, err := parseSearch(mustQuery(t, in))
		var br badRequestError
		if !errors.As(err, &br) {
			t.Errorf("parseSearch(%q) error = %v, want badRequest", in, err)
		}
	}
}

func TestParseSearchRejectsMalformedUTF8(t *testing.T) {
	for _, in := range []string{"search=%80", "search=%ff"} {
		_, err := parseSearch(mustQuery(t, in))
		var br badRequestError
		if !errors.As(err, &br) {
			t.Errorf("parseSearch(%q) error = %v, want badRequest", in, err)
		}
	}
}

func TestParseSearchAcceptsTabsNewlinesAndUnicode(t *testing.T) {
	japanese := "\u65e5\u672c"
	cases := map[string]string{
		"search=a%09b":                        "a\tb",
		"search=a%0Ab":                        "a\nb",
		"search=A%C3%B1ejo":                   "A\u00f1ejo",
		"search=" + url.QueryEscape(japanese): japanese,
	}
	for in, want := range cases {
		got, err := parseSearch(mustQuery(t, in))
		if err != nil {
			t.Errorf("parseSearch(%q) unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseSearch(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSearchAcceptsTheNumericShortcutInput(t *testing.T) {
	got, err := parseSearch(mustQuery(t, "search=0"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "0" {
		t.Fatalf("got %q, want %q", got, "0")
	}
}

// --- parseRecordID ---

func TestParseRecordIDRejectsAnEmptySegment(t *testing.T) {
	_, err := parseRecordID("")
	var br badRequestError
	if !errors.As(err, &br) {
		t.Fatalf("error = %v, want badRequest", err)
	}
}

func TestParseRecordIDRejectsANULByteAndMalformedUTF8(t *testing.T) {
	for _, in := range []string{"\x00", "a\x00", "\x80"} {
		_, err := parseRecordID(in)
		var br badRequestError
		if !errors.As(err, &br) {
			t.Errorf("parseRecordID(%q) error = %v, want badRequest", in, err)
		}
	}
}

func TestParseRecordIDPassesAnOpaqueIDThrough(t *testing.T) {
	for _, in := range []string{"01970000-aaaa-7000-8000-000000000001", "not-a-uuid", "abc/def"} {
		got, err := parseRecordID(in)
		if err != nil {
			t.Errorf("parseRecordID(%q) unexpected error: %v", in, err)
			continue
		}
		if got != in {
			t.Errorf("parseRecordID(%q) = %q, want verbatim", in, got)
		}
	}
}

// --- statusForStoreError ---

func TestStatusForStoreErrorMapsTheThreeClasses(t *testing.T) {
	status, _ := statusForStoreError(store.ErrNotFound)
	if status != http.StatusNotFound {
		t.Errorf("ErrNotFound status = %d, want 404", status)
	}
	status, _ = statusForStoreError(store.ErrConflict)
	if status != http.StatusConflict {
		t.Errorf("ErrConflict status = %d, want 409", status)
	}
	status, msg := statusForStoreError(errors.New("boom"))
	if status != http.StatusInternalServerError {
		t.Errorf("generic error status = %d, want 500", status)
	}
	if msg != msgInternal {
		t.Errorf("generic error msg = %q, want %q", msg, msgInternal)
	}
}

func TestStatusForStoreErrorUnwrapsAWrappedSentinel(t *testing.T) {
	wrapped := fmt.Errorf("query records: %w", store.ErrNotFound)
	status, _ := statusForStoreError(wrapped)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

func TestStatusForStoreErrorNeverEchoesTheErrorText(t *testing.T) {
	src := errors.New(`SQLSTATE 23505: duplicate key value violates unique constraint "weather_records_pkey"`)
	_, msg := statusForStoreError(src)
	for _, forbidden := range []string{"SQLSTATE", "weather_records_pkey", "23505"} {
		if strings.Contains(msg, forbidden) {
			t.Fatalf("message %q leaks %q", msg, forbidden)
		}
	}
}

// --- writeJSON / writeError ---

func TestWriteJSONSetsContentTypeBeforeTheStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	writeJSON(rec, req, http.StatusNotFound, clientErrorDTO{Error: "not found"})
	if got := rec.Header().Get(headerContentType); got != contentTypeJSON {
		t.Fatalf("Content-Type = %q, want %q", got, contentTypeJSON)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestWriteErrorOmitsRequestIDBelowFiveHundred(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	writeError(rec, req, http.StatusBadRequest, "bad input")

	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["request_id"]; ok {
		t.Fatalf("400 body must not carry a request_id key at all: %s", rec.Body.String())
	}
	want := `{"error":"bad input"}`
	if strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("body = %s, want %s", rec.Body.String(), want)
	}
}

func TestWriteErrorCarriesRequestIDOnFiveHundred(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	ctx := context.WithValue(req.Context(), requestIDContextKey{}, "01970000-bbbb-7000-8000-000000000002")
	req = req.WithContext(ctx)

	writeError(rec, req, http.StatusInternalServerError, msgInternal)

	var got serverErrorDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.RequestID != "01970000-bbbb-7000-8000-000000000002" {
		t.Fatalf("request_id = %q, want the seeded id", got.RequestID)
	}
}
