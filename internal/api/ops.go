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
	// "nothing happened".
	//
	// It is DERIVED FROM THE SNAPSHOT AT REQUEST TIME, not a static list: a
	// static var passed the round-trip once but never noticed the field going
	// live, because nothing tied its contents to snap.AbortedCount actually
	// being nonzero. "abortedCount" is included here if and only if
	// snap.AbortedCount == 0. The frozen store.RecordStore has no method today
	// that can write chain_status 'aborted' (B1's handover, item 7), so in the
	// shipped code AbortedCount is always 0 and this is always ["abortedCount"]
	// — but the moment Plan C widens RecordStore, wires the write path AND a
	// snapshot actually observes a nonzero count, this marker removes itself
	// with no code change required and no chance of a human forgetting to
	// delete a stale entry.
	//
	// Never omitted and never null: it is built with make(..., 0, ...) even
	// when it ends up empty, so an all-live snapshot still emits "stale":[]
	// rather than dropping the key or emitting null — the same "present and
	// explicit" rule the rest of this response follows.
	//
	// Rendering a permanently-zero field as if it were live is worse than
	// omitting it, because a zero reads as an affirmative all-clear to both an
	// operator and an alarm author.
	Stale []string `json:"stale"`
}

// staleAbortedCountName is the one spelling of the field name shared by the
// response and the derivation below, so the two can never drift.
const staleAbortedCountName = "abortedCount"

// staleOpsFields derives the stale marker list from the values actually
// observed in snap. Today AbortedCount is structurally always 0, so this
// always returns ["abortedCount"] in the shipped code — but the derivation
// is what makes that conditional on the DATA rather than on a static
// constant nobody is obligated to update.
func staleOpsFields(snap store.Snapshot) []string {
	stale := make([]string, 0, 1)
	if snap.AbortedCount == 0 {
		stale = append(stale, staleAbortedCountName)
	}
	return stale
}

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

			Stale: staleOpsFields(snap),
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
