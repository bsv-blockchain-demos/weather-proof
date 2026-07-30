package api

import (
	"time"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
)

// blockchainDTO is never null and always carries all three keys — spec §13.1
// crash-list item 3: record.blockchain.txid is accessed unguarded, so a null
// blockchain throws inside render and white-screens the page. TxID,
// OutputIndex and BlockHeight are each null until known, which is fine; the
// OBJECT is not optional.
type blockchainDTO struct {
	TxID        *string `json:"txid"`
	OutputIndex *int32  `json:"outputIndex"`
	BlockHeight *int64  `json:"blockHeight"`
}

// weatherItem is one element of GET /api/weather's items array. It carries NO
// error key: the TS list route does not emit one and nothing in the list UI
// reads it (spec §13.2). Keep the asymmetry with weatherDetail.
//
// Data is a weather.WeatherData VALUE and not a pointer, and it is not
// embedded: the wire shape nests it under "data", and embeddedstructfieldcheck
// would additionally require an embedded field to come first.
//
// Status is r.Status DIRECTLY. B1 already stores exactly the four wire
// literals and spec §13.8's projection table is the identity on all four
// rows. The chain-level richness lives in r.ChainStatus, which no DTO
// carries. A switch translating them would be dead code and would need to
// satisfy exhaustive; this comment is why nobody adds one.
type weatherItem struct {
	ID          string              `json:"id"`
	StationID   int64               `json:"stationId"`
	Timestamp   isoMillis           `json:"timestamp"`
	Data        weather.WeatherData `json:"data"`
	Blockchain  blockchainDTO       `json:"blockchain"`
	Status      store.Status        `json:"status"`
	CreatedAt   isoMillis           `json:"createdAt"`
	ProcessedAt *isoMillis          `json:"processedAt"`
}

// weatherDetail is GET /api/weather/{id}'s BARE body — no envelope. It is
// weatherItem plus a trailing error, per spec §13.5. The duplication of the
// eight shared fields is deliberate: embedding weatherItem would nest the
// shared fields under a key, and a json-inlined embed would put "error" in a
// position the spec's shape does not have.
type weatherDetail struct {
	ID          string              `json:"id"`
	StationID   int64               `json:"stationId"`
	Timestamp   isoMillis           `json:"timestamp"`
	Data        weather.WeatherData `json:"data"`
	Blockchain  blockchainDTO       `json:"blockchain"`
	Status      store.Status        `json:"status"`
	CreatedAt   isoMillis           `json:"createdAt"`
	ProcessedAt *isoMillis          `json:"processedAt"`
	Error       *string             `json:"error"`
}

// stationSummary is one element of GET /api/stations' stations array AND the
// entire bare body of GET /api/stations/{stationId} — spec §13.4 says the
// detail response is the same nine fields, unwrapped. ONE DTO serves both.
//
// Status is the derived online/offline string and NOT store.Status. The
// frontend compares it strictly against the literal "online".
//
// LastTemp is *float64 and omitempty is FORBIDDEN on it: spec §13.1 crash-list
// item 2. The frontend tests station.lastTemp !== null; an omitted key is
// undefined, undefined !== null is true, and the UI renders the literal
// string "undefined°C".
//
// TxRecords is a non-null int64 — crash-list item 1: station.txRecords
// .toLocaleString() is not optional-chained.
type stationSummary struct {
	StationID       int64      `json:"stationId"`
	Name            string     `json:"name"`
	Location        string     `json:"location"`
	Status          string     `json:"status"`
	LastReading     *isoMillis `json:"lastReading"`
	LastTemp        *float64   `json:"lastTemp"`
	LastConditions  string     `json:"lastConditions"`
	TxRecords       int64      `json:"txRecords"`
	LastBlockHeight *int64     `json:"lastBlockHeight"`
}

// statsDTO is the four-key dashboard object. The SAME four keys are the SSE
// stats_update payload (spec §13.7), because the frontend replaces the whole
// stats slice wholesale — so there is one type, not two.
type statsDTO struct {
	ActiveStations  int64      `json:"activeStations"`
	TotalTx         int64      `json:"totalTx"`
	LastRecordWrite *isoMillis `json:"lastRecordWrite"`
	TotalDataPoints int64      `json:"totalDataPoints"`
}

// paginationDTO is the page envelope shared by every paginated list response.
type paginationDTO struct {
	Page       int   `json:"page"`
	Limit      int   `json:"limit"`
	Total      int64 `json:"total"`
	TotalPages int64 `json:"totalPages"`
}

// errorDTO is every non-2xx body. RequestID is a *string so the 400 and 404
// bodies the spec pins verbatim ({"error":"Weather record not found"}) do not
// grow a second key, while every 500 carries one.
type errorDTO struct {
	Error     string  `json:"error"`
	RequestID *string `json:"request_id"`
}

const (
	statusOnline  = "online"
	statusOffline = "offline"
)

// toBlockchain projects a record's on-chain placement, always as a non-nil
// object with all three keys. Each pointer field is CLONED rather than
// copied: copying r.TxID directly would let a later mutation through the
// caller's own pointer change an already-projected DTO's marshaled bytes,
// since neither this function nor its caller takes a *store.Record — the
// DTO must not retain a pointer into anything the caller still holds.
func toBlockchain(r store.Record) blockchainDTO {
	return blockchainDTO{
		TxID:        clonePtr(r.TxID),
		OutputIndex: clonePtr(r.OutputIndex),
		BlockHeight: clonePtr(r.BlockHeight),
	}
}

// clonePtr returns a new pointer to a copy of *p, or nil if p is nil.
func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// toWeatherItem projects one list-view record. See the doc comment on
// weatherItem for why Status is r.Status directly.
func toWeatherItem(r store.Record) weatherItem {
	return weatherItem{
		ID:          r.ID,
		StationID:   r.StationID,
		Timestamp:   isoValue(r.Timestamp),
		Data:        r.Data,
		Blockchain:  toBlockchain(r),
		Status:      r.Status,
		CreatedAt:   isoValue(r.CreatedAt),
		ProcessedAt: isoPtr(r.ProcessedAt),
	}
}

// toWeatherDetail projects the single-record detail view: weatherItem plus a
// trailing error.
func toWeatherDetail(r store.Record) weatherDetail {
	return weatherDetail{
		ID:          r.ID,
		StationID:   r.StationID,
		Timestamp:   isoValue(r.Timestamp),
		Data:        r.Data,
		Blockchain:  toBlockchain(r),
		Status:      r.Status,
		CreatedAt:   isoValue(r.CreatedAt),
		ProcessedAt: isoPtr(r.ProcessedAt),
		Error:       clonePtr(r.Error),
	}
}

// stationStatus derives spec §13.3's strict online literal: online iff
// s.IsActive AND s.LastReading is within 3 × pollRate of now. The freshness
// window lives here rather than in the store so the poll rate does not leak
// into persistence.
func stationStatus(s store.Station, now time.Time, pollRate time.Duration) string {
	if !s.IsActive || s.LastReading == nil {
		return statusOffline
	}
	if now.Sub(*s.LastReading) <= 3*pollRate {
		return statusOnline
	}
	return statusOffline
}

// toStationSummary projects a station row into the wire shape shared by the
// stations list and the station detail endpoint.
func toStationSummary(s store.Station, now time.Time, pollRate time.Duration) stationSummary {
	return stationSummary{
		StationID:       s.StationID,
		Name:            s.Name,
		Location:        s.Location,
		Status:          stationStatus(s, now, pollRate),
		LastReading:     isoPtr(s.LastReading),
		LastTemp:        clonePtr(s.LastTemp),
		LastConditions:  s.LastConditions,
		TxRecords:       s.TxRecords,
		LastBlockHeight: clonePtr(s.LastBlockHeight),
	}
}

// toStats projects the four-value dashboard summary. TotalDataPoints goes
// through store.Stats.TotalDataPoints so the field-count multiplier is never
// duplicated as a literal.
func toStats(s store.Stats) statsDTO {
	return statsDTO{
		ActiveStations:  s.ActiveStations,
		TotalTx:         s.TotalTx,
		LastRecordWrite: isoPtr(s.LastRecordWrite),
		TotalDataPoints: s.TotalDataPoints(),
	}
}

// newPagination derives the page envelope. totalPages is 0 when total is 0 —
// otherwise the frontend's Next button never disables (spec §13.2).
func newPagination(page, limit int, total int64) paginationDTO {
	if total == 0 {
		return paginationDTO{Page: page, Limit: limit, Total: total, TotalPages: 0}
	}
	if limit <= 0 {
		limit = 1
	}
	totalPages := (total + int64(limit) - 1) / int64(limit)
	return paginationDTO{Page: page, Limit: limit, Total: total, TotalPages: totalPages}
}
