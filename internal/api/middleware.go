package api

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"

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

// Unwrap exposes the underlying ResponseWriter to http.ResponseController.
// Without it, http.NewResponseController(rec).SetWriteDeadline fails with
// http.ErrNotSupported even when the connection underneath fully supports
// deadlines: ResponseController first type-asserts the writer it was given
// directly, and only recurses into whatever an Unwrap() http.ResponseWriter
// method exposes. statusRecorder itself never implements SetWriteDeadline,
// so without Unwrap the assertion fails silently and every unit test that
// runs against a bare httptest.ResponseRecorder still passes — the failure
// only shows up against a real connection, in production.
func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
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

// writeDeadline is the per-response budget for every NON-SSE handler. It is a
// package var, not a const, because the timing tests in middleware_test.go
// shorten it to keep the suite fast and restore it with t.Cleanup — a real
// 30-second sleep in a unit test is how a suite becomes something nobody
// runs.
var writeDeadline = 30 * time.Second

// sseExemptKey is the context key for the pointer through which markSSEExempt
// signals withWriteDeadline. The stored value is a *bool, deliberately NOT a
// plain bool and NOT a package-level bool.
//
// It cannot be a plain bool set via a fresh context.WithValue call from
// markSSEExempt, because markSSEExempt runs deep inside next.ServeHTTP —
// after net/http.ServeMux has resolved which pattern matched — while
// withWriteDeadline's own deadline decision must take effect no later than
// the handler's first write, which can happen before or interleaved with
// that resolution. context.Context is immutable: a value added downstream
// produces a new context node that no ancestor closure can observe, so
// withWriteDeadline cannot simply "check again" after next.ServeHTTP starts
// running. Threading a shared pointer through the context BEFORE calling
// next, and letting markSSEExempt flip the cell it points to, lets a
// downstream middleware communicate back to an already-installed lazy
// deadline check without needing a second, mutable, PACKAGE-LEVEL flag — the
// one shape this must never take, because a package-level bool leaks across
// every request after the first SSE connection (see
// TestSSEExemptFlagDoesNotLeakToTheNextRequest).
type sseExemptKey struct{}

// markSSEExempt marks a handler as exempt from withWriteDeadline. It is a
// request context value rather than a separate router because the exemption
// must be visible to withWriteDeadline, installed ABOVE the mux, while only
// the mux (and the handler it dispatches to) knows which pattern matched.
// Task 18's SSE route registration wraps its handler in markSSEExempt; no
// other seam is supported.
func markSSEExempt(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if flag, ok := r.Context().Value(sseExemptKey{}).(*bool); ok {
			*flag = true
		}
		next.ServeHTTP(w, r)
	})
}

// isSSEExempt reports whether the request context carries the SSE exemption
// set by markSSEExempt. It must be called AFTER the point in the handler
// chain where markSSEExempt would have run — which is exactly what
// deadlineResponseWriter does, by deferring the check to the first write
// rather than evaluating it eagerly before calling next.
func isSSEExempt(ctx context.Context) bool {
	flag, ok := ctx.Value(sseExemptKey{}).(*bool)
	return ok && *flag
}

// deadlineResponseWriter defers the write-deadline decision to the first
// Write, WriteHeader or Flush call, rather than deciding before calling
// next.ServeHTTP. It must defer: at the point withWriteDeadline's own
// handler runs, the mux has not yet dispatched to the matched route, so
// markSSEExempt (if this request even reaches it) has not yet had a chance
// to flip the shared exemption flag. By the time anything is actually
// written, dispatch has completed and the flag holds its final value.
type deadlineResponseWriter struct {
	http.ResponseWriter

	rc      *http.ResponseController
	ctx     context.Context
	log     *slog.Logger
	applied bool
}

func (d *deadlineResponseWriter) applyDeadline() {
	if d.applied {
		return
	}
	d.applied = true
	if isSSEExempt(d.ctx) {
		return
	}
	if deadlineErr := d.rc.SetWriteDeadline(time.Now().Add(writeDeadline)); deadlineErr != nil {
		d.log.DebugContext(d.ctx, "write deadline not supported",
			"request_id", requestIDFrom(d.ctx),
			"error", deadlineErr,
		)
	}
}

func (d *deadlineResponseWriter) WriteHeader(status int) {
	d.applyDeadline()
	d.ResponseWriter.WriteHeader(status)
}

func (d *deadlineResponseWriter) Write(b []byte) (int, error) {
	d.applyDeadline()
	return d.ResponseWriter.Write(b)
}

// Flush passes through to the underlying ResponseWriter's http.Flusher, if
// it implements one, applying the deadline first — the SSE handler's own
// heartbeat flush must be able to reach this path exempt.
func (d *deadlineResponseWriter) Flush() {
	d.applyDeadline()
	if f, ok := d.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// FlushError passes through to the underlying ResponseWriter's
// http.ResponseController-recognised FlushError, if it implements one. This
// is the method that actually surfaces a deadline-exceeded write error: if
// deadlineResponseWriter exposed ONLY the older no-error Flush() method
// above, http.ResponseController.Flush would stop at THIS type (it
// satisfies http.Flusher) and never unwrap down to the real connection's
// FlushError, silently discarding the very error this middleware exists to
// let a caller observe.
func (d *deadlineResponseWriter) FlushError() error {
	d.applyDeadline()
	return http.NewResponseController(d.ResponseWriter).Flush()
}

// Hijack passes through to the underlying ResponseWriter's http.Hijacker, if
// it implements one.
func (d *deadlineResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := d.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return h.Hijack()
}

// Unwrap exposes the underlying ResponseWriter to any http.ResponseController
// installed further out in the chain — there is none today, but the same
// silent-failure trap that statusRecorder guards against applies here too.
func (d *deadlineResponseWriter) Unwrap() http.ResponseWriter {
	return d.ResponseWriter
}

// withWriteDeadline sets a per-response write deadline via
// http.NewResponseController. It exists because the API server's WriteTimeout
// is 0 — required for SSE — so without this middleware a slow-reading client
// can pin a goroutine and a connection on any ordinary endpoint forever.
//
// The actual SetWriteDeadline call is deferred to the first write (see
// deadlineResponseWriter): checking isSSEExempt eagerly, before calling
// next.ServeHTTP, would run before net/http.ServeMux has dispatched to the
// matched route and therefore before markSSEExempt has had any chance to run.
//
// A SetWriteDeadline failure is logged at DEBUG and ignored: the only
// documented cause is a ResponseWriter that does not support it, which under
// httptest.ResponseRecorder is the normal unit-test case and is not a runtime
// error worth a 500.
func withWriteDeadline(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			flag := new(bool)
			ctx := context.WithValue(r.Context(), sseExemptKey{}, flag)
			dw := &deadlineResponseWriter{
				ResponseWriter: w,
				rc:             http.NewResponseController(w),
				ctx:            ctx,
				log:            log,
			}
			next.ServeHTTP(dw, r.WithContext(ctx))
		})
	}
}
