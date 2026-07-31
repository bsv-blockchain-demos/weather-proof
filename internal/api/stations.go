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
