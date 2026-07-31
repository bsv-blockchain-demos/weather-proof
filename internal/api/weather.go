package api

import (
	"errors"
	"net/http"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

// weatherListResponse is GET /api/weather's envelope. Items is never nil —
// data?.items is read without a fallback in the list hook, and a JSON null
// there is an empty-array render at best and a TypeError at worst.
type weatherListResponse struct {
	Items      []weatherItem `json:"items"`
	Pagination paginationDTO `json:"pagination"`
}

// handleWeatherList serves GET /api/weather. It does NOT re-sort: the store
// returns created_at DESC, id DESC and that order is what the SPA renders by
// default (sortKey initializes to null and sortedRecords returns records
// unchanged, so the default view is exactly server order).
func handleWeatherList(recs store.RecordStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		limit := parseLimit(q, weatherLimitDef, weatherLimitMax)
		page := parsePage(q, limit)

		status, statusErr := parseStatus(q)
		if statusErr != nil {
			var badReq badRequestError
			if errors.As(statusErr, &badReq) {
				writeError(w, r, http.StatusBadRequest, badReq.Error())
				return
			}
			writeErrorCause(w, r, http.StatusInternalServerError, msgInternal, statusErr)
			return
		}

		stationID, stationErr := parseStationID(q)
		if stationErr != nil {
			var badReq badRequestError
			if errors.As(stationErr, &badReq) {
				writeError(w, r, http.StatusBadRequest, badReq.Error())
				return
			}
			writeErrorCause(w, r, http.StatusInternalServerError, msgInternal, stationErr)
			return
		}

		f := store.ListFilter{
			StationID: stationID,
			Status:    status,
			Limit:     limit,
			Offset:    (page - 1) * limit,
		}

		records, total, listErr := recs.List(r.Context(), f)
		if listErr != nil {
			writeStoreError(w, r, listErr)
			return
		}

		items := make([]weatherItem, 0, len(records))
		for _, rec := range records {
			items = append(items, toWeatherItem(rec))
		}

		writeJSON(w, r, http.StatusOK, weatherListResponse{
			Items:      items,
			Pagination: newPagination(page, limit, total),
		})
	}
}

// msgWeatherNotFound is spec §13.5's verbatim 404 body message.
const msgWeatherNotFound = "Weather record not found"

// handleWeatherDetail serves GET /api/weather/{id}. The body is a BARE
// weatherDetail — no envelope (spec §13.5).
//
// The status discipline is the point of this handler: the TypeScript let an
// unparseable id become a Mongoose CastError caught into a 500 while the
// frontend only special-cases 404. Go returns 400 for a syntactically invalid
// id and 404 for an unknown one, and NEVER 500 for either.
func handleWeatherDetail(recs store.RecordStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, idErr := parseRecordID(r.PathValue("id"))
		if idErr != nil {
			var badReq badRequestError
			if errors.As(idErr, &badReq) {
				writeError(w, r, http.StatusBadRequest, badReq.Error())
				return
			}
			writeErrorCause(w, r, http.StatusInternalServerError, msgInternal, idErr)
			return
		}

		rec, getErr := recs.Get(r.Context(), id)
		if getErr != nil {
			if errors.Is(getErr, store.ErrNotFound) {
				writeError(w, r, http.StatusNotFound, msgWeatherNotFound)
				return
			}
			writeStoreError(w, r, getErr)
			return
		}

		writeJSON(w, r, http.StatusOK, toWeatherDetail(rec))
	}
}
