package api

import (
	"net/http"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

// opsResponse is the sampler snapshot as spec §11.2 serves it on OPS_PORT.
//
// It is deliberately NOT on the API listener. §15.5 path-splits the single
// Ingress on prefix /api, so anything registered on :3001 under /api is
// world-reachable at the public hostname — and this body publishes exactly the
// signal an attacker would use to time abuse of the unauthenticated
// /api/proof and /api/verify amplifiers.
//
// The fuel and wallet fields of the §11.1 heartbeat are NOT here: B2 owns no
// wallet and no keeper, and emitting zeros for them would be the same lie the
// Stale field exists to prevent. Plan C adds them together with the sampler
// that can populate them.
type opsResponse struct {
	PendingRows             int64 `json:"pendingRows"`
	ProcessingRows          int64 `json:"processingRows"`
	FailedRows              int64 `json:"failedRows"`
	StillUnminedOlderThan1h int64 `json:"stillUnminedOlderThan1h"`
	MinedCount              int64 `json:"minedCount"`
	AbortedCount            int64 `json:"abortedCount"`

	ActiveStations int64 `json:"activeStations"`
	TotalTx        int64 `json:"totalTx"`
	SSEClients     int   `json:"sseClients"`

	// Stale names every field above whose value is STRUCTURALLY unreachable in
	// the shipped code, so a zero there means "not implemented" rather than
	// "nothing happened". Today it is exactly ["abortedCount"]: the frozen
	// store.RecordStore has no method that can write chain_status 'aborted'
	// (B1's handover, item 7), so Snapshot.AbortedCount is a bucket that was
	// individually proven wired and that nothing can increment.
	//
	// Rendering a permanently-zero field as if it were live is worse than
	// omitting it, because a zero reads as an affirmative all-clear to both an
	// operator and an alarm author.
	Stale []string `json:"stale"`
}

// staleOpsFields is the list. When Plan C widens RecordStore so an aborted
// transition becomes writable, DELETE the entry — and
// TestOpsStaleListNamesAbortedCount will fail until somebody does, which is
// the point of pinning it exactly.
var staleOpsFields = []string{"abortedCount"}

// handleOps serves GET /api/ops on the OPS listener.
//
// It checks BOTH store errors independently (snapErr, statsErr per the
// package's shadow convention) so a Stats-only failure cannot be masked by a
// successful Snapshot call and answer 200 with a zeroed half.
func handleOps(recs store.RecordStore, sts store.StationStore, h *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snap, snapErr := recs.Snapshot(r.Context())
		if snapErr != nil {
			httpStatus, msg := statusForStoreError(snapErr)
			writeError(w, r, httpStatus, msg)
			return
		}

		stats, statsErr := sts.Stats(r.Context())
		if statsErr != nil {
			httpStatus, msg := statusForStoreError(statsErr)
			writeError(w, r, httpStatus, msg)
			return
		}

		stale := make([]string, len(staleOpsFields))
		copy(stale, staleOpsFields)

		writeJSON(w, r, http.StatusOK, opsResponse{
			PendingRows:             snap.PendingRows,
			ProcessingRows:          snap.ProcessingRows,
			FailedRows:              snap.FailedRows,
			StillUnminedOlderThan1h: snap.StillUnminedOlderThan1h,
			MinedCount:              snap.MinedCount,
			AbortedCount:            snap.AbortedCount,

			ActiveStations: stats.ActiveStations,
			TotalTx:        stats.TotalTx,
			SSEClients:     h.Clients(),

			Stale: stale,
		})
	}
}

// NewOpsRouter builds the ops listener's handler. It is a SEPARATE mux from
// NewRouter's with no limiter and no read routes, which is what makes /api/ops
// exempt by construction rather than by registration order.
//
// It takes the hub from d.Hub (added to Deps in Task 18) rather than as a
// second parameter, so the two routers cannot be handed different hubs and
// report different sseClients counts.
func NewOpsRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	hub := d.Hub
	if hub == nil {
		hub = NewHub(sseGlobalMax, sseConcurrentPerIP, d.Logger)
	}

	mux.HandleFunc(pathOps, onlyGET(handleOps(d.Store.Records, d.Store.Stations, hub)))
	mux.HandleFunc("/", notFoundJSON)

	return withRequestID(withSecurityHeaders(withRecover(d.Logger)(withWriteDeadline(d.Logger)(mux))))
}
