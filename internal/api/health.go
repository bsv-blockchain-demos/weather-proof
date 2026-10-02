package api

import (
	"context"
	"net/http"
	"time"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

// readyPingTimeout bounds handleReady's call to Pinger.Ping. The brief's
// interface sketch shows this as a const, but a const cannot be reassigned by
// a test's t.Cleanup, and TestReadyPingCarriesADeadline needs to shorten it to
// stay fast and deterministic. It is a package var instead, for that reason
// alone.
var readyPingTimeout = 2 * time.Second

// healthResponse is spec §11.2's exact two keys and nothing else — the same
// shape the TypeScript returns at src/api/index.ts:45-54.
type healthResponse struct {
	Status    string    `json:"status"`
	Timestamp isoMillis `json:"timestamp"`
}

// readyResponse is /api/ready's body on both outcomes. It carries no detail
// about WHY a ping failed: readiness output can end up in a public probe log,
// and a driver error's Error() string can carry a hostname or DSN fragment.
type readyResponse struct {
	Status string `json:"status"`
}

// readyStatusUnavailable is the one spelling of the 503 body's status value,
// shared by the body and the log line so the two cannot drift.
const readyStatusUnavailable = "unavailable"

// handleHealth serves GET /api/health: LIVENESS ONLY, and PROCESS-ONLY. No DB
// ping, no wallet check, no fuel state, unconditionally 200 while the process
// is up.
//
// Why liveness must not touch the DB: Postgres is a single-replica
// strategy:Recreate Deployment, so every node drain, image bump or restart
// makes it briefly unreachable. If liveness pinged the DB, kubelet would
// restart the app pod on EVERY one of those events — converting an accepted
// seconds-long read outage into a CrashLoopBackOff that ALSO stops the
// wallet-free Tempest poller, dropping weather observations for a DB event that
// has nothing to do with polling. A DB outage must remove the pod from the
// Service (readiness), never restart it (liveness).
func handleHealth(now func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, r, http.StatusOK, healthResponse{
			Status:    "ok",
			Timestamp: isoValue(now()),
		})
	}
}

// handleReady serves GET /api/ready: a fast Pinger.Ping, 503 when the database
// is unreachable and 200 otherwise. The ping carries its own short timeout so a
// wedged pool cannot make the readiness probe itself hang past kubelet's
// timeout, which kubelet scores as a failure anyway but with a far less useful
// log line.
func handleReady(p store.Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readyPingTimeout)
		defer cancel()

		// The ping failure is LOGGED even though the body carries no detail and
		// no request id: readiness is the one 5xx in the package whose body is
		// readyResponse rather than an error DTO, so it does not pass through
		// writeErrorCause and without this line the cause of a flapping probe
		// exists nowhere. logServerError attaches the same request id the
		// response's X-Request-Id header carries.
		if pingErr := p.Ping(ctx); pingErr != nil {
			logServerError(r, http.StatusServiceUnavailable, readyStatusUnavailable, pingErr)
			writeJSON(w, r, http.StatusServiceUnavailable, readyResponse{Status: readyStatusUnavailable})
			return
		}
		writeJSON(w, r, http.StatusOK, readyResponse{Status: "ok"})
	}
}
