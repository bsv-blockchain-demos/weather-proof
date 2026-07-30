package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

// contentTypeJSON and headerContentType are declared once so every response
// path shares the literal rather than repeating the string past goconst's
// ten-occurrence threshold.
const (
	contentTypeJSON   = "application/json"
	headerContentType = "Content-Type"
)

// msgInternal is the fixed 500 body message. It never varies: the whole
// point of statusForStoreError's default branch is that no driver detail
// ever reaches this string.
const msgInternal = "internal server error"

// requestIDContextKey is a placeholder carrier for the correlation id until
// the request-id middleware task defines its own canonical key and
// installs it on every request's context; that task updates writeError to
// read its own accessor instead of this one. Kept unexported and
// zero-sized so it never collides with another package's context key.
type requestIDContextKey struct{}

// requestIDFromContext returns the id the middleware attached to ctx, or ""
// when none is present (e.g. no middleware installed, as in this task's own
// tests except where they set one explicitly).
func requestIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(requestIDContextKey{}).(string)
	return v
}

// writeJSON marshals v and writes it with the status. Content-Type is set
// BEFORE WriteHeader — setting it afterwards is a silent no-op — and an
// encode failure is logged rather than answered with a second WriteHeader,
// since the status line is already on the wire by the time Encode runs.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set(headerContentType, contentTypeJSON)
	w.WriteHeader(status)
	if encodeErr := json.NewEncoder(w).Encode(v); encodeErr != nil {
		slog.ErrorContext(r.Context(), "encode response body", "error", encodeErr)
	}
}

// writeError writes an errorDTO shape. The request id is attached only for a
// 5xx, so the 400/404 bodies spec §13.5 and §13.6 pin verbatim keep exactly
// one key.
func writeError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if status >= 500 {
		writeJSON(w, r, status, serverErrorDTO{Error: msg, RequestID: requestIDFromContext(r.Context())})
		return
	}
	writeJSON(w, r, status, clientErrorDTO{Error: msg})
}

// statusForStoreError maps a store error to its HTTP status and client-safe
// message. It is the ONLY place in the package that turns an error into a
// status, and its default branch NEVER formats the error into the body: a
// *pgconn.PgError's Error() carries SQL text, column names and constraint
// names.
func statusForStoreError(err error) (status int, msg string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, store.ErrConflict):
		return http.StatusConflict, "conflict"
	default:
		return http.StatusInternalServerError, msgInternal
	}
}
