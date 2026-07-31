package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// The timeout table of spec §6.7. The API server's WriteTimeout is 0 —
// required for the never-ending SSE response — which is exactly why every
// non-SSE handler carries the per-response write deadline of Task 14
// (withWriteDeadline). The ops server has no streaming endpoint and therefore
// keeps a real WriteTimeout.
//
// Go's http.Server has no useful defaults: ReadHeaderTimeout in particular is
// the Slowloris defence the zero value lacks.
//
// gosec G112 does NOT reliably catch its absence, contrary to the plan's Global
// Constraints and spec §6.7. Measured against this repository's exact config:
// with ReadTimeout still present, deleting ReadHeaderTimeout from either
// literal below yields a completely GREEN lint run; G112 fires only when a
// literal carries NEITHER field. TestReadHeaderTimeoutIsSetOnBothServers is
// therefore the only gate on this field — do not delete it thinking lint has
// it covered.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 15 * time.Second
	opsWriteTimeout   = 15 * time.Second
	idleTimeout       = 120 * time.Second
	maxHeaderBytes    = 16 << 10
)

// shutdownBudget is step 1 of spec §16.1's deadline table: both servers stop
// accepting and drain in-flight reads within 5 s. SSE handlers return on
// ctx.Done rather than being waited for — see AttachBaseContext.
//
// It is a package var rather than a const only so the tests can shorten it
// and restore it with t.Cleanup; nothing in production writes it.
var shutdownBudget = 5 * time.Second

// The other two terms of spec §16.1's arithmetic. B2 owns step 1 only —
// PLAN C owns the errgroup wait and the pool close — but they are declared
// here so TestBudgetsFitTheTerminationGracePeriod can assert the sum against
// terminationGracePeriod instead of leaving it in a comment nobody re-checks.
const (
	// errgroupWaitBudget is step 2: waiting for poller, processor, keeper
	// supervisor, reconciler and sampler. It must exceed the longest in-flight
	// wallet RPC (CreateAction, 60 s). Owner: Plan C's cmd/weather/main.go.
	errgroupWaitBudget = 70 * time.Second

	// poolCloseBudget is step 3: pgxpool.Close after every user is gone.
	// Owner: Plan C's cmd/weather/main.go.
	poolCloseBudget = 5 * time.Second

	// terminationGracePeriod is the Deployment's terminationGracePeriodSeconds
	// (§15, §16.1). Steps 1-3 must sum to strictly less than this, or SIGKILL
	// lands mid-CreateAction.
	terminationGracePeriod = 90 * time.Second
)

// NewAPIServer builds the public listener (API_PORT). WriteTimeout is 0 and
// that is deliberate: GET /api/events never finishes writing, and any finite
// WriteTimeout here kills every SSE stream at that mark with no error anywhere
// a client can see. The compensating control is withWriteDeadline, which sets
// a per-response deadline on every handler EXCEPT the SSE one.
func NewAPIServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      0,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

// NewOpsServer builds the cluster-internal listener (OPS_PORT). It carries a
// real 15 s WriteTimeout because it has no streaming endpoint; /api/ops is a
// few hundred bytes. The port separation is also what makes /api/ops exempt
// from the rate limiter by construction rather than by registration order.
func NewOpsServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      opsWriteTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

// AttachBaseContext makes ctx the parent of every request context the server
// subsequently serves, so cancelling ctx unblocks handlers that are parked on
// r.Context().Done().
//
// This is NOT optional wiring, and it is the whole reason Shutdown can be
// bounded at 5 s. http.Server.Shutdown waits for active requests to return;
// the SSE handler's only termination signals are its request context and a
// write failure, and the hub deliberately exposes no lever to end a stream
// (see Hub's shutdown contract — it never closes a client channel). So with a
// live stream attached and no base context, Shutdown cannot complete until its
// own deadline expires: the process burns the entire step-1 budget on every
// deploy, and does it silently.
//
// BaseContext is the seam because a request context derives from the
// connection context, which derives from BaseContext — one cancellation
// therefore reaches every in-flight request on every connection. It must be
// called BEFORE Serve/ListenAndServe, since BaseContext is only consulted when
// a listener starts accepting.
//
// The ctx-first parameter order is the convention even though the context is
// stored rather than used for a call.
func AttachBaseContext(ctx context.Context, srv *http.Server) {
	srv.BaseContext = func(_ net.Listener) context.Context { return ctx }
}

// Shutdown stops every server under ONE shutdownBudget-bounded context and
// returns every error joined, so a failure on one server cannot hide a failure
// on the other.
//
// ctx's own cancellation is deliberately DROPPED with context.WithoutCancel:
// step 0 of §16.1 is "SIGTERM → cancel root ctx", so the context a caller has
// in hand at shutdown time is almost always ALREADY canceled. Deriving the
// budget from it would make context.WithTimeout return an already-expired
// context, every Shutdown call would return context.Canceled immediately, and
// every in-flight response would be cut mid-write instead of drained — the
// exact opposite of a graceful shutdown, with nothing in the logs to say so.
// What ctx is still used for is its VALUES.
func Shutdown(ctx context.Context, servers ...*http.Server) error {
	if len(servers) == 0 {
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownBudget)
	defer cancel()

	// One slot per server, written only by that server's goroutine, so the
	// order of the joined error matches the order of the arguments and no
	// server's failure can be lost to a race on a shared slice.
	errs := make([]error, len(servers))

	var wg sync.WaitGroup
	for i, srv := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if srvErr := srv.Shutdown(shutdownCtx); srvErr != nil {
				errs[i] = fmt.Errorf("api: shutdown %q: %w", srv.Addr, srvErr)
			}
		}()
	}
	wg.Wait()

	return errors.Join(errs...)
}
