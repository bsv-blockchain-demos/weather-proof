package api

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"

	"github.com/google/uuid"
)

// requestIDHeader is the response header carrying the correlation id. It is
// also what an operator greps for when a user reports a 500.
const requestIDHeader = "X-Request-Id"

// The three headers of spec §6.0 control 19. The TypeScript has NONE of them;
// this is a beyond-parity control and it is in the acceptance gate
// specifically so it cannot quietly not ship.
const (
	headerContentTypeOptions = "X-Content-Type-Options"
	headerFrameOptions       = "X-Frame-Options"
	headerReferrerPolicy     = "Referrer-Policy"

	valueNoSniff    = "nosniff"
	valueDeny       = "DENY"
	valueNoReferrer = "no-referrer"
)

// withSecurityHeaders sets all three on EVERY response, including error
// responses and including the SSE stream. It sets them before calling next, so
// a handler that writes a status immediately still carries them — headers set
// after WriteHeader are silently dropped.
//
// No CSP here: spec §6.6 puts the CSP on the frontend nginx, and an API that
// serves no HTML gains nothing from one. No HSTS either — TLS terminates at
// Cloudflare and an HSTS header from the origin is at best redundant and at
// worst wrong for an in-cluster caller. Both omissions are deliberate.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerContentTypeOptions, valueNoSniff)
		w.Header().Set(headerFrameOptions, valueDeny)
		w.Header().Set(headerReferrerPolicy, valueNoReferrer)
		next.ServeHTTP(w, r)
	})
}

// requestIDKey is the context key type. A named unexported struct type
// rather than a string, so no other package can collide with it.
type requestIDKey struct{}

// withRequestID assigns a uuidv7 to every request, puts it in the context and
// echoes it in the response header. It NEVER trusts an inbound X-Request-Id:
// an attacker-chosen id lets a caller poison log correlation and, if it were
// echoed unvalidated, inject a header value.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.NewV7()
		if err != nil {
			// uuid.NewV7 only fails if the entropy source errors; fall back to
			// a random v4 rather than serving requests with no id at all.
			id = uuid.New()
		}
		idStr := id.String()
		w.Header().Set(requestIDHeader, idStr)
		ctx := context.WithValue(r.Context(), requestIDKey{}, idStr)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requestIDFrom returns the id, or "" when the middleware is not installed.
// Returning "" rather than panicking keeps a unit-tested handler usable
// without the whole chain.
func requestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey{}).(string)
	return v
}

// statusRecorder wraps a ResponseWriter to observe the status and whether a
// header was written. It implements http.Flusher and http.Hijacker
// passthrough — WITHOUT the Flusher passthrough the SSE handler silently
// stops flushing the moment this middleware is installed, which is a
// live-dot-green /stats-frozen failure with no error anywhere.
type statusRecorder struct {
	http.ResponseWriter

	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if s.wroteHeader {
		return
	}
	s.wroteHeader = true
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.WriteHeader(http.StatusOK)
	}
	n, writeErr := s.ResponseWriter.Write(b)
	return n, writeErr
}

// Flush passes through to the underlying ResponseWriter's http.Flusher, if
// it implements one. Without this, wrapping an SSE handler in statusRecorder
// silently breaks streaming: the wrapped writer no longer satisfies
// http.Flusher and http.NewResponseController's Flush call fails.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack passes through to the underlying ResponseWriter's http.Hijacker, if
// it implements one.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return h.Hijack()
}

// withRecover converts a panic into an opaque 500. It logs the value AND the
// stack with slog at error level together with the request id, and writes
// {"error":"internal server error","request_id":"…"} — never the panic
// value, which for a nil-map write or a slice bound carries no secret but
// for a wrapped driver error carries a DSN.
//
// It also handles the case where the handler already wrote a status before
// panicking: a captured wroteHeader flag means the recovery logs and returns
// rather than attempting a second WriteHeader, which would emit
// "superfluous response.WriteHeader" and nothing useful.
func withRecover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w}
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if panicErr, ok := v.(error); ok && errors.Is(panicErr, http.ErrAbortHandler) {
					panic(v)
				}
				stack := debug.Stack()
				log.ErrorContext(r.Context(), "panic recovered",
					"request_id", requestIDFrom(r.Context()),
					"panic", v,
					"stack", string(stack),
				)
				if rec.wroteHeader {
					return
				}
				writeError(w, r, http.StatusInternalServerError, msgInternal)
			}()
			next.ServeHTTP(rec, r)
		})
	}
}
