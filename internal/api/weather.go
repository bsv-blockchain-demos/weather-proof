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
			writeError(w, r, http.StatusInternalServerError, msgInternal)
			return
		}

		stationID, stationErr := parseStationID(q)
		if stationErr != nil {
			var badReq badRequestError
			if errors.As(stationErr, &badReq) {
				writeError(w, r, http.StatusBadRequest, badReq.Error())
				return
			}
			writeError(w, r, http.StatusInternalServerError, msgInternal)
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
			httpStatus, msg := statusForStoreError(listErr)
			writeError(w, r, httpStatus, msg)
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
