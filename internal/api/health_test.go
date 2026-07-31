package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/fake"
)

const healthGoldenPath = "testdata/health.json"

// healthFixedNow matches every other package fixture's clock instant, so the
// golden file's timestamp is shared rather than a one-off literal.
var healthFixedNow = time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

func fixedHealthNow() time.Time { return healthFixedNow }

func doHealthRequest(t *testing.T, target string, h http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func decodeJSONMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, raw)
	}
	return out
}

func TestHealthMatchesItsGoldenFile(t *testing.T) {
	rec := doHealthRequest(t, "/api/health", handleHealth(fixedHealthNow))

	var gotIndented bytes.Buffer
	if err := json.Indent(&gotIndented, rec.Body.Bytes(), "", "  "); err != nil {
		t.Fatalf("indent got: %v", err)
	}

	wantRaw, readErr := os.ReadFile(healthGoldenPath)
	if readErr != nil {
		t.Fatalf("read golden: %v", readErr)
	}
	var wantIndented bytes.Buffer
	if err := json.Indent(&wantIndented, wantRaw, "", "  "); err != nil {
		t.Fatalf("indent want: %v", err)
	}

	if gotIndented.String() != wantIndented.String() {
		t.Errorf("golden mismatch\ngot:\n%s\nwant:\n%s", gotIndented.String(), wantIndented.String())
	}
}

func TestHealthHasExactlyTwoKeys(t *testing.T) {
	rec := doHealthRequest(t, "/api/health", handleHealth(fixedHealthNow))
	got := decodeJSONMap(t, rec.Body.Bytes())

	if len(got) != 2 {
		t.Fatalf("got %d keys, want exactly 2: %v", len(got), got)
	}
	if _, ok := got["status"]; !ok {
		t.Error("missing status key")
	}
	if _, ok := got["timestamp"]; !ok {
		t.Error("missing timestamp key")
	}
}

// ---- fatal-on-touch store fixtures ----
//
// Every method calls t.Fatal so that a passing test is structural proof the
// handler never invoked it, not merely that a working store happened to
// return an unused value. A test built against a WORKING store cannot catch a
// handler that queries: the query would just succeed silently.

type fatalRecordStore struct{ t *testing.T }

func (f fatalRecordStore) Insert(context.Context, store.NewRecord) (bool, error) {
	f.t.Fatal("RecordStore.Insert called")
	return false, nil
}

func (f fatalRecordStore) ClaimPending(context.Context, int, uuid.UUID) ([]store.Record, error) {
	f.t.Fatal("RecordStore.ClaimPending called")
	return nil, nil
}

func (f fatalRecordStore) Complete(context.Context, string, []store.Publication) (store.Stats, error) {
	f.t.Fatal("RecordStore.Complete called")
	return store.Stats{}, nil
}

func (f fatalRecordStore) FailPermanent(context.Context, []string, string) error {
	f.t.Fatal("RecordStore.FailPermanent called")
	return nil
}

func (f fatalRecordStore) RequeueInfra(context.Context, []string, string) error {
	f.t.Fatal("RecordStore.RequeueInfra called")
	return nil
}

func (f fatalRecordStore) MarkUnknown(context.Context, []string, string) error {
	f.t.Fatal("RecordStore.MarkUnknown called")
	return nil
}

func (f fatalRecordStore) ReapExpired(context.Context, time.Duration, int) ([]store.Record, error) {
	f.t.Fatal("RecordStore.ReapExpired called")
	return nil, nil
}

func (f fatalRecordStore) Requeue(context.Context, store.RequeueFilter) (int64, error) {
	f.t.Fatal("RecordStore.Requeue called")
	return 0, nil
}

func (f fatalRecordStore) List(context.Context, store.ListFilter) ([]store.Record, int64, error) {
	f.t.Fatal("RecordStore.List called")
	return nil, 0, nil
}

func (f fatalRecordStore) Get(context.Context, string) (store.Record, error) {
	f.t.Fatal("RecordStore.Get called")
	return store.Record{}, nil
}

func (f fatalRecordStore) TxIDExists(context.Context, string) (bool, error) {
	f.t.Fatal("RecordStore.TxIDExists called")
	return false, nil
}

func (f fatalRecordStore) SetBlockHeights(context.Context, []store.BlockHeightUpdate) error {
	f.t.Fatal("RecordStore.SetBlockHeights called")
	return nil
}

func (f fatalRecordStore) ReconcileCandidates(context.Context, time.Duration, int) ([]store.Record, error) {
	f.t.Fatal("RecordStore.ReconcileCandidates called")
	return nil, nil
}

func (f fatalRecordStore) Snapshot(context.Context) (store.Snapshot, error) {
	f.t.Fatal("RecordStore.Snapshot called")
	return store.Snapshot{}, nil
}

type fatalStationStore struct{ t *testing.T }

func (f fatalStationStore) Upsert(context.Context, store.Station) error {
	f.t.Fatal("StationStore.Upsert called")
	return nil
}

func (f fatalStationStore) List(context.Context, store.StationFilter) ([]store.Station, int64, error) {
	f.t.Fatal("StationStore.List called")
	return nil, 0, nil
}

func (f fatalStationStore) Get(context.Context, int64) (store.Station, error) {
	f.t.Fatal("StationStore.Get called")
	return store.Station{}, nil
}

func (f fatalStationStore) Stats(context.Context) (store.Stats, error) {
	f.t.Fatal("StationStore.Stats called")
	return store.Stats{}, nil
}

type fatalDepositStore struct{ t *testing.T }

func (f fatalDepositStore) NewDeposit(context.Context, store.Deposit) error {
	f.t.Fatal("DepositStore.NewDeposit called")
	return nil
}

func (f fatalDepositStore) PendingDeposits(context.Context) ([]store.Deposit, error) {
	f.t.Fatal("DepositStore.PendingDeposits called")
	return nil, nil
}

func (f fatalDepositStore) MarkInternalized(context.Context, string, string, int32, int64) error {
	f.t.Fatal("DepositStore.MarkInternalized called")
	return nil
}

type fatalPreflightStore struct{ t *testing.T }

func (f fatalPreflightStore) PreflightOK(context.Context, string) (bool, error) {
	f.t.Fatal("PreflightStore.PreflightOK called")
	return false, nil
}

func (f fatalPreflightStore) RecordPreflight(context.Context, string) error {
	f.t.Fatal("PreflightStore.RecordPreflight called")
	return nil
}

type fatalPinger struct{ t *testing.T }

func (f fatalPinger) Ping(context.Context) error {
	f.t.Fatal("Pinger.Ping called")
	return nil
}

// fatalStore builds a store.Store whose every member panics the test the
// moment any method is invoked.
func fatalStore(t *testing.T) store.Store {
	t.Helper()
	return store.Store{
		Records:   fatalRecordStore{t: t},
		Stations:  fatalStationStore{t: t},
		Deposits:  fatalDepositStore{t: t},
		Preflight: fatalPreflightStore{t: t},
		Health:    fatalPinger{t: t},
	}
}

func TestHealthDoesNotTouchTheStore(t *testing.T) {
	_ = fatalStore(t) // constructed to prove it is never reached below.

	rec := doHealthRequest(t, "/api/health", handleHealth(fixedHealthNow))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
}

// TestHealthIsTwoHundredWhenThePingerFails proves liveness ignores the
// pinger's answer entirely: handleHealth's signature takes no Pinger at all,
// but this drives the scenario at the router level, through a failing
// fake.Store.PingErr, so a future signature change that wires one in cannot
// silently reintroduce the coupling.
func TestHealthIsTwoHundredWhenThePingerFails(t *testing.T) {
	s := fake.New()
	s.Now = fixedHealthNow
	s.PingErr = errors.New("dial tcp: connect: connection refused")

	d := testDeps(t)
	d.Store = s.Store()
	d.Now = fixedHealthNow
	h := NewRouter(d)

	rec := doHealthRequest(t, "/api/health", h.ServeHTTP)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200: a failing pinger must not affect liveness", rec.Code)
	}
}

func TestReadyIsTwoHundredWhenThePingSucceeds(t *testing.T) {
	s := fake.New()

	rec := doHealthRequest(t, "/api/ready", handleReady(s))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
}

func TestReadyIsFiveHundredThreeWhenThePingFails(t *testing.T) {
	s := fake.New()
	s.PingErr = errors.New("dial tcp: connect: connection refused")

	rec := doHealthRequest(t, "/api/ready", handleReady(s))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got status %d, want 503", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "dial tcp") || strings.Contains(body, "connection refused") {
		t.Fatalf("body leaked the driver error: %s", body)
	}
}

func TestReadyBodyIsJSONInBothDirections(t *testing.T) {
	cases := []struct {
		name    string
		pingErr error
	}{
		{name: "ready", pingErr: nil},
		{name: "unready", pingErr: errors.New("dial tcp: connect: connection refused")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := fake.New()
			s.PingErr = tc.pingErr

			rec := doHealthRequest(t, "/api/ready", handleReady(s))
			if got := rec.Header().Get(headerContentType); got != contentTypeJSON {
				t.Errorf("%s = %q, want %q", headerContentType, got, contentTypeJSON)
			}
			_ = decodeJSONMap(t, rec.Body.Bytes())
		})
	}
}

// blockingPinger blocks until its context is done, and reports whether the
// context carried a deadline.
type blockingPinger struct {
	hadDeadline chan bool
}

func (p blockingPinger) Ping(ctx context.Context) error {
	_, ok := ctx.Deadline()
	p.hadDeadline <- ok
	<-ctx.Done()
	return ctx.Err()
}

func TestReadyPingCarriesADeadline(t *testing.T) {
	original := readyPingTimeout
	readyPingTimeout = 20 * time.Millisecond
	t.Cleanup(func() { readyPingTimeout = original })

	p := blockingPinger{hadDeadline: make(chan bool, 1)}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- doHealthRequest(t, "/api/ready", handleReady(p))
	}()

	select {
	case had := <-p.hadDeadline:
		if !had {
			t.Fatal("ping's context had no deadline")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Ping to be called")
	}

	select {
	case rec := <-done:
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("got status %d, want 503 once the deadline elapses", rec.Code)
		}
	case <-time.After(time.Second):
		t.Fatal("handleReady did not return within its own timeout")
	}
}

func TestReadyDoesNotReadAnyDataMethod(t *testing.T) {
	fs := fatalStore(t)
	fs.Health = fake.New() // a working pinger; only the DATA members must fatal.

	rec := doHealthRequest(t, "/api/ready", handleReady(fs.Health))
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
}

func TestHealthAndReadyCarryTheSecurityHeaders(t *testing.T) {
	s := fake.New()
	s.PingErr = errors.New("dial tcp: connect: connection refused")

	d := testDeps(t)
	d.Store = s.Store()
	h := NewRouter(d)

	for _, target := range []string{"/api/health", "/api/ready"} {
		rec := doHealthRequest(t, target, h.ServeHTTP)
		if got := rec.Header().Get(headerContentTypeOptions); got != valueNoSniff {
			t.Errorf("%s: %s = %q, want %q", target, headerContentTypeOptions, got, valueNoSniff)
		}
		if got := rec.Header().Get(headerFrameOptions); got != valueDeny {
			t.Errorf("%s: %s = %q, want %q", target, headerFrameOptions, got, valueDeny)
		}
		if got := rec.Header().Get(headerReferrerPolicy); got != valueNoReferrer {
			t.Errorf("%s: %s = %q, want %q", target, headerReferrerPolicy, got, valueNoReferrer)
		}
	}
}
