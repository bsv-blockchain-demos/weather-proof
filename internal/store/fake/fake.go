// Package fake is an in-memory store.Store for tests that must not touch a
// database.
//
// WHAT THIS FAKE IS NOT EVIDENCE FOR. Every method here runs under one mutex,
// so N concurrent ClaimPending calls trivially return disjoint rows. That
// means the fake PASSES a zero-double-claim assertion while proving nothing
// whatsoever about the SQL, and a claim-concurrency test written against it is
// worse than no test at all: it reports a guarantee that does not exist. The
// real claim is a single UPDATE whose inner SELECT takes row locks with
// FOR UPDATE SKIP LOCKED, and the only place that can be tested is
// internal/store/postgres against a real server. Keep concurrency assertions
// physically out of this package.
package fake

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

// fixedNow is the default clock instant. It is fixed rather than time.Now so
// that a golden-file test over an HTTP handler produces byte-identical output
// on every run.
var fixedNow = time.Date(2026, 4, 17, 15, 40, 0, 0, time.UTC)

// Store is an in-memory implementation of every store interface.
type Store struct {
	// Now is the clock. Tests that assert on timestamps should set it.
	Now func() time.Time

	// FailAll, when non-nil, is returned by every method except Ping. It is
	// how a caller tests the store-error paths of an HTTP handler.
	FailAll error

	// PingErr is returned by Ping. It is how a caller tests /api/ready's 503.
	PingErr error

	mu        sync.Mutex
	records   map[string]store.Record
	stations  map[int64]store.Station
	deposits  map[string]store.Deposit
	preflight map[string]time.Time
	totalTx   int64
	totalRecs int64
	lastWrite *time.Time
	seenTx    map[string]struct{}
}

// New returns an empty fake with the default fixed clock.
func New() *Store {
	return &Store{
		Now:       func() time.Time { return fixedNow },
		records:   make(map[string]store.Record),
		stations:  make(map[int64]store.Station),
		deposits:  make(map[string]store.Deposit),
		preflight: make(map[string]time.Time),
		seenTx:    make(map[string]struct{}),
	}
}

// stationsAdapter re-exposes the station methods under the names
// store.StationStore requires.
//
// *Store cannot carry both Get(ctx, string) and Get(ctx, int64), nor both
// List(ctx, ListFilter) and List(ctx, StationFilter), so the station methods on
// *Store are named GetStation and ListStations and this adapter renames them.
// It is the same asymmetry that forces store.Store to be a struct of interfaces
// rather than one composed interface — an embedded version does not compile.
type stationsAdapter struct{ s *Store }

func (a stationsAdapter) Upsert(ctx context.Context, st store.Station) error {
	return a.s.Upsert(ctx, st)
}

func (a stationsAdapter) List(ctx context.Context, f store.StationFilter) ([]store.Station, int64, error) {
	return a.s.ListStations(ctx, f)
}

func (a stationsAdapter) Get(ctx context.Context, stationID int64) (store.Station, error) {
	return a.s.GetStation(ctx, stationID)
}

func (a stationsAdapter) Stats(ctx context.Context) (store.Stats, error) {
	return a.s.Stats(ctx)
}

// Stations returns the fake's store.StationStore view.
func (s *Store) Stations() store.StationStore { return stationsAdapter{s: s} }

// Store returns the aggregate seam with every member wired to s.
func (s *Store) Store() store.Store {
	return store.Store{
		Records:   s,
		Stations:  s.Stations(),
		Deposits:  s,
		Preflight: s,
		Health:    s,
	}
}

// SeedRecord inserts r verbatim, bypassing Insert's dedupe and defaults. It is
// for constructing a specific lifecycle state a test needs to observe.
func (s *Store) SeedRecord(r store.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Status == "" {
		r.Status = store.StatusPending
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = s.Now()
	}
	s.records[r.ID] = r
	if r.TxID != nil {
		s.seenTx[*r.TxID] = struct{}{}
	}
}

// SeedStation inserts st verbatim.
func (s *Store) SeedStation(st store.Station) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stations[st.StationID] = st
}

// Ping implements store.Pinger.
func (s *Store) Ping(_ context.Context) error { return s.PingErr }

// Insert implements store.RecordStore.
//
// The two collision cases are deliberately DIFFERENT, and they match the SQL:
// the ON CONFLICT arbiter covers (station_id, observation_time), so a repeated
// observation is the intended steady state and reports (false, nil), while a
// repeated id collides with the PRIMARY KEY, which the arbiter does not cover,
// and surfaces as store.ErrConflict.
func (s *Store) Insert(_ context.Context, r store.NewRecord) (bool, error) {
	if s.FailAll != nil {
		return false, s.FailAll
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.records {
		if existing.StationID == r.StationID && existing.ObservationTime.Equal(r.ObservationTime) {
			return false, nil
		}
	}
	if _, clash := s.records[r.ID]; clash {
		return false, store.ErrConflict
	}
	s.records[r.ID] = store.Record{
		ID:              r.ID,
		StationID:       r.StationID,
		Timestamp:       r.Timestamp,
		ObservationTime: r.ObservationTime,
		Data:            r.Data,
		Status:          store.StatusPending,
		CreatedAt:       s.Now(),
	}
	return true, nil
}

// clampLimit is the fake's single enforcement point for interfaces.go's
// frozen non-positive-limit rule, mirroring postgres.clampLimit's contract
// exactly: a non-positive limit means "return zero rows (or a zero count)
// with a nil error," never "unbounded." ok reports whether the caller
// should proceed with the returned (always positive) limit.
//
// It exists here for the SAME reason it exists in the postgres package: two
// separate one-line reimplementations of "is n positive" already diverged
// from the rule INSIDE this exact package, caught only by a later review
// rather than by a test any of the six prescribed fixtures exercised.
// ReapExpired's old `if len(out) == limit { break }` loop guard can never
// equal a NEGATIVE limit, so it reaped (and mutated!) every stranded row
// instead of none — while limit == 0 happened to work, by the same loop
// coincidentally starting len(out) at 0. Requeue's old
// `if f.Limit > 0 && len(match) > f.Limit` skipped truncation for ANY
// non-positive limit, zero included, so both its DryRun count and its real
// write touched every matched row regardless of Limit. Every limit-taking
// method in this package now calls this one function instead of writing its
// own comparison, exactly as postgres.clampLimit's own doc comment demands
// of that package.
func clampLimit(n int) (limit int, ok bool) {
	if n <= 0 {
		return 0, false
	}
	return n, true
}

// ClaimPending implements store.RecordStore.
//
// n is clamped by clampLimit — see its doc comment for why this is the
// package's single enforcement point rather than an inline check here.
func (s *Store) ClaimPending(_ context.Context, n int, ref uuid.UUID) ([]store.Record, error) {
	if s.FailAll != nil {
		return nil, s.FailAll
	}
	n, ok := clampLimit(n)
	if !ok {
		return []store.Record{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	pending := make([]store.Record, 0, len(s.records))
	for _, r := range s.records {
		if r.Status == store.StatusPending {
			pending = append(pending, r)
		}
	}
	sortOldestFirst(pending)

	now := s.Now()
	out := make([]store.Record, 0, n)
	for i := range pending {
		if len(out) == n {
			break
		}
		r := pending[i]
		r.Status = store.StatusProcessing
		claimedAt := now
		r.ClaimedAt = &claimedAt
		if r.ClaimRef == nil {
			claimRef := ref
			r.ClaimRef = &claimRef
		}
		s.records[r.ID] = r
		out = append(out, r)
	}
	return cloneRecords(out), nil
}

// Complete implements store.RecordStore.
func (s *Store) Complete(ctx context.Context, txID string, pubs []store.Publication) (store.Stats, error) {
	if s.FailAll != nil {
		return store.Stats{}, s.FailAll
	}
	s.mu.Lock()
	now := s.Now()
	moved := int64(0)
	arc := store.ChainARCAccepted
	for _, p := range pubs {
		r, ok := s.records[p.RecordID]
		if !ok || r.Status != store.StatusProcessing {
			continue
		}
		r.Status = store.StatusCompleted
		txid := txID
		r.TxID = &txid
		vout := p.OutputIndex
		r.OutputIndex = &vout
		chain := arc
		r.ChainStatus = &chain
		processedAt := now
		r.ProcessedAt = &processedAt
		r.ClaimedAt = nil
		r.AdoptRequired = false
		r.Error = nil
		s.records[r.ID] = r
		moved++

		// A MISSING station row is skipped, never created. bumpStationsSQL is an
		// UPDATE that simply matches nothing, and a fake that invented the row
		// instead would let a B2 test assert a station that Postgres would not
		// have. last_temp and last_conditions advance only with a NEWER reading,
		// which is the same CASE the SQL carries.
		st, ok := s.stations[r.StationID]
		if !ok {
			continue
		}
		st.TxRecords++
		if st.LastReading == nil || st.LastReading.Before(r.Timestamp) {
			reading := r.Timestamp
			st.LastReading = &reading
			temp := float64(r.Data.AirTemperature)
			st.LastTemp = &temp
			st.LastConditions = r.Data.Conditions
		}
		s.stations[r.StationID] = st
	}
	if moved > 0 {
		s.totalTx++
		s.totalRecs += moved
		write := now
		s.lastWrite = &write
		s.seenTx[txID] = struct{}{}
	}
	s.mu.Unlock()
	return s.Stats(ctx)
}

// FailPermanent implements store.RecordStore.
func (s *Store) FailPermanent(_ context.Context, ids []string, reason string) error {
	return s.transition(ids, func(r *store.Record) {
		r.Status = store.StatusFailed
		r.Attempts++
		processedAt := s.Now()
		r.ProcessedAt = &processedAt
		r.ClaimedAt = nil
		r.AdoptRequired = false
		// reasonCopy, not &reason: apply runs once per matched id, and reason is
		// the enclosing method's single parameter, so &reason would be the SAME
		// address on every row this call touches — every record it marks would
		// share one *string, and mutating one record's Error through a pointer
		// would silently corrupt every sibling from this same batch.
		reasonCopy := reason
		r.Error = &reasonCopy
	})
}

// RequeueInfra implements store.RecordStore.
func (s *Store) RequeueInfra(_ context.Context, ids []string, reason string) error {
	return s.transition(ids, func(r *store.Record) {
		r.Status = store.StatusPending
		r.ClaimedAt = nil
		r.AdoptRequired = false
		reasonCopy := reason // see FailPermanent: apply runs once per id.
		r.Error = &reasonCopy
	})
}

// MarkUnknown implements store.RecordStore.
func (s *Store) MarkUnknown(_ context.Context, ids []string, reason string) error {
	return s.transition(ids, func(r *store.Record) {
		r.Status = store.StatusPending
		r.ClaimedAt = nil
		r.AdoptRequired = true
		reasonCopy := reason // see FailPermanent: apply runs once per id.
		r.Error = &reasonCopy
	})
}

// ReapExpired implements store.RecordStore.
//
// limit is clamped by clampLimit — see its doc comment for why this is the
// package's single enforcement point rather than an inline check here. The
// clamp runs BEFORE the lock is taken and before any row is read, so a
// non-positive limit never mutates a single row — unlike the old
// `if len(out) == limit { break }` loop guard, which could never equal a
// negative limit and so reaped (and mutated) every stale row for one.
func (s *Store) ReapExpired(_ context.Context, lease time.Duration, limit int) ([]store.Record, error) {
	if s.FailAll != nil {
		return nil, s.FailAll
	}
	limit, ok := clampLimit(limit)
	if !ok {
		return []store.Record{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := s.Now().Add(-lease)
	stale := make([]store.Record, 0, len(s.records))
	for _, r := range s.records {
		if r.Status == store.StatusProcessing && r.ClaimedAt != nil && r.ClaimedAt.Before(cutoff) {
			stale = append(stale, r)
		}
	}
	// claimed_at ASC, id ASC: one ClaimPending call stamps a whole batch with a
	// single claimed_at, so ties are the COMMON case here, not the exception.
	// Without the id tiebreak, sort.Slice is not stable and iteration over
	// s.records is randomized, so which rows a limit-truncated reap returns
	// would be non-deterministic and would disagree with reapExpiredSQL.
	sort.Slice(stale, func(i, j int) bool {
		if !stale[i].ClaimedAt.Equal(*stale[j].ClaimedAt) {
			return stale[i].ClaimedAt.Before(*stale[j].ClaimedAt)
		}
		return stale[i].ID < stale[j].ID
	})

	out := make([]store.Record, 0, len(stale))
	for i := range stale {
		if len(out) == limit {
			break
		}
		r := stale[i]
		r.Status = store.StatusPending
		r.AdoptRequired = true
		s.records[r.ID] = r
		out = append(out, r)
	}
	return cloneRecords(out), nil
}

// Requeue implements store.RecordStore.
//
// f.Limit is clamped by clampLimit — see its doc comment for why this is the
// package's single enforcement point rather than an inline check here. The
// clamp applies identically to both the DryRun count and the real write
// (both return before either the count or the mutation loop runs), so
// neither path can disagree with the other about a non-positive limit. The
// old `if f.Limit > 0 && len(match) > f.Limit` guard skipped truncation
// entirely for ANY non-positive limit, so both paths touched every matched
// row regardless of Limit.
func (s *Store) Requeue(_ context.Context, f store.RequeueFilter) (int64, error) {
	if s.FailAll != nil {
		return 0, s.FailAll
	}
	limit, ok := clampLimit(f.Limit)
	if !ok {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := s.Now().Add(-f.Since)
	match := make([]store.Record, 0, len(s.records))
	for _, r := range s.records {
		if r.Status != f.Status || !r.CreatedAt.Before(cutoff) {
			continue
		}
		if f.StationID != nil && r.StationID != *f.StationID {
			continue
		}
		match = append(match, r)
	}
	sortOldestFirst(match)
	if len(match) > limit {
		match = match[:limit]
	}
	if f.DryRun {
		return int64(len(match)), nil
	}
	for i := range match {
		r := match[i]
		r.Status = store.StatusPending
		r.AdoptRequired = true
		s.records[r.ID] = r
	}
	return int64(len(match)), nil
}

// List implements store.RecordStore.
func (s *Store) List(_ context.Context, f store.ListFilter) ([]store.Record, int64, error) {
	if s.FailAll != nil {
		return nil, 0, s.FailAll
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	match := make([]store.Record, 0, len(s.records))
	for _, r := range s.records {
		if f.StationID != nil && r.StationID != *f.StationID {
			continue
		}
		if f.Status != nil && r.Status != *f.Status {
			continue
		}
		match = append(match, r)
	}
	sortNewestFirst(match)

	// f.Limit is clamped by clampLimit — see its doc comment for why this is
	// the package's single enforcement point rather than an inline check
	// here. As in postgres.RecordStore.List, a clamped limit does NOT
	// short-circuit the whole method: Total must still report the full
	// unpaged count of matching rows even when the page is empty.
	total := int64(len(match))
	limit, ok := clampLimit(f.Limit)
	if !ok {
		return []store.Record{}, total, nil
	}
	if f.Offset >= len(match) {
		return []store.Record{}, total, nil
	}
	end := f.Offset + limit
	if end > len(match) {
		end = len(match)
	}
	page := make([]store.Record, 0, end-f.Offset)
	page = append(page, match[f.Offset:end]...)
	return cloneRecords(page), total, nil
}

// Get implements store.RecordStore.
func (s *Store) Get(_ context.Context, id string) (store.Record, error) {
	if s.FailAll != nil {
		return store.Record{}, s.FailAll
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return store.Record{}, store.ErrNotFound
	}
	return cloneRecord(r), nil
}

// TxIDExists implements store.RecordStore.
func (s *Store) TxIDExists(_ context.Context, txID string) (bool, error) {
	if s.FailAll != nil {
		return false, s.FailAll
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seenTx[txID]
	return ok, nil
}

// SetBlockHeights implements store.RecordStore.
func (s *Store) SetBlockHeights(_ context.Context, ups []store.BlockHeightUpdate) error {
	if s.FailAll != nil {
		return s.FailAll
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	mined := store.ChainMined
	for _, u := range ups {
		for id, r := range s.records {
			if r.TxID == nil || *r.TxID != u.TxID || r.Status != store.StatusCompleted {
				continue
			}
			height := u.BlockHeight
			r.BlockHeight = &height
			chain := mined
			r.ChainStatus = &chain
			if u.MinedAt != nil {
				minedAt := *u.MinedAt
				r.MinedAt = &minedAt
			} else {
				minedAt := s.Now()
				r.MinedAt = &minedAt
			}
			s.records[id] = r

			// As in Complete: setStationHeightSQL is an UPDATE, so a station
			// with no row is skipped rather than invented.
			st, ok := s.stations[r.StationID]
			if !ok {
				continue
			}
			if st.LastBlockHeight == nil || *st.LastBlockHeight < height {
				h := height
				st.LastBlockHeight = &h
			}
			s.stations[r.StationID] = st
		}
	}
	return nil
}

// ReconcileCandidates implements store.RecordStore.
//
// limit is clamped by clampLimit — see its doc comment for why this is the
// package's single enforcement point rather than an inline check here. The
// old `if limit > 0 && len(out) > limit` guard skipped truncation for any
// non-positive limit and returned every candidate instead of none.
func (s *Store) ReconcileCandidates(_ context.Context, olderThan time.Duration, limit int) ([]store.Record, error) {
	if s.FailAll != nil {
		return nil, s.FailAll
	}
	limit, ok := clampLimit(limit)
	if !ok {
		return []store.Record{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := s.Now().Add(-olderThan)
	out := make([]store.Record, 0, len(s.records))
	for _, r := range s.records {
		if r.Status != store.StatusCompleted || r.ProcessedAt == nil || !r.ProcessedAt.Before(cutoff) {
			continue
		}
		if r.ChainStatus != nil && *r.ChainStatus == store.ChainMined {
			continue
		}
		out = append(out, r)
	}
	// processed_at ASC, id ASC, matching reconcileCandidatesSQL: Complete stamps
	// a whole batch with a single processed_at, so ties are common, and without
	// the id tiebreak a limit-truncated result would be non-deterministic.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ProcessedAt.Equal(*out[j].ProcessedAt) {
			return out[i].ProcessedAt.Before(*out[j].ProcessedAt)
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return cloneRecords(out), nil
}

// Snapshot implements store.RecordStore.
func (s *Store) Snapshot(_ context.Context) (store.Snapshot, error) {
	if s.FailAll != nil {
		return store.Snapshot{}, s.FailAll
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := s.Now().Add(-time.Hour)
	var snap store.Snapshot
	for _, r := range s.records {
		switch r.Status {
		case store.StatusPending:
			snap.PendingRows++
		case store.StatusProcessing:
			snap.ProcessingRows++
		case store.StatusFailed:
			snap.FailedRows++
		case store.StatusCompleted:
			notMined := r.ChainStatus == nil || *r.ChainStatus != store.ChainMined
			if notMined && r.ProcessedAt != nil && r.ProcessedAt.Before(cutoff) {
				snap.StillUnminedOlderThan1h++
			}
		}
		if r.ChainStatus != nil {
			switch *r.ChainStatus {
			case store.ChainMined:
				snap.MinedCount++
			case store.ChainAborted:
				snap.AbortedCount++
			case store.ChainARCAccepted, store.ChainUnmined:
			}
		}
	}
	return snap, nil
}

// Upsert implements store.StationStore.
func (s *Store) Upsert(_ context.Context, in store.Station) error {
	if s.FailAll != nil {
		return s.FailAll
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.stations[in.StationID]
	if !ok {
		in.CreatedAt = s.Now()
		in.UpdatedAt = s.Now()
		s.stations[in.StationID] = in
		return nil
	}
	existing.Name = in.Name
	existing.Location = in.Location
	existing.Latitude = in.Latitude
	existing.Longitude = in.Longitude
	existing.IsActive = in.IsActive
	existing.UpdatedAt = s.Now()
	s.stations[in.StationID] = existing
	return nil
}

// GetStation is store.StationStore.Get under a non-clashing name; the
// stationsAdapter above renames it. See that type's comment for why.
func (s *Store) GetStation(_ context.Context, stationID int64) (store.Station, error) {
	if s.FailAll != nil {
		return store.Station{}, s.FailAll
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.stations[stationID]
	if !ok {
		return store.Station{}, store.ErrNotFound
	}
	return cloneStation(st), nil
}

// ListStations implements store.StationStore.List.
//
// The search branch is not decoration and must not be dropped: B2's whole
// ?search= test suite runs against this fake, and a fake that ignored f.Search
// would let every one of those tests pass while proving nothing. It reproduces
// the CONTRACT the SQL has — one parsed value drives the branch, a NUL byte
// matches nothing rather than erroring — and deliberately not the ranking,
// which is websearch_to_tsquery's and cannot be imitated honestly. Task 19's
// conformance suite asserts the shared part against both implementations.
func (s *Store) ListStations(_ context.Context, f store.StationFilter) ([]store.Station, int64, error) {
	if s.FailAll != nil {
		return nil, 0, s.FailAll
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	match := make([]store.Station, 0, len(s.stations))
	for _, st := range s.stations {
		if !stationMatches(st, f.Search) {
			continue
		}
		match = append(match, st)
	}
	sort.Slice(match, func(i, j int) bool { return match[i].StationID < match[j].StationID })

	// f.Limit is clamped by clampLimit — same single enforcement point as
	// RecordStore.List. See that method's doc comment for why a clamped limit
	// does not short-circuit the whole method: Total must still report the
	// full unpaged count.
	total := int64(len(match))
	limit, ok := clampLimit(f.Limit)
	if !ok {
		return []store.Station{}, total, nil
	}
	if f.Offset >= len(match) {
		return []store.Station{}, total, nil
	}
	end := f.Offset + limit
	if end > len(match) {
		end = len(match)
	}
	page := make([]store.Station, 0, end-f.Offset)
	page = append(page, match[f.Offset:end]...)
	return cloneStations(page), total, nil
}

// Stats implements store.StationStore.
func (s *Store) Stats(_ context.Context) (store.Stats, error) {
	if s.FailAll != nil {
		return store.Stats{}, s.FailAll
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	active := int64(0)
	for _, st := range s.stations {
		if st.IsActive {
			active++
		}
	}
	out := store.Stats{
		ActiveStations: active,
		TotalTx:        s.totalTx,
		TotalRecords:   s.totalRecs,
	}
	if s.lastWrite != nil {
		write := *s.lastWrite
		out.LastRecordWrite = &write
	}
	return out, nil
}

// NewDeposit implements store.DepositStore.
func (s *Store) NewDeposit(_ context.Context, d store.Deposit) error {
	if s.FailAll != nil {
		return s.FailAll
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.deposits[d.Suffix]; ok {
		return store.ErrConflict
	}
	d.CreatedAt = s.Now()
	s.deposits[d.Suffix] = d
	return nil
}

// PendingDeposits implements store.DepositStore.
func (s *Store) PendingDeposits(_ context.Context) ([]store.Deposit, error) {
	if s.FailAll != nil {
		return nil, s.FailAll
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.Deposit, 0, len(s.deposits))
	for _, d := range s.deposits {
		if d.InternalizedAt == nil {
			out = append(out, d)
		}
	}
	// created_at ASC, suffix ASC, matching pendingDepositsSQL: suffix is only
	// the TIEBREAKER, not the primary sort key, so this diverges from Postgres
	// any time suffix-alphabetical order differs from creation order, even
	// without a tie.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].Suffix < out[j].Suffix
	})
	return out, nil
}

// MarkInternalized implements store.DepositStore.
func (s *Store) MarkInternalized(_ context.Context, suffix, txID string, vout int32, sats int64) error {
	if s.FailAll != nil {
		return s.FailAll
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.deposits[suffix]
	if !ok {
		return store.ErrNotFound
	}
	txid := txID
	d.TxID = &txid
	v := vout
	d.Vout = &v
	amount := sats
	d.Satoshis = &amount
	at := s.Now()
	d.InternalizedAt = &at
	s.deposits[suffix] = d
	return nil
}

// PreflightOK implements store.PreflightStore.
func (s *Store) PreflightOK(_ context.Context, fingerprint string) (bool, error) {
	if s.FailAll != nil {
		return false, s.FailAll
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.preflight[fingerprint]
	return ok, nil
}

// RecordPreflight implements store.PreflightStore.
func (s *Store) RecordPreflight(_ context.Context, fingerprint string) error {
	if s.FailAll != nil {
		return s.FailAll
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preflight[fingerprint] = s.Now()
	return nil
}

func (s *Store) transition(ids []string, apply func(*store.Record)) error {
	if s.FailAll != nil {
		return s.FailAll
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		r, ok := s.records[id]
		if !ok || r.Status != store.StatusProcessing {
			continue
		}
		apply(&r)
		s.records[id] = r
	}
	return nil
}

// stationMatches is the fake's half of the one-parsed-value search split.
//
// An empty search matches everything. A search that parses as an int64 is an
// EXACT station_id lookup, exactly as strconv.ParseInt routes it in
// StationStore.List — so "0" finds station 0 and "12345abc" does not parse and
// falls through to text. A value store.ValidText rejects matches nothing,
// because Postgres cannot bind it at all and the store answers an empty page
// rather than a 500.
func stationMatches(st store.Station, search string) bool {
	if search == "" {
		return true
	}
	if store.ValidText(search) != nil {
		return false
	}
	if id, err := strconv.ParseInt(search, 10, 64); err == nil {
		return st.StationID == id
	}
	needle := strings.ToLower(search)
	return strings.Contains(strings.ToLower(st.Name), needle) ||
		strings.Contains(strings.ToLower(st.Location), needle)
}

// cloneRecord returns a copy of r whose pointer fields point to freshly
// allocated values, never to the ones s.records still holds. Every method that
// hands a Record to a caller must route it through this helper (or through
// cloneRecords for a slice) on the way out.
//
// Real Postgres always returns freshly scanned values decoded off the wire, so
// a caller can never reach into the server's storage through a pointer a query
// returned. Without this, a Record built by e.g. ClaimPending and stored under
// s.records[r.ID] shares its *time.Time and *uuid.UUID field VALUES with the
// slice element handed back to the caller — same struct, copied twice, but the
// pointer fields inside are the same address both times — so
// `*rec.ClaimedAt = t` silently corrupts the fake's internal state, something
// structurally impossible against a real database connection.
func cloneRecord(r store.Record) store.Record {
	if r.ClaimRef != nil {
		ref := *r.ClaimRef
		r.ClaimRef = &ref
	}
	if r.ClaimedAt != nil {
		claimedAt := *r.ClaimedAt
		r.ClaimedAt = &claimedAt
	}
	if r.TxID != nil {
		txid := *r.TxID
		r.TxID = &txid
	}
	if r.OutputIndex != nil {
		outputIndex := *r.OutputIndex
		r.OutputIndex = &outputIndex
	}
	if r.BlockHeight != nil {
		blockHeight := *r.BlockHeight
		r.BlockHeight = &blockHeight
	}
	if r.ChainStatus != nil {
		chainStatus := *r.ChainStatus
		r.ChainStatus = &chainStatus
	}
	if r.MinedAt != nil {
		minedAt := *r.MinedAt
		r.MinedAt = &minedAt
	}
	if r.Error != nil {
		errText := *r.Error
		r.Error = &errText
	}
	if r.ProcessedAt != nil {
		processedAt := *r.ProcessedAt
		r.ProcessedAt = &processedAt
	}
	return r
}

// cloneRecords applies cloneRecord to every element of recs.
func cloneRecords(recs []store.Record) []store.Record {
	out := make([]store.Record, 0, len(recs))
	for _, r := range recs {
		out = append(out, cloneRecord(r))
	}
	return out
}

// cloneStation is cloneRecord's exact counterpart for Station: it returns a
// copy of st whose pointer fields point to freshly allocated values, never to
// the ones s.stations still holds. Every method that hands a Station to a
// caller must route it through this helper (or through cloneStations for a
// slice) on the way out, for the same reason cloneRecord exists: real
// Postgres always returns freshly scanned values, so `*station.LastReading =
// t` on a Station this fake returned must never be able to reach s.stations.
func cloneStation(st store.Station) store.Station {
	if st.Latitude != nil {
		latitude := *st.Latitude
		st.Latitude = &latitude
	}
	if st.Longitude != nil {
		longitude := *st.Longitude
		st.Longitude = &longitude
	}
	if st.LastReading != nil {
		lastReading := *st.LastReading
		st.LastReading = &lastReading
	}
	if st.LastTemp != nil {
		lastTemp := *st.LastTemp
		st.LastTemp = &lastTemp
	}
	if st.LastBlockHeight != nil {
		lastBlockHeight := *st.LastBlockHeight
		st.LastBlockHeight = &lastBlockHeight
	}
	return st
}

// cloneStations applies cloneStation to every element of stations.
func cloneStations(stations []store.Station) []store.Station {
	out := make([]store.Station, 0, len(stations))
	for _, st := range stations {
		out = append(out, cloneStation(st))
	}
	return out
}

func sortNewestFirst(recs []store.Record) {
	sort.Slice(recs, func(i, j int) bool {
		if !recs[i].CreatedAt.Equal(recs[j].CreatedAt) {
			return recs[i].CreatedAt.After(recs[j].CreatedAt)
		}
		return recs[i].ID > recs[j].ID
	})
}

func sortOldestFirst(recs []store.Record) {
	sort.Slice(recs, func(i, j int) bool {
		if !recs[i].CreatedAt.Equal(recs[j].CreatedAt) {
			return recs[i].CreatedAt.Before(recs[j].CreatedAt)
		}
		return recs[i].ID < recs[j].ID
	})
}
