package api

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// serverTestHandler is a distinguishable handler value. It is a POINTER type so
// srv.Handler can be compared for identity: a func value is not comparable,
// and comparing "not nil" would not catch a constructor that quietly
// substituted http.DefaultServeMux.
type serverTestHandler struct{}

func (*serverTestHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// serveOn starts srv on an OS-assigned loopback port and returns its base URL.
// Port 0, never a fixed port: a fixed port flakes on a busy machine and makes
// two concurrent runs of this package impossible.
func serveOn(t *testing.T, srv *http.Server) string {
	t.Helper()

	var lc net.ListenConfig
	ln, listenErr := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if listenErr != nil {
		t.Fatalf("listen: %v", listenErr)
	}

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after Close")
		}
	})

	return "http://" + ln.Addr().String()
}

// getOK issues one GET and reports whether it completed with a 200. Used to
// prove a server was actually serving BEFORE shutdown and is not afterwards.
func getOK(t *testing.T, url string) bool {
	t.Helper()

	// t.Errorf and a return, never t.Fatalf: getOK is called from a non-test
	// goroutine by wedgedServer, and t.Fatalf off the test goroutine is
	// undefined behavior (it calls runtime.Goexit on the wrong goroutine).
	req, reqErr := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if reqErr != nil {
		t.Errorf("new request: %v", reqErr)
		return false
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, doErr := client.Do(req)
	if doErr != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode == http.StatusOK
}

func TestAPIServerTimeoutTable(t *testing.T) {
	srv := NewAPIServer(":0", &serverTestHandler{})

	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 15*time.Second {
		t.Errorf("ReadTimeout = %v, want 15s", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0 (SSE exception, spec §6.7)", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout = %v, want 120s", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 16*1024 {
		t.Errorf("MaxHeaderBytes = %d, want 16384", srv.MaxHeaderBytes)
	}
}

func TestOpsServerTimeoutTable(t *testing.T) {
	srv := NewOpsServer(":0", &serverTestHandler{})

	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 15*time.Second {
		t.Errorf("ReadTimeout = %v, want 15s", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 15*time.Second {
		t.Errorf("WriteTimeout = %v, want 15s", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout = %v, want 120s", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 16*1024 {
		t.Errorf("MaxHeaderBytes = %d, want 16384", srv.MaxHeaderBytes)
	}
}

// TestTheTwoServersDifferOnlyInWriteTimeout asserts the asymmetry AS an
// asymmetry. A copy-paste that gave the API server the ops server's 15 s
// WriteTimeout would still satisfy TestOpsServerTimeoutTable — and would kill
// every SSE stream at 15 seconds.
func TestTheTwoServersDifferOnlyInWriteTimeout(t *testing.T) {
	api := NewAPIServer(":0", &serverTestHandler{})
	ops := NewOpsServer(":0", &serverTestHandler{})

	if api.ReadHeaderTimeout != ops.ReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout differs: api %v, ops %v", api.ReadHeaderTimeout, ops.ReadHeaderTimeout)
	}
	if api.ReadTimeout != ops.ReadTimeout {
		t.Errorf("ReadTimeout differs: api %v, ops %v", api.ReadTimeout, ops.ReadTimeout)
	}
	if api.IdleTimeout != ops.IdleTimeout {
		t.Errorf("IdleTimeout differs: api %v, ops %v", api.IdleTimeout, ops.IdleTimeout)
	}
	if api.MaxHeaderBytes != ops.MaxHeaderBytes {
		t.Errorf("MaxHeaderBytes differs: api %d, ops %d", api.MaxHeaderBytes, ops.MaxHeaderBytes)
	}
	if api.WriteTimeout == ops.WriteTimeout {
		t.Errorf("WriteTimeout is the same on both (%v); the two servers MUST differ here", api.WriteTimeout)
	}
}

// TestAPIServerWriteTimeoutIsZeroAndThatIsDeliberate stands alone because
// WriteTimeout: 0 is the one field a reviewer is most likely to "fix". The
// compensating control is withWriteDeadline (Task 14), which sets a 30 s
// per-response write deadline on every handler except the SSE one — so the
// zero here is not a missing timeout, it is a timeout moved to where it can
// exempt GET /api/events.
func TestAPIServerWriteTimeoutIsZeroAndThatIsDeliberate(t *testing.T) {
	srv := NewAPIServer(":0", &serverTestHandler{})

	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v, want exactly 0: any finite value here ends every "+
			"SSE stream at that mark. The per-response deadline lives in withWriteDeadline.",
			srv.WriteTimeout)
	}
}

// TestReadHeaderTimeoutIsSetOnBothServers is the ONLY gate on this field. DO
// NOT DELETE IT believing gosec G112 covers it — measured against this
// repository's exact config, it does not:
//
//	ReadHeaderTimeout removed, ReadTimeout kept  -> golangci-lint: 0 issues
//	both removed                                 -> gosec G112, 1 issue
//
// G112 fires only when a server literal carries NEITHER field, so with
// ReadTimeout set the Slowloris defence can be deleted with a completely green
// lint run. The plan's Global Constraints and spec §6.7 both state the
// opposite; that is a documented error, adjudicated in Task 21's review. This
// test is the primary gate and the lint rule is the fallback, not the reverse.
func TestReadHeaderTimeoutIsSetOnBothServers(t *testing.T) {
	if got := NewAPIServer(":0", &serverTestHandler{}).ReadHeaderTimeout; got == 0 {
		t.Error("api server ReadHeaderTimeout is 0: Slowloris defence absent")
	}
	if got := NewOpsServer(":0", &serverTestHandler{}).ReadHeaderTimeout; got == 0 {
		t.Error("ops server ReadHeaderTimeout is 0: Slowloris defence absent")
	}
}

// TestBothServersCarryTheirHandler pins handler identity. A nil Handler makes
// http.Server fall back to http.DefaultServeMux, which is how an
// unintentionally-registered pprof endpoint ends up world-reachable.
func TestBothServersCarryTheirHandler(t *testing.T) {
	h := &serverTestHandler{}

	if got := NewAPIServer(":0", h).Handler; got != http.Handler(h) {
		t.Errorf("api server Handler = %#v, want the handler passed in", got)
	}
	if got := NewOpsServer(":0", h).Handler; got != http.Handler(h) {
		t.Errorf("ops server Handler = %#v, want the handler passed in", got)
	}
}

func TestServerAddrIsTheOneGiven(t *testing.T) {
	if got := NewAPIServer("127.0.0.1:0", &serverTestHandler{}).Addr; got != "127.0.0.1:0" {
		t.Errorf("api server Addr = %q, want %q", got, "127.0.0.1:0")
	}
	if got := NewOpsServer("127.0.0.1:0", &serverTestHandler{}).Addr; got != "127.0.0.1:0" {
		t.Errorf("ops server Addr = %q, want %q", got, "127.0.0.1:0")
	}
}

func TestShutdownStopsBothServers(t *testing.T) {
	apiSrv := NewAPIServer("", &serverTestHandler{})
	opsSrv := NewOpsServer("", &serverTestHandler{})

	apiURL := serveOn(t, apiSrv)
	opsURL := serveOn(t, opsSrv)

	// Liveness first: without this, "cannot connect after shutdown" would hold
	// vacuously against a server that never served at all.
	if !getOK(t, apiURL) {
		t.Fatal("api server did not answer 200 before shutdown")
	}
	if !getOK(t, opsURL) {
		t.Fatal("ops server did not answer 200 before shutdown")
	}

	if err := Shutdown(context.Background(), apiSrv, opsSrv); err != nil {
		t.Fatalf("Shutdown = %v, want nil", err)
	}

	if getOK(t, apiURL) {
		t.Error("api server still answering after Shutdown")
	}
	if getOK(t, opsURL) {
		t.Error("ops server still answering after Shutdown")
	}
}

// errCloseA and errCloseB are the two distinct sentinels the listeners below
// return from Close. They must be DISTINCT: two servers failing with the same
// error cannot distinguish "joined both" from "returned the first".
var (
	errCloseA = errors.New("listener a refused to close")
	errCloseB = errors.New("listener b refused to close")
)

// failingCloseListener is a listener whose Close reports an error after really
// closing. http.Server.Shutdown surfaces a listener-close error, which is the
// only injectable failure path a Shutdown call has.
// accepting closes once Accept has been called, which is how the test knows
// http.Server has REGISTERED this listener. Shutdown only closes registered
// listeners, so calling it before Serve got that far would see no listener,
// return nil, and make this test pass for the wrong reason.
type failingCloseListener struct {
	net.Listener

	err       error
	once      sync.Once
	accepting chan struct{}
}

func (l *failingCloseListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.accepting) })
	return l.Listener.Accept()
}

func (l *failingCloseListener) Close() error {
	_ = l.Listener.Close()
	return l.err
}

func TestShutdownJoinsErrorsFromBothServers(t *testing.T) {
	mk := func(sentinel error) *http.Server {
		t.Helper()

		var lc net.ListenConfig
		ln, listenErr := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
		if listenErr != nil {
			t.Fatalf("listen: %v", listenErr)
		}
		srv := NewOpsServer("", &serverTestHandler{})
		wrapped := &failingCloseListener{Listener: ln, err: sentinel, accepting: make(chan struct{})}
		served := make(chan error, 1)
		go func() { served <- srv.Serve(wrapped) }()
		t.Cleanup(func() {
			select {
			case <-served:
			case <-time.After(5 * time.Second):
				t.Error("Serve did not return")
			}
		})

		select {
		case <-wrapped.accepting:
		case <-time.After(5 * time.Second):
			t.Fatal("Serve never reached Accept, so the listener was never registered")
		}
		return srv
	}

	a := mk(errCloseA)
	b := mk(errCloseB)

	err := Shutdown(context.Background(), a, b)
	if err == nil {
		t.Fatal("Shutdown = nil, want an error from each server")
	}
	if !errors.Is(err, errCloseA) {
		t.Errorf("error does not match the FIRST server's failure: %v", err)
	}
	if !errors.Is(err, errCloseB) {
		t.Errorf("error does not match the SECOND server's failure (a return-on-first-error "+
			"Shutdown would hide it): %v", err)
	}
}

// wedgedServer starts a server whose single handler is parked in a request that
// never completes until the test ends, and shortens shutdownBudget to budget so
// the tests that use it are fast. A request is in flight and the connection is
// never idle, which is the only state in which Shutdown's budget handling is
// observable: with no active request Shutdown returns nil on its first poll and
// every budget assertion holds vacuously.
func wedgedServer(t *testing.T, budget time.Duration) *http.Server {
	t.Helper()

	restore := shutdownBudget
	shutdownBudget = budget
	t.Cleanup(func() { shutdownBudget = restore })

	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	blocking := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
	})

	srv := NewOpsServer("", blocking)
	url := serveOn(t, srv)

	go func() { _ = getOK(t, url) }()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("blocking handler never ran")
	}

	return srv
}

// TestShutdownRespectsTheBudget pins the 5 s bound of §16.1 step 1: a wedged
// handler must not make Shutdown wait for it. The budget is shortened so the
// test is fast, and the assertion is a TIME BOUND with a failure-path guard, so
// a regression fails rather than hangs.
func TestShutdownRespectsTheBudget(t *testing.T) {
	srv := wedgedServer(t, 200*time.Millisecond)

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- Shutdown(context.Background(), srv) }()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Shutdown = %v, want an error matching context.DeadlineExceeded", err)
		}
		// Three budgets of slack for a loaded CI box, still far below the
		// 600 ms the blocked handler would cost if Shutdown waited for it.
		if elapsed > 3*200*time.Millisecond {
			t.Errorf("Shutdown took %v, want roughly the 200ms budget", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown blocked on the wedged handler instead of honoring its budget")
	}
}

// TestShutdownStillDrainsForTheWholeBudgetWhenTheParentContextIsAlreadyCanceled
// covers the §16.1 step-0 ordering: SIGTERM cancels the root context BEFORE
// step 1 runs, so the context handed to Shutdown is normally already canceled.
// Deriving the budget from it would make context.WithTimeout return an
// already-expired context, Shutdown would return context.Canceled instantly,
// and every in-flight response would be cut mid-write instead of drained.
//
// The discriminator is the ERROR KIND, not the duration: with a request in
// flight a budget-honoring Shutdown can only end in context.DeadlineExceeded,
// while one that inherits the cancellation ends in context.Canceled.
func TestShutdownStillDrainsForTheWholeBudgetWhenTheParentContextIsAlreadyCanceled(t *testing.T) {
	srv := wedgedServer(t, 200*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() { done <- Shutdown(ctx, srv) }()

	select {
	case err := <-done:
		if errors.Is(err, context.Canceled) {
			t.Errorf("Shutdown = %v: it inherited the parent's cancellation and cut the "+
				"in-flight request instead of draining it for its own budget", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Shutdown = %v, want an error matching context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown never returned")
	}
}

func TestShutdownOfZeroServersIsNil(t *testing.T) {
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() with no servers = %v, want nil", err)
	}
}

// TestBudgetsFitTheTerminationGracePeriod is spec §16.1's arithmetic. Each
// term is first checked against a LITERAL — asserting the sum against the
// package's own constants alone would pass for any set of values that happens
// to add up — and then the sum is checked against the grace period.
func TestBudgetsFitTheTerminationGracePeriod(t *testing.T) {
	if shutdownBudget != 5*time.Second {
		t.Errorf("shutdownBudget = %v, want 5s (§16.1 step 1)", shutdownBudget)
	}
	if errgroupWaitBudget != 70*time.Second {
		t.Errorf("errgroupWaitBudget = %v, want 70s (§16.1 step 2)", errgroupWaitBudget)
	}
	if poolCloseBudget != 5*time.Second {
		t.Errorf("poolCloseBudget = %v, want 5s (§16.1 step 3)", poolCloseBudget)
	}
	if terminationGracePeriod != 90*time.Second {
		t.Errorf("terminationGracePeriod = %v, want 90s", terminationGracePeriod)
	}

	if sum := shutdownBudget + errgroupWaitBudget + poolCloseBudget; sum >= 90*time.Second {
		t.Errorf("steps 1-3 sum to %v, which is not strictly less than the 90s grace period", sum)
	}
}

// TestAttachBaseContextCancelsAnInFlightRequest is the unit-level half of the
// shutdown story: a canceled base context must reach r.Context().
func TestAttachBaseContextCancelsAnInFlightRequest(t *testing.T) {
	entered := make(chan struct{})
	observed := make(chan struct{})

	h := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(observed)
	})

	srv := NewOpsServer("", h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	AttachBaseContext(ctx, srv)

	url := serveOn(t, srv)

	// NOT getOK: its client carries a 3 s timeout, and a client-side timeout
	// closes the connection, which cancels the request context on its own. That
	// would make this test pass without any base context at all. The base
	// context must be the ONLY cancellation source inside the window below.
	req, reqErr := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if reqErr != nil {
		t.Fatalf("new request: %v", reqErr)
	}
	go func() {
		resp, doErr := (&http.Client{}).Do(req)
		if doErr == nil {
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never ran")
	}

	cancel()

	select {
	case <-observed:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling the base context did not cancel the request context")
	}
}

// TestShutdownWithALiveSSEStreamCompletesWellUnderTheBudget is the load-bearing
// test of this task. A real /api/events stream is opened against a real
// listener; the SSE handler is then parked in a select whose only exits are its
// request context and a write failure. Without AttachBaseContext, Shutdown
// cannot return until its own 5 s deadline expires, so this asserts a TIME
// BOUND — and the failure mode is a bounded wait, not a hang.
func TestShutdownWithALiveSSEStreamCompletesWellUnderTheBudget(t *testing.T) {
	// Goroutine-leak baseline, sampled before anything is started. See the
	// settle loop at the end of this test for why the bound is not exact.
	baselineGoroutines := runtime.NumGoroutine()

	d := testDeps(t)
	srv := NewAPIServer("", NewRouter(d))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	AttachBaseContext(ctx, srv)

	url := serveOn(t, srv)

	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()

	req, reqErr := http.NewRequestWithContext(streamCtx, http.MethodGet, url+pathEvents, nil)
	if reqErr != nil {
		t.Fatalf("new request: %v", reqErr)
	}

	client := &http.Client{}
	resp, doErr := client.Do(req)
	if doErr != nil {
		t.Fatalf("open stream: %v", doErr)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}

	// Liveness: read the first frame's event line, so the assertion below
	// cannot pass against a stream the handler never actually entered.
	firstLine := make(chan string, 1)
	go func() {
		line, readErr := bufio.NewReader(resp.Body).ReadString('\n')
		if readErr != nil {
			firstLine <- ""
			return
		}
		firstLine <- line
	}()

	select {
	case line := <-firstLine:
		if !strings.Contains(line, sseEventName) {
			t.Fatalf("first stream line = %q, want it to name the %s event", line, sseEventName)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no SSE frame arrived; the stream was never live")
	}

	// Step 0 then step 1, in the production order.
	cancel()

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- Shutdown(context.Background(), srv) }()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if err != nil {
			t.Errorf("Shutdown with a live stream = %v, want nil", err)
		}
		// The real budget is 5 s. Anything at or near it means the SSE handler
		// did NOT observe the cancellation and Shutdown waited out its
		// deadline; 2 s is generous slack while still failing that case.
		if elapsed > 2*time.Second {
			t.Errorf("Shutdown took %v with one live SSE stream: the stream did not observe "+
				"the base context's cancellation, so every deploy burns the whole grace period",
				elapsed)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Shutdown never returned with a live SSE stream attached")
	}

	// ── Goroutine-leak gate, scoped to the CLEAN-shutdown case.
	//
	// It lives here rather than in its own test because this is the only test
	// in the file whose shutdown is clean: TestShutdownRespectsTheBudget and the
	// canceled-parent test both leave a WEDGED handler goroutine parked on a
	// channel by design (accepted behavior — it is bounded only by process
	// exit), so a leak assertion around either of those would contradict the
	// contract they pin.
	//
	// Everything this test started must be gone once Shutdown has returned: the
	// SSE handler's goroutine (returned on ctx.Done), the Serve goroutine
	// (returned with ErrServerClosed) and the transport's read loop.
	_ = resp.Body.Close()
	client.CloseIdleConnections()

	// A BOUNDED assertion, not exact equality, and sampled in a settle loop
	// rather than after a sleep. Goroutine teardown is asynchronous — the
	// transport's read/write loops and Serve's per-connection goroutine all exit
	// on their own schedule — and NumGoroutine also counts goroutines belonging
	// to OTHER work in the process (the testing framework, and any parallel
	// package under `go test ./...`). An exact equality would therefore be a
	// flake, especially under -race -count=5. The tolerance is small enough that
	// a real leak — one goroutine per live stream, which is the failure mode
	// worth catching — still trips it, because that failure grows with every
	// connection rather than staying inside a fixed slack.
	const goroutineSlack = 3

	settled := baselineGoroutines
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		settled = runtime.NumGoroutine()
		if settled <= baselineGoroutines+goroutineSlack {
			return
		}
		runtime.Gosched()
	}

	t.Errorf("goroutines: %d before, %d after a clean shutdown (slack %d): the SSE handler, "+
		"the Serve goroutine or a transport read loop leaked",
		baselineGoroutines, settled, goroutineSlack)
}
