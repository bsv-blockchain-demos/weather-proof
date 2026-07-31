package api

import (
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
//
// It is writeErrorCause with no cause, for the call sites that have no
// underlying error to report — a 4xx, or the 501 placeholder.
func writeError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	writeErrorCause(w, r, status, msg, nil)
}

// writeErrorCause is the SINGLE 5xx exit from this package, and it is the one
// place the correlation id is made useful: every status >= 500 logs here,
// unconditionally, before the body is written. That is deliberate centralization
// — a per-handler log line is a line a new 500 call site can forget, and a
// request id that appears only in the response body while the logs hold nothing
// under it is decoration (plan Global Constraints, spec §12.5, docs/api.md's
// "Error bodies").
//
// cause NEVER reaches the body: msg is the client-safe string and cause goes to
// the log only. That separation is the whole reason the 500 body is opaque —
// a *pgconn.PgError's Error() carries the SQLSTATE, Detail, Hint,
// ConstraintName, ColumnName and TableName.
func writeErrorCause(w http.ResponseWriter, r *http.Request, status int, msg string, cause error) {
	if status >= 500 {
		logServerError(r, status, msg, cause)
		writeJSON(w, r, status, serverErrorDTO{Error: msg, RequestID: requestIDFrom(r.Context())})
		return
	}
	writeJSON(w, r, status, clientErrorDTO{Error: msg})
}

// logServerError emits the error-level record that a grep for the response's
// request_id has to find. The id logged is requestIDFrom of the SAME context
// the body's id is taken from, so the two are the same string by construction
// rather than by convention — TestFiveHundredLogsTheSameRequestIDAsTheBody
// compares them.
//
// cause is omitted rather than logged as <nil> when there is none (the 501
// placeholder), so an operator never reads "error: <nil>" as a missing cause.
func logServerError(r *http.Request, status int, msg string, cause error) {
	ctx := r.Context()
	if cause == nil {
		slog.ErrorContext(ctx, "request failed",
			"request_id", requestIDFrom(ctx),
			"status", status,
			"response_message", msg,
		)
		return
	}
	slog.ErrorContext(ctx, "request failed",
		"request_id", requestIDFrom(ctx),
		"status", status,
		"response_message", msg,
		"error", cause,
	)
}

// writeStoreError is how EVERY handler answers a store failure: it maps the
// error to a status and a client-safe message and routes it through
// writeErrorCause, so the underlying error is logged on the 500 path and
// dropped on the 404/409 paths where it carries nothing an operator needs.
//
// Handlers call this rather than statusForStoreError + writeError, because that
// two-step shape is what let eight call sites each independently forget the log
// line.
func writeStoreError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := statusForStoreError(err)
	writeErrorCause(w, r, status, msg, err)
}

// writeParamError is writeStoreError's counterpart on the INPUT side: it is how
// every handler answers a request-parameter parse failure. A badRequestError
// carries a message written to be shown to a client and becomes a 400; anything
// else is a defect in this package's own parsing and becomes the opaque 500 with
// the real cause logged and never bodied.
//
// One function rather than the five identical branches it replaces, for exactly
// the reason writeStoreError exists: five copies of an error mapping are five
// places a newly added parse error can be classified differently, and the copy
// that gets it wrong is the one that puts an internal message in a response.
func writeParamError(w http.ResponseWriter, r *http.Request, err error) {
	var badReq badRequestError
	if errors.As(err, &badReq) {
		writeError(w, r, http.StatusBadRequest, badReq.Error())
		return
	}
	writeErrorCause(w, r, http.StatusInternalServerError, msgInternal, err)
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
