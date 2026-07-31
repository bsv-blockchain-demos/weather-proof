package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

// stationListResponse is spec §13.3's three required top-level keys, read as
// data?.stations ?? [], data?.stats and data?.pagination.
type stationListResponse struct {
	Stats      statsDTO         `json:"stats"`
	Stations   []stationSummary `json:"stations"`
	Pagination paginationDTO    `json:"pagination"`
}

// handleStationList serves GET /api/stations. now and pollRate are injected
// rather than read from the clock and config inside the handler, so the
// online/offline boundary is testable and the golden file is byte-stable.
func handleStationList(sts store.StationStore, now func() time.Time, pollRate time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		limit := parseLimit(q, stationsLimitDef, stationsLimitMax)
		page := parsePage(q, limit)

		search, searchErr := parseSearch(q)
		if searchErr != nil {
			var badReq badRequestError
			if errors.As(searchErr, &badReq) {
				writeError(w, r, http.StatusBadRequest, badReq.Error())
				return
			}
			writeError(w, r, http.StatusInternalServerError, msgInternal)
			return
		}

		f := store.StationFilter{
			Search: search,
			Limit:  limit,
			Offset: (page - 1) * limit,
		}

		stats, statsErr := sts.Stats(r.Context())
		if statsErr != nil {
			httpStatus, msg := statusForStoreError(statsErr)
			writeError(w, r, httpStatus, msg)
			return
		}

		stations, total, listErr := sts.List(r.Context(), f)
		if listErr != nil {
			httpStatus, msg := statusForStoreError(listErr)
			writeError(w, r, httpStatus, msg)
			return
		}

		instant := now()
		summaries := make([]stationSummary, 0, len(stations))
		for _, st := range stations {
			summaries = append(summaries, toStationSummary(st, instant, pollRate))
		}

		writeJSON(w, r, http.StatusOK, stationListResponse{
			Stats:      toStats(stats),
			Stations:   summaries,
			Pagination: newPagination(page, limit, total),
		})
	}
}

// msgStationNotFound is spec §13.4's 404 message. It is ours, not a value
// the frontend inspects (it special-cases only the status), but it is a
// named constant so the golden and the handler cannot disagree.
const msgStationNotFound = "Station not found"

// handleStationDetail serves GET /api/stations/{stationId}. The body is a
// BARE stationSummary — the same nine fields as one element of the list,
// unwrapped (spec §13.4). ONE DTO serves both, so the list's non-null
// discipline holds here even though the detail page reads every field
// through optional chaining.
func handleStationDetail(sts store.StationStore, now func() time.Time, pollRate time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stationID, idErr := parsePathStationID(r.PathValue("stationId"))
		if idErr != nil {
			var badReq badRequestError
			if errors.As(idErr, &badReq) {
				writeError(w, r, http.StatusBadRequest, badReq.Error())
				return
			}
			writeError(w, r, http.StatusInternalServerError, msgInternal)
			return
		}

		st, getErr := sts.Get(r.Context(), stationID)
		if getErr != nil {
			if errors.Is(getErr, store.ErrNotFound) {
				writeError(w, r, http.StatusNotFound, msgStationNotFound)
				return
			}
			httpStatus, msg := statusForStoreError(getErr)
			writeError(w, r, httpStatus, msg)
			return
		}

		writeJSON(w, r, http.StatusOK, toStationSummary(st, now(), pollRate))
	}
}
