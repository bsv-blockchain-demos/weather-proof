package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsv-blockchain-demos/weather-proof/internal/config"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store/fake"
	"github.com/bsv-blockchain-demos/weather-proof/internal/weather"
)

// broadcastGuard is the failure-path timeout for "this call must not block".
// It is generous on purpose: it exists to turn a hang into a named failure on
// a loaded CI runner, never to synchronize anything. No test in this file uses
// time.Sleep as synchronization.
const broadcastGuard = 5 * time.Second

// fixtureStats is a statsDTO whose four fields are pairwise distinct, so a
// projection or copy that swaps two of them cannot pass a deep-equal
// assertion. lastRecordWrite is non-nil for the same reason: a dropped
// pointer field would otherwise be invisible.
func fixtureStats() statsDTO {
	ts := time.Date(2026, 7, 30, 12, 34, 56, 789_000_000, time.UTC)
	return statsDTO{
		ActiveStations:  7,
		TotalTx:         11,
		LastRecordWrite: isoPtr(&ts),
		TotalDataPoints: 23,
	}
}

// mustRegister registers one client and fails the test if admission was
// refused. It returns the channel and the unregister func; the unregister is
// NOT auto-deferred, because several tests here care about the exact moment
// capacity is released.
func mustRegister(t *testing.T, h *Hub, ipKey string) (<-chan statsDTO, func()) {
	t.Helper()
	ch, cancel, err := h.Register(ipKey)
	if err != nil {
		t.Fatalf("Register(%q): unexpected error %v", ipKey, err)
	}
	return ch, cancel
}

// recvNow reads one payload that must ALREADY be buffered. It never waits, so
// it cannot pass by accident on a slow machine and it cannot hide a fan-out
// that delivered to nobody.
func recvNow(t *testing.T, ch <-chan statsDTO, who string) statsDTO {
	t.Helper()
	select {
	case got := <-ch:
		return got
	default:
		t.Fatalf("client %s received nothing", who)
		return statsDTO{}
	}
}

func TestBroadcastReachesEveryRegisteredClient(t *testing.T) {
	h := NewHub(10, 10, discardLogger())

	chA, cancelA := mustRegister(t, h, "a")
	defer cancelA()
	chB, cancelB := mustRegister(t, h, "b")
	defer cancelB()
	chC, cancelC := mustRegister(t, h, "c")
	defer cancelC()

	if n := h.Broadcast(fixtureStats()); n != 3 {
		t.Fatalf("Broadcast delivered to %d clients, want 3", n)
	}

	// The count alone would be satisfied by an implementation that sent three
	// times to the first client, so every channel is drained individually.
	for who, ch := range map[string]<-chan statsDTO{"a": chA, "b": chB, "c": chC} {
		recvNow(t, ch, who)
	}
}

func TestBroadcastDeliversTheSamePayloadToEveryClient(t *testing.T) {
	h := NewHub(10, 10, discardLogger())

	chans := make([]<-chan statsDTO, 0, 3)
	for _, key := range []string{"a", "b", "c"} {
		ch, cancel := mustRegister(t, h, key)
		defer cancel()
		chans = append(chans, ch)
	}

	want := fixtureStats()
	if n := h.Broadcast(want); n != 3 {
		t.Fatalf("Broadcast delivered to %d clients, want 3", n)
	}

	for i, ch := range chans {
		got := recvNow(t, ch, fmt.Sprint(i))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("client %d received %+v, want %+v", i, got, want)
		}
	}
}

func TestBroadcastReturnsZeroWithNoClients(t *testing.T) {
	h := NewHub(10, 10, discardLogger())
	if n := h.Broadcast(fixtureStats()); n != 0 {
		t.Fatalf("Broadcast on an empty hub returned %d, want 0", n)
	}
}

func TestBroadcastDoesNotBlockOnAFullClientBuffer(t *testing.T) {
	h := NewHub(10, 10, discardLogger())
	ch, cancel := mustRegister(t, h, "a")
	defer cancel()

	// The client never reads. A publisher that blocks on a full buffer wedges
	// the whole process behind one hung browser tab.
	const rounds = 5
	counts := make(chan int, 1)
	go func() {
		total := 0
		for i := 0; i < rounds; i++ {
			total += h.Broadcast(fixtureStats())
		}
		counts <- total
	}()

	select {
	case delivered := <-counts:
		// The first send fills the depth-1 buffer; the other four are dropped.
		if delivered != 1 {
			t.Fatalf("%d broadcasts delivered %d payloads, want 1", rounds, delivered)
		}
	case <-time.After(broadcastGuard):
		t.Fatalf("Broadcast blocked on a client that never reads")
	}

	if got := len(ch); got != 1 {
		t.Fatalf("client buffer holds %d payloads, want 1", got)
	}
}

func TestUnregisterRemovesTheClient(t *testing.T) {
	h := NewHub(10, 10, discardLogger())
	_, cancel := mustRegister(t, h, "a")
	cancel()

	if n := h.Broadcast(fixtureStats()); n != 0 {
		t.Fatalf("Broadcast after unregister delivered to %d clients, want 0", n)
	}
	if got := h.Clients(); got != 0 {
		t.Fatalf("Clients() = %d after unregister, want 0", got)
	}
}

func TestUnregisterIsIdempotent(t *testing.T) {
	h := NewHub(1000, 12, discardLogger())
	_, cancel := mustRegister(t, h, "a")

	cancel()
	cancel()

	if got := h.Clients(); got != 0 {
		t.Fatalf("Clients() = %d after a double unregister, want 0", got)
	}

	_, cancelAgain, err := h.Register("a")
	if err != nil {
		t.Fatalf("Register after a double unregister: unexpected error %v", err)
	}
	cancelAgain()
}

// TestADoubleUnregisterDoesNotReleaseAnotherStreamsSlot is the assertion that
// actually needs the membership guard inside remove. A lone client cannot
// expose a double decrement — the delete-at-zero rule absorbs it — so the
// fixture keeps a SECOND live stream under the same key. Cancelling the first
// client twice must not release the second client's slot; if it does, that key
// silently gets one extra concurrent stream per double call, i.e. unlimited
// streams for a handler that both defers its unregister and calls it on an
// error path.
func TestADoubleUnregisterDoesNotReleaseAnotherStreamsSlot(t *testing.T) {
	const perIP = 2
	h := NewHub(1000, perIP, discardLogger())

	_, cancelFirst := mustRegister(t, h, "a")
	_, cancelSecond := mustRegister(t, h, "a")
	defer cancelSecond()

	cancelFirst()
	cancelFirst()

	// One stream is still live under "a", so exactly one more fits.
	_, cancelThird := mustRegister(t, h, "a")
	defer cancelThird()

	if _, _, err := h.Register("a"); !errors.Is(err, ErrPerIPFull) {
		t.Fatalf("Register beyond the per-IP cap after a double unregister: got %v, want ErrPerIPFull", err)
	}
	if got := h.Clients(); got != perIP {
		t.Fatalf("Clients() = %d, want %d", got, perIP)
	}
}

func TestRegisterRefusesBeyondThePerIPCap(t *testing.T) {
	const cap12 = 12
	h := NewHub(1000, cap12, discardLogger())

	for i := 0; i < cap12; i++ {
		_, cancel := mustRegister(t, h, "a")
		defer cancel()
	}
	if got := h.Clients(); got != cap12 {
		t.Fatalf("Clients() = %d at the per-IP cap, want %d", got, cap12)
	}

	_, _, err := h.Register("a")
	if !errors.Is(err, ErrPerIPFull) {
		t.Fatalf("Register at the per-IP cap: got %v, want ErrPerIPFull", err)
	}
	if got := h.Clients(); got != cap12 {
		t.Fatalf("a refused Register changed Clients() to %d, want %d", got, cap12)
	}
}

func TestPerIPCapIsPerKeyNotGlobal(t *testing.T) {
	h := NewHub(1000, 2, discardLogger())

	for _, key := range []string{"a", "a", "b", "b"} {
		_, cancel := mustRegister(t, h, key)
		defer cancel()
	}
	if got := h.Clients(); got != 4 {
		t.Fatalf("Clients() = %d, want 4: the per-IP cap must be per key", got)
	}
}

func TestRegisterRefusesBeyondTheGlobalCap(t *testing.T) {
	h := NewHub(3, 12, discardLogger())

	for _, key := range []string{"a", "b", "c"} {
		_, cancel := mustRegister(t, h, key)
		defer cancel()
	}

	_, _, err := h.Register("d")
	if !errors.Is(err, ErrHubFull) {
		t.Fatalf("Register at the global cap: got %v, want ErrHubFull", err)
	}
	if got := h.Clients(); got != 3 {
		t.Fatalf("a refused Register changed Clients() to %d, want 3", got)
	}
}

// TestGlobalCapTakesPrecedenceOverThePerIPCap pins the CHECK ORDER, so the
// fixture saturates BOTH caps at once: two streams under one key with
// globalMax 2 and perIP 2. Only then is the order observable — with the caps
// set so that a refused newcomer is inside its own per-IP share (the brief's
// globalMax 2 / perIP 12 / three distinct keys), a hub that checks the per-IP
// cap first falls through to the global check and still answers ErrHubFull, so
// that shape cannot fail against the mutation it names. The handler picks 503
// vs 429 off this distinction, and at global capacity the refusal is our
// ceiling, not the client's fault.
func TestGlobalCapTakesPrecedenceOverThePerIPCap(t *testing.T) {
	h := NewHub(2, 2, discardLogger())

	_, cancelFirst := mustRegister(t, h, "a")
	defer cancelFirst()
	_, cancelSecond := mustRegister(t, h, "a")
	defer cancelSecond()

	_, _, err := h.Register("a")
	if !errors.Is(err, ErrHubFull) {
		t.Fatalf("Register with both caps saturated: got %v, want ErrHubFull", err)
	}
	if errors.Is(err, ErrPerIPFull) {
		t.Fatalf("Register at the global cap reported ErrPerIPFull: %v", err)
	}
}

func TestUnregisterFreesGlobalAndPerIPCapacity(t *testing.T) {
	t.Run("perIP", func(t *testing.T) {
		h := NewHub(1000, 2, discardLogger())
		_, cancelFirst := mustRegister(t, h, "a")
		_, cancelSecond := mustRegister(t, h, "a")
		defer cancelSecond()

		if _, _, err := h.Register("a"); !errors.Is(err, ErrPerIPFull) {
			t.Fatalf("third Register under one key: got %v, want ErrPerIPFull", err)
		}

		cancelFirst()

		_, cancelThird, err := h.Register("a")
		if err != nil {
			t.Fatalf("Register after freeing per-IP capacity: %v", err)
		}
		cancelThird()
	})

	t.Run("global", func(t *testing.T) {
		h := NewHub(2, 12, discardLogger())
		_, cancelA := mustRegister(t, h, "a")
		_, cancelB := mustRegister(t, h, "b")
		defer cancelB()

		if _, _, err := h.Register("c"); !errors.Is(err, ErrHubFull) {
			t.Fatalf("third Register at globalMax 2: got %v, want ErrHubFull", err)
		}

		cancelA()

		_, cancelC, err := h.Register("c")
		if err != nil {
			t.Fatalf("Register after freeing global capacity: %v", err)
		}
		cancelC()
	})
}

func TestClientsCountMatchesRegistrations(t *testing.T) {
	h := NewHub(1000, 12, discardLogger())

	cancels := make([]func(), 0, 5)
	for i := 0; i < 5; i++ {
		_, cancel := mustRegister(t, h, fmt.Sprintf("key-%d", i))
		cancels = append(cancels, cancel)
	}
	defer func() {
		for _, cancel := range cancels[2:] {
			cancel()
		}
	}()

	cancels[0]()
	cancels[1]()

	if got := h.Clients(); got != 3 {
		t.Fatalf("Clients() = %d after 5 registers and 2 unregisters, want 3", got)
	}
}

// TestPerIPMapDropsKeysWithNoLiveStreams pins the memory bound on the hub's
// second map. The client set is bounded by globalMax, but a per-IP counter map
// that kept a zero entry forever would let a rotating spoofed source address
// mint one key per connection until the process is out of memory — the exact
// vector the bounded-key work exists to close.
func TestPerIPMapDropsKeysWithNoLiveStreams(t *testing.T) {
	h := NewHub(1000, 12, discardLogger())

	for i := 0; i < 50; i++ {
		_, cancel := mustRegister(t, h, fmt.Sprintf("key-%d", i))
		cancel()
	}

	h.mu.Lock()
	live := len(h.perIPCount)
	h.mu.Unlock()

	if live != 0 {
		t.Fatalf("perIPCount retains %d keys with no live stream, want 0", live)
	}
}

// TestHubNeverEvictsALiveStream pins the admission policy: at the cap the hub
// REFUSES a newcomer, it never drops an existing client to make room. Evicting
// an entry whose stream is still open would either leak that stream's
// goroutine or double-free its slot when the handler later unregisters.
func TestHubNeverEvictsALiveStream(t *testing.T) {
	h := NewHub(2, 12, discardLogger())

	chA, cancelA := mustRegister(t, h, "a")
	defer cancelA()
	chB, cancelB := mustRegister(t, h, "b")
	defer cancelB()

	if _, _, err := h.Register("c"); err == nil {
		t.Fatalf("Register beyond the global cap succeeded, want a refusal")
	}

	// Both incumbents must still be in the set — a hub that evicted "a" to
	// admit "c" would deliver to only one of them.
	if n := h.Broadcast(fixtureStats()); n != 2 {
		t.Fatalf("Broadcast after a refused Register delivered to %d clients, want 2", n)
	}
	recvNow(t, chA, "a")
	recvNow(t, chB, "b")
}

// TestHubIsSafeUnderConcurrentRegistersAndBroadcasts is the ONLY test in this
// file whose value depends on -race: everything it asserts about final state
// also holds for a single-threaded run. Its job is to give the race detector
// concurrent subscribe / unsubscribe / broadcast to observe, including the
// classic unregister-during-broadcast panic.
func TestHubIsSafeUnderConcurrentRegistersAndBroadcasts(t *testing.T) {
	const workers = 20
	const rounds = 50

	h := NewHub(1000, workers, discardLogger())

	var subs, pubs sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < workers; i++ {
		subs.Add(1)
		go func(i int) {
			defer subs.Done()
			key := fmt.Sprintf("key-%d", i%4)
			for j := 0; j < rounds; j++ {
				ch, cancel, err := h.Register(key)
				if err != nil {
					continue
				}
				// Drain opportunistically so some sends land and some drop.
				select {
				case <-ch:
				default:
				}
				cancel()
				cancel()
			}
		}(i)
	}

	for i := 0; i < workers; i++ {
		pubs.Add(1)
		go func() {
			defer pubs.Done()
			for {
				select {
				case <-stop:
					return
				default:
					h.Broadcast(fixtureStats())
				}
			}
		}()
	}

	// The subscribing goroutines finish on their own; the broadcasters run
	// until told to stop. Stopping them only after every register/unregister
	// pair has run is what makes the final Clients() assertion deterministic —
	// no sleep is involved in either direction.
	subsDone := make(chan struct{})
	go func() {
		defer close(subsDone)
		subs.Wait()
	}()

	timedOut := false
	select {
	case <-subsDone:
	case <-time.After(broadcastGuard):
		timedOut = true
	}
	close(stop)
	pubs.Wait()
	if timedOut {
		<-subsDone
		t.Fatalf("concurrent register/unregister workers did not finish")
	}

	if got := h.Clients(); got != 0 {
		t.Fatalf("Clients() = %d after every unregister ran, want 0", got)
	}
	h.mu.Lock()
	live := len(h.perIPCount)
	h.mu.Unlock()
	if live != 0 {
		t.Fatalf("perIPCount retains %d keys after every unregister ran, want 0", live)
	}
}

// ───────────────────────────────────────────────────────────────────────────
// Task 18: GET /api/events — the wire contract, the first push, the ping,
// the caps.
//
// Every test below runs against a REAL httptest.NewServer and a real reader.
// An httptest.ResponseRecorder buffers everything and reports success, so it
// cannot observe a flush, cannot observe frame ORDER over time, and cannot
// observe a write deadline — which are the properties under test.
// ───────────────────────────────────────────────────────────────────────────

// The SSE wire contract, stated INDEPENDENTLY of internal/api's own constants.
//
// These are DELIBERATE duplicates of sseEventName, sseContentType,
// sseHeartbeat, sseFrameSuffix and the four header names — never references to
// them. The Global Constraints forbid asserting an expected value against the
// package's own constant because both sides then mutate together: with
// `got != sseEventName`, renaming sseEventName to "statsupdate" leaves the
// whole suite GREEN while every browser silently stops receiving updates.
// Measured, not theorized — that mutation, and "text/plain" for the content
// type, and ":pong\n\n" for the heartbeat, all SURVIVED this suite before
// these literals existed.
//
// DO NOT "deduplicate" these against the package constants. Duplication is the
// entire point: this block is the wire contract the browser implements, and it
// must be able to disagree with the code.
const (
	wireEventName            = "stats_update"
	wireEventLine            = "event: stats_update"
	wireFrameTerminator      = "\n\n"
	wirePing                 = ":ping\n\n"
	wireContentType          = "text/event-stream"
	wireCacheControl         = "no-cache"
	wireConnection           = "keep-alive"
	wireAccelBuffering       = "no"
	wireHeaderContentType    = "Content-Type"
	wireHeaderCacheControl   = "Cache-Control"
	wireHeaderConnection     = "Connection"
	wireHeaderAccelBuffering = "X-Accel-Buffering"
)

// sseFixtureStats is the store-level fixture the first push and the golden
// frame are built from. All four projected values are pairwise distinct and
// none is 0 or 1, so a projection that swaps two fields, or a handler that
// emits a zero-valued statsDTO, cannot pass.
func sseFixtureStats() store.Stats {
	write := time.Date(2026, 7, 30, 9, 8, 7, 654_000_000, time.UTC)
	return store.Stats{
		ActiveStations:  19,
		TotalTx:         2701,
		TotalRecords:    51_234,
		LastRecordWrite: &write,
	}
}

// sseBroadcastStats is a SECOND, entirely different fixture, used for the
// broadcast test. Every field differs from sseFixtureStats, so a handler that
// replays the first payload on every event fails.
func sseBroadcastStats() store.Stats {
	write := time.Date(2026, 7, 31, 1, 2, 3, 4_000_000, time.UTC)
	return store.Stats{
		ActiveStations:  23,
		TotalTx:         3117,
		TotalRecords:    60_001,
		LastRecordWrite: &write,
	}
}

// fixedStatsStore serves a caller-controlled store.Stats and delegates
// everything else to the embedded StationStore. It exists because the fake's
// totalTx / totalRecords counters are only movable through a full
// insert-claim-complete cycle, which cannot produce four pairwise-distinct
// values (one Complete call is exactly one tx).
type fixedStatsStore struct {
	store.StationStore

	stats store.Stats
	err   error
}

func (f *fixedStatsStore) Stats(_ context.Context) (store.Stats, error) {
	if f.err != nil {
		return store.Stats{}, f.err
	}
	return f.stats, nil
}

// sseHarness is a real HTTP server carrying the PRODUCTION router, so every
// test below runs through withRequestID → withSecurityHeaders → withRecover →
// withWriteDeadline → the SSE limiter → markSSEExempt → handleEvents. Testing
// handleEvents in isolation would not catch a missing markSSEExempt at the
// registration site, which is the defect that kills every stream at 30 s.
type sseHarness struct {
	srv *httptest.Server
	hub *Hub
}

func newSSEHarness(t *testing.T, mutate func(d *Deps)) *sseHarness {
	t.Helper()
	d := testDeps(t)
	d.Logger = discardLogger()
	if mutate != nil {
		mutate(&d)
	}
	srv := httptest.NewServer(NewRouter(d))
	t.Cleanup(srv.Close)
	return &sseHarness{srv: srv, hub: d.Hub}
}

// withFixedStats returns a Deps mutator replacing only the Stations seam's
// Stats, leaving List and Get on the fake.
func withFixedStats(s store.Stats) func(d *Deps) {
	return func(d *Deps) {
		d.Store.Stations = &fixedStatsStore{StationStore: d.Store.Stations, stats: s}
	}
}

// sseConn is one open stream plus a goroutine that parses frames off it as
// they arrive. The goroutine is what makes every assertion below a channel
// receive with a failure-path timeout rather than a sleep.
type sseConn struct {
	resp   *http.Response
	frames chan string
	cancel context.CancelFunc
}

// connect opens one stream. It returns the response head immediately — the
// handler flushes it before doing anything else — so a refusal (429 / 503) is
// observable here with its body.
func (h *sseHarness) connect(t *testing.T, headers map[string]string) *sseConn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, h.srv.URL+pathEvents, nil)
	if reqErr != nil {
		cancel()
		t.Fatalf("new request: %v", reqErr)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	// Do is driven on its own goroutine with a bounded wait, because it does
	// not return until the response HEAD reaches the client — and the head only
	// reaches the client if the handler flushed it. Calling Do inline would turn
	// a missing flush into a whole-suite timeout panic naming nothing; this
	// turns it into a named failure on the test that connected.
	type result struct {
		resp *http.Response
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, doErr := h.srv.Client().Do(req)
		if doErr != nil {
			done <- result{err: doErr}
			return
		}
		select {
		case done <- result{resp: resp}:
			// The caller now owns the body and registers its Close in a
			// t.Cleanup below.
		case <-time.After(broadcastGuard):
			// Nobody is waiting any more — the caller already timed out and
			// failed the test. Close the body here rather than leaking the
			// connection for the rest of the run.
			_ = resp.Body.Close()
		}
	}()

	var resp *http.Response
	select {
	case got := <-done:
		if got.err != nil {
			cancel()
			t.Fatalf("GET %s: %v", pathEvents, got.err)
		}
		resp = got.resp
	case <-time.After(broadcastGuard):
		cancel()
		t.Fatalf("timed out waiting for the %s response head: the handler never flushed it", pathEvents)
	}
	c := &sseConn{resp: resp, frames: make(chan string, 64), cancel: cancel}
	t.Cleanup(func() {
		cancel()
		if closeErr := resp.Body.Close(); closeErr != nil {
			_ = closeErr
		}
	})
	if resp.StatusCode == http.StatusOK {
		go c.read()
	}
	return c
}

// read pushes one string per SSE frame — every byte up to and including the
// blank line that terminates it — and closes the channel when the stream ends.
func (c *sseConn) read() {
	defer close(c.frames)
	br := bufio.NewReader(c.resp.Body)
	for {
		frame, readErr := readSSEFrame(br)
		if frame != "" {
			c.frames <- frame
		}
		if readErr != nil {
			return
		}
	}
}

// readSSEFrame reads one frame VERBATIM, blank-line terminator included. It
// does not normalize anything: the terminator and the exact field-line bytes
// are what the assertions are about.
func readSSEFrame(br *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		line, readErr := br.ReadString('\n')
		sb.WriteString(line)
		if readErr != nil {
			return sb.String(), readErr
		}
		if line == "\n" {
			return sb.String(), nil
		}
	}
}

// next returns the next frame, failing on a timeout or a closed stream. The
// timeout is a failure path only — it is never used to sequence anything.
func (c *sseConn) next(t *testing.T) string {
	t.Helper()
	select {
	case frame, ok := <-c.frames:
		if !ok {
			t.Fatal("the stream ended before a frame arrived")
		}
		return frame
	case <-time.After(broadcastGuard):
		t.Fatal("timed out waiting for an SSE frame")
		return ""
	}
}

// nextStatsFrame returns the next frame that is not a heartbeat comment. Only
// the tests that shorten the heartbeat interval can see a ping at all.
func (c *sseConn) nextStatsFrame(t *testing.T) string {
	t.Helper()
	for {
		frame := c.next(t)
		if !strings.HasPrefix(frame, ":") {
			return frame
		}
	}
}

// parsedFrame is a frame decomposed by an SSE parser rather than by substring
// matching. A `strings.Contains(body, "stats_update")` assertion passes on a
// frame no EventSource would ever dispatch.
type parsedFrame struct {
	raw     string
	event   string
	data    string
	comment string
}

// parseSSEFrame decomposes one frame and asserts its structural invariants:
// it must end with the blank line, and it must carry no bare CR.
func parseSSEFrame(t *testing.T, raw string) parsedFrame {
	t.Helper()
	if !strings.HasSuffix(raw, wireFrameTerminator) {
		t.Fatalf("frame is not terminated by a blank line, so no client would ever dispatch it: %q", raw)
	}
	out := parsedFrame{raw: raw}
	for _, line := range strings.Split(strings.TrimSuffix(raw, wireFrameTerminator), "\n") {
		switch {
		case strings.HasPrefix(line, ":"):
			out.comment = strings.TrimPrefix(line, ":")
		case strings.HasPrefix(line, "event: "):
			out.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			out.data = strings.TrimPrefix(line, "data: ")
		}
	}
	return out
}

// wireStats is statsDTO as a CLIENT sees it: the same four json keys, with
// the timestamp as a plain string.
//
// The brief asked for the data line to be unmarshaled "into a statsDTO", and
// that fixture is not implementable as written: isoMillis is marshal-ONLY (it
// has MarshalJSON and deliberately no UnmarshalJSON, because nothing in this
// app ever parses one back), so json.Unmarshal into a statsDTO fails on the
// lastRecordWrite field for EVERY input including a perfectly correct one —
// the assertion could never pass, let alone discriminate. This mirror type is
// the replacement: it round-trips the wire shape, keeps all four keys, and is
// deep-equal comparable against an expected value built WITHOUT toStats.
//
// KNOWN COUPLING, deliberate: a newly ADDED statsDTO field is invisible here —
// json.Unmarshal ignores an unknown key, so these tests would keep passing.
// The gate for that is testdata/stations_list.json, whose golden bytes carry
// the same four keys and break on a fifth. If that golden ever stops covering
// statsDTO, this type needs the new field too.
type wireStats struct {
	ActiveStations  int64   `json:"activeStations"`
	TotalTx         int64   `json:"totalTx"`
	LastRecordWrite *string `json:"lastRecordWrite"`
	TotalDataPoints int64   `json:"totalDataPoints"`
}

// wantStats is the wireStats a store.Stats fixture must project to. Every
// value is computed here from the fixture rather than by calling toStats or
// isoPtr, so a broken projection cannot satisfy the assertion by mutating both
// sides of it.
func wantStats(t *testing.T, s store.Stats) wireStats {
	t.Helper()
	out := wireStats{
		ActiveStations:  s.ActiveStations,
		TotalTx:         s.TotalTx,
		TotalDataPoints: s.TotalRecords * weather.DataFieldsPerRecord,
	}
	if s.LastRecordWrite != nil {
		stamp := s.LastRecordWrite.UTC().Format("2006-01-02T15:04:05.000Z")
		out.LastRecordWrite = &stamp
	}
	return out
}

// decodeStatsData parses a frame's data line. It first asserts all four keys
// are PRESENT — a payload missing lastRecordWrite would otherwise decode
// cleanly into a nil pointer and compare equal to an expected nil.
func decodeStatsData(t *testing.T, frame parsedFrame) wireStats {
	t.Helper()
	var keys map[string]json.RawMessage
	if keysErr := json.Unmarshal([]byte(frame.data), &keys); keysErr != nil {
		t.Fatalf("data line %q is not a JSON object: %v", frame.data, keysErr)
	}
	for _, key := range []string{"activeStations", "totalTx", "lastRecordWrite", "totalDataPoints"} {
		if _, ok := keys[key]; !ok {
			t.Fatalf("data line %q is missing the %q key", frame.data, key)
		}
	}
	var got wireStats
	if unmarshalErr := json.Unmarshal([]byte(frame.data), &got); unmarshalErr != nil {
		t.Fatalf("data line %q does not unmarshal into the wire stats shape: %v", frame.data, unmarshalErr)
	}
	return got
}

// setShortHeartbeat shortens the comment-frame period so the ping is
// observable in milliseconds instead of 30 seconds, restoring it afterwards.
func setShortHeartbeat(t *testing.T) time.Duration {
	t.Helper()
	original := sseHeartbeatInterval
	short := 50 * time.Millisecond
	sseHeartbeatInterval = short
	t.Cleanup(func() { sseHeartbeatInterval = original })
	return short
}

func TestEventsSetsTheStreamingHeaders(t *testing.T) {
	h := newSSEHarness(t, withFixedStats(sseFixtureStats()))
	c := h.connect(t, nil)

	if c.resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", c.resp.StatusCode)
	}
	if got := c.resp.Header.Get(wireHeaderContentType); got != wireContentType {
		t.Errorf("%s = %q, want %q", wireHeaderContentType, got, wireContentType)
	}
	if got := c.resp.Header.Get(wireHeaderCacheControl); got != wireCacheControl {
		t.Errorf("%s = %q, want %q", wireHeaderCacheControl, got, wireCacheControl)
	}
	if got := c.resp.Header.Get(wireHeaderConnection); got != wireConnection {
		t.Errorf("%s = %q, want %q", wireHeaderConnection, got, wireConnection)
	}
	if got := c.resp.Header.Get(wireHeaderAccelBuffering); got != wireAccelBuffering {
		t.Errorf("%s = %q, want %q", wireHeaderAccelBuffering, got, wireAccelBuffering)
	}
}

// TestEventsWritesOneStatsUpdateImmediatelyOnConnect never calls Broadcast, so
// a handler that only writes on a broadcast cannot pass it (spec §13.7: the
// tiles must populate without waiting).
func TestEventsWritesOneStatsUpdateImmediatelyOnConnect(t *testing.T) {
	want := sseFixtureStats()
	h := newSSEHarness(t, withFixedStats(want))
	c := h.connect(t, nil)

	frame := parseSSEFrame(t, c.next(t))
	if frame.event != wireEventName {
		t.Fatalf("first frame event = %q, want %q; raw=%q", frame.event, wireEventName, frame.raw)
	}
	if got, expected := decodeStatsData(t, frame), wantStats(t, want); !reflect.DeepEqual(got, expected) {
		t.Fatalf("first push payload = %+v, want %+v", got, expected)
	}
}

func TestEventsEventNameIsExactlyStatsUpdate(t *testing.T) {
	h := newSSEHarness(t, withFixedStats(sseFixtureStats()))
	c := h.connect(t, nil)

	raw := c.next(t)
	if !strings.Contains(raw, wireEventLine+"\n") {
		t.Fatalf("frame carries no %q line: %q", wireEventLine, raw)
	}
	// Each negative form is a plausible typo and each silently breaks the
	// client: EventSource dispatches on the exact field value.
	for _, wrong := range []string{"event: message", "event: stats-update", "event:" + wireEventName} {
		if strings.Contains(raw, wrong) {
			t.Errorf("frame contains %q, which no listener is registered for: %q", wrong, raw)
		}
	}
}

func TestEventsDataIsASingleLineOfValidJSON(t *testing.T) {
	h := newSSEHarness(t, withFixedStats(sseFixtureStats()))
	c := h.connect(t, nil)

	frame := parseSSEFrame(t, c.next(t))
	if frame.data == "" {
		t.Fatalf("frame carries no data line: %q", frame.raw)
	}
	if strings.Contains(frame.data, "\n") {
		t.Fatalf("data line contains an embedded newline, which ends the frame early: %q", frame.data)
	}
	decodeStatsData(t, frame)
}

func TestEventsFrameEndsWithABlankLine(t *testing.T) {
	h := newSSEHarness(t, withFixedStats(sseFixtureStats()))
	c := h.connect(t, nil)

	raw := c.next(t)
	if !strings.HasSuffix(raw, wireFrameTerminator) {
		t.Fatalf("frame does not end with a blank line, so the client never dispatches it: %q", raw)
	}
	// The blank line must be the ONLY blank line: an extra one before the data
	// field would dispatch an event with an empty payload.
	if strings.Contains(strings.TrimSuffix(raw, wireFrameTerminator), wireFrameTerminator) {
		t.Fatalf("frame contains a blank line before its terminator: %q", raw)
	}

	// And there must be no terminator too many. json.Encoder.Encode appends a
	// newline of its own, which turns the frame's tail into "\n\n\n" — the
	// frame itself still parses, but the extra byte becomes an EMPTY frame that
	// desynchronizes every subsequent event on the stream. Checked here rather
	// than on the data line, because as a parser sees it the stray newline
	// lands outside the data field, not inside it.
	if n := h.hub.Broadcast(toStats(sseBroadcastStats())); n != 1 {
		t.Fatalf("Broadcast delivered to %d clients, want 1", n)
	}
	// An EMPTY frame is a lone terminator line: everything before the blank
	// line is nothing at all.
	if next := c.next(t); strings.TrimLeft(next, "\n") == "" {
		t.Fatalf("the frame is followed by an EMPTY frame (%q), so the stream is one terminator out of step: %q", next, raw)
	}
}

func TestEventsWritesASubsequentBroadcast(t *testing.T) {
	h := newSSEHarness(t, withFixedStats(sseFixtureStats()))
	c := h.connect(t, nil)

	first := decodeStatsData(t, parseSSEFrame(t, c.next(t)))

	later := wantStats(t, sseBroadcastStats())
	if reflect.DeepEqual(first, later) {
		t.Fatal("the two fixtures are equal, so this test could not detect a replayed payload")
	}
	if n := h.hub.Broadcast(toStats(sseBroadcastStats())); n != 1 {
		t.Fatalf("Broadcast delivered to %d clients, want 1", n)
	}

	second := decodeStatsData(t, parseSSEFrame(t, c.nextStatsFrame(t)))
	if !reflect.DeepEqual(second, later) {
		t.Fatalf("second frame payload = %+v, want the broadcast value %+v", second, later)
	}
}

func TestEventsWritesAPingCommentAndItIsNotAStatsUpdate(t *testing.T) {
	setShortHeartbeat(t)
	h := newSSEHarness(t, withFixedStats(sseFixtureStats()))
	c := h.connect(t, nil)

	// Frame 1 is the first push; the ping is whatever comes next, with no
	// broadcast in between.
	c.next(t)

	ping := c.next(t)
	if ping != wirePing {
		t.Fatalf("heartbeat frame = %q, want exactly %q", ping, wirePing)
	}
	// Spec §13.7: the client JSON.parses every stats_update inside a bare
	// catch, so a heartbeat shaped like one is invisible breakage.
	if strings.Contains(ping, wireEventName) {
		t.Fatalf("the heartbeat is a %s frame: %q", wireEventName, ping)
	}
}

// TestEventsFlushesPerWrite reads the first frame with the connection still
// OPEN. That is the flush assertion in its only honest form: net/http buffers
// the response, so without a Flush the read blocks until the handler returns
// and this test times out.
func TestEventsFlushesPerWrite(t *testing.T) {
	h := newSSEHarness(t, withFixedStats(sseFixtureStats()))
	c := h.connect(t, nil)

	frame := parseSSEFrame(t, c.next(t))
	if frame.event != wireEventName {
		t.Fatalf("first flushed frame event = %q, want %q", frame.event, wireEventName)
	}
	// The stream must still be open: a frame that only became readable because
	// the handler returned would prove nothing about flushing.
	if h.hub.Clients() != 1 {
		t.Fatalf("hub Clients() = %d after the first frame, want 1 (the handler must still be running)", h.hub.Clients())
	}
}

func TestEventsReturnsWhenTheClientDisconnects(t *testing.T) {
	h := newSSEHarness(t, withFixedStats(sseFixtureStats()))
	c := h.connect(t, nil)

	// Liveness first: without this the "reaches 0" assertion below holds
	// vacuously against a handler that never registered at all.
	c.next(t)
	if got := h.hub.Clients(); got != 1 {
		t.Fatalf("hub Clients() = %d with one open stream, want 1", got)
	}

	c.cancel()
	waitForClients(t, h.hub, 0)
}

// waitForClients polls the hub's count until it reaches want, failing on a
// bounded timeout. Polling is unavoidable here: the handler's unregister runs
// on the SERVER's goroutine after the client's context propagates, and the hub
// exposes no notification seam. The poll interval is a small fraction of the
// failure timeout, so this is a bounded wait and not a sleep-as-synchronization.
func waitForClients(t *testing.T, h *Hub, want int) {
	t.Helper()
	deadline := time.Now().Add(broadcastGuard)
	for time.Now().Before(deadline) {
		if got := h.Clients(); got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("hub Clients() = %d after %v, want %d: the handler leaked its slot", h.Clients(), broadcastGuard, want)
}

// TestEventsRefusesTheThirteenthConcurrentStreamFromOneIP drives spec §6.1's
// per-IP concurrency cap. It asserts the DISTINCT hub message rather than the
// limiter's, so a 429 from the 30/min rate scope could not satisfy it.
func TestEventsRefusesTheThirteenthConcurrentStreamFromOneIP(t *testing.T) {
	h := newSSEHarness(t, withFixedStats(sseFixtureStats()))

	conns := make([]*sseConn, 0, sseConcurrentPerIP)
	for i := range sseConcurrentPerIP {
		c := h.connect(t, nil)
		if c.resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d: status = %d, want 200", i, c.resp.StatusCode)
		}
		conns = append(conns, c)
	}

	refused := h.connect(t, nil)
	if refused.resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("stream %d: status = %d, want 429", sseConcurrentPerIP+1, refused.resp.StatusCode)
	}
	assertRefusalBody(t, refused, msgTooManyStreams, false)

	// The twelve incumbents must still be open — the hub refuses, it never
	// evicts — and every one of them must still receive.
	if got := h.hub.Clients(); got != sseConcurrentPerIP {
		t.Fatalf("hub Clients() = %d after the refusal, want %d", got, sseConcurrentPerIP)
	}
	if n := h.hub.Broadcast(toStats(sseBroadcastStats())); n != sseConcurrentPerIP {
		t.Fatalf("Broadcast reached %d incumbents, want %d", n, sseConcurrentPerIP)
	}
	for i, c := range conns {
		if frame := parseSSEFrame(t, c.nextStatsFrame(t)); frame.event != wireEventName {
			t.Fatalf("incumbent %d: event = %q, want %q", i, frame.event, wireEventName)
		}
	}
}

// TestEventsAnswersFiveHundredThreeAtTheGlobalCap uses THREE DISTINCT peers,
// so the per-IP cap cannot be what refused the third, and asserts 503 rather
// than 429 — the status is the only thing that tells an operator which cap
// fired.
func TestEventsAnswersFiveHundredThreeAtTheGlobalCap(t *testing.T) {
	const globalMax = 2

	h := newSSEHarness(t, func(d *Deps) {
		d.Hub = NewHub(globalMax, sseConcurrentPerIP, discardLogger())
		d.Trusted = trustLoopback(t)
		d.Store.Stations = &fixedStatsStore{StationStore: d.Store.Stations, stats: sseFixtureStats()}
	})

	// Distinct keys over one loopback socket: the peer is trusted, so
	// CF-Connecting-IP is honored (and is the only header consulted).
	for i, peer := range []string{"198.51.100.1", "198.51.100.2"} {
		c := h.connect(t, map[string]string{headerCFConnectingIP: peer})
		if c.resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d from %s: status = %d, want 200", i, peer, c.resp.StatusCode)
		}
	}

	refused := h.connect(t, map[string]string{headerCFConnectingIP: "198.51.100.3"})
	if refused.resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("the third stream from a third peer: status = %d, want 503", refused.resp.StatusCode)
	}
	assertRefusalBody(t, refused, msgStreamCapacityReached, true)
}

// headerCFConnectingIP is the trusted-peer forwarding header the resolver
// consults first. It is declared here rather than imported because
// internal/ratelimit keeps its own copy unexported.
const headerCFConnectingIP = "CF-Connecting-IP"

// trustLoopback trusts 127.0.0.0/8, which is what makes an httptest client's
// CF-Connecting-IP readable and therefore what lets one loopback socket stand
// in for distinct peers.
func trustLoopback(t *testing.T) config.TrustedProxies {
	t.Helper()
	trusted, parseErr := config.ParseCIDRList([]string{"127.0.0.0/8"})
	if parseErr != nil {
		t.Fatalf("ParseCIDRList: %v", parseErr)
	}
	return trusted
}

// assertRefusalBody checks the refusal is the package's standard JSON error
// shape with the expected message — not an empty body and not a stream that
// opened anyway. withRequestID is present on a 5xx and absent below it.
func assertRefusalBody(t *testing.T, c *sseConn, wantMsg string, wantRequestID bool) {
	t.Helper()
	if got := c.resp.Header.Get(headerContentType); got != contentTypeJSON {
		t.Errorf("refusal %s = %q, want %q", headerContentType, got, contentTypeJSON)
	}
	body, readErr := io.ReadAll(c.resp.Body)
	if readErr != nil {
		t.Fatalf("read refusal body: %v", readErr)
	}
	var decoded serverErrorDTO
	if unmarshalErr := json.Unmarshal(body, &decoded); unmarshalErr != nil {
		t.Fatalf("refusal body %q does not unmarshal: %v", body, unmarshalErr)
	}
	if decoded.Error != wantMsg {
		t.Errorf("refusal error = %q, want %q", decoded.Error, wantMsg)
	}
	if wantRequestID && decoded.RequestID == "" {
		t.Errorf("refusal body carries no request_id: %q", body)
	}
	if !wantRequestID && decoded.RequestID != "" {
		t.Errorf("refusal body carries a request_id on a 4xx: %q", body)
	}
}

// TestEventsCarriesTheSecurityHeaders is asserted for this route specifically:
// the SSE handler is the one that writes its own header block, and therefore
// the likeliest to bypass withSecurityHeaders.
func TestEventsCarriesTheSecurityHeaders(t *testing.T) {
	h := newSSEHarness(t, withFixedStats(sseFixtureStats()))
	c := h.connect(t, nil)

	for name, want := range map[string]string{
		headerContentTypeOptions: valueNoSniff,
		headerFrameOptions:       valueDeny,
		headerReferrerPolicy:     valueNoReferrer,
	} {
		if got := c.resp.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// TestEventsHasNoWriteDeadline is the BEHAVIORAL half of the §6.0 control-20
// asymmetry and the pair to Task 14's TestNoWriteDeadlineOnAnSSEExemptHandler:
// it is what fails if the route loses its markSSEExempt wrapper at the
// registration site, which nothing else in the package enforces.
//
// The elapsed time past the deadline is established by COUNTING HEARTBEATS,
// not by sleeping: with a 50 ms ping and a 150 ms deadline, four pings mean at
// least 200 ms of a still-writable stream.
func TestEventsHasNoWriteDeadline(t *testing.T) {
	short := setShortWriteDeadline(t)
	ping := setShortHeartbeat(t)

	h := newSSEHarness(t, withFixedStats(sseFixtureStats()))
	c := h.connect(t, nil)
	c.next(t)

	needed := int(short/ping) + 1
	for i := range needed {
		if got := c.next(t); got != wirePing {
			t.Fatalf("frame %d after the first push = %q, want the heartbeat %q", i, got, wirePing)
		}
	}

	// Well past writeDeadline now. A deadline-bearing stream's flush would
	// have failed and the handler would have returned.
	if n := h.hub.Broadcast(toStats(sseBroadcastStats())); n != 1 {
		t.Fatalf("Broadcast delivered to %d clients past the write deadline, want 1", n)
	}
	frame := parseSSEFrame(t, c.nextStatsFrame(t))
	if frame.event != wireEventName {
		t.Fatalf("event past the write deadline = %q, want %q", frame.event, wireEventName)
	}
}

// TestEventsStatsFailureStillOpensTheStream pins the deliberate decision that
// a failed stats read omits the first push rather than refusing the
// connection: the stream's job is to deliver FUTURE updates, and a 500 here
// would turn a transient DB blip into a dead Live dot until the user reloads.
func TestEventsStatsFailureStillOpensTheStream(t *testing.T) {
	h := newSSEHarness(t, func(d *Deps) {
		s := fake.New()
		s.FailAll = errors.New("SQLSTATE 42P01: relation \"stations\" does not exist")
		d.Store.Stations = s.Stations()
	})
	c := h.connect(t, nil)

	if c.resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: a failed stats read must not refuse the stream", c.resp.StatusCode)
	}
	if got := c.resp.Header.Get(wireHeaderContentType); got != wireContentType {
		t.Errorf("%s = %q, want %q", wireHeaderContentType, got, wireContentType)
	}

	// The FIRST PUSH is omitted, not sent empty and not sent as a broken
	// frame. A later broadcast still arrives as frame one.
	waitForClients(t, h.hub, 1)
	later := wantStats(t, sseBroadcastStats())
	if n := h.hub.Broadcast(toStats(sseBroadcastStats())); n != 1 {
		t.Fatalf("Broadcast delivered to %d clients, want 1", n)
	}
	frame := parseSSEFrame(t, c.nextStatsFrame(t))
	if got := decodeStatsData(t, frame); !reflect.DeepEqual(got, later) {
		t.Fatalf("the first frame on the wire = %+v, want the broadcast value %+v "+
			"(an omitted first push, not an empty one)", got, later)
	}
}

// TestRegisterRefusalReturnsANilChannelAndANilUnregister pins Task 17's
// refusal contract, which had no test until Task 18 became its first consumer.
// handleEvents DEFERS the returned func, so a future change returning a
// non-nil no-op would let a refusal look like an admission — and a change
// returning a non-nil CHANNEL would let a refused handler wait on a channel
// nothing ever sends to.
func TestRegisterRefusalReturnsANilChannelAndANilUnregister(t *testing.T) {
	// BOTH refusal branches, because the global cap is checked FIRST: a hub
	// with globalMax 1 can never reach the per-IP branch at all, so a
	// single-case version of this test leaves ErrPerIPFull's return statement
	// completely uncovered — verified by mutating exactly that line.
	cases := []struct {
		name      string
		hub       *Hub
		wantErr   error
		otherKeys []string
	}{
		{
			name:    "the global cap",
			hub:     NewHub(1, 12, discardLogger()),
			wantErr: ErrHubFull,
		},
		{
			name:    "the per-IP cap",
			hub:     NewHub(500, 1, discardLogger()),
			wantErr: ErrPerIPFull,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, admitted, admitErr := tc.hub.Register("a")
			if admitErr != nil {
				t.Fatalf("the first Register was refused: %v", admitErr)
			}
			defer admitted()

			ch, unregister, refusedErr := tc.hub.Register("a")
			if !errors.Is(refusedErr, tc.wantErr) {
				t.Fatalf("the second Register returned %v, want %v — this case is not exercising the branch it names", refusedErr, tc.wantErr)
			}
			if ch != nil {
				t.Error("a refused Register returned a non-nil channel; nothing will ever send to it")
			}
			if unregister != nil {
				t.Error("a refused Register returned a non-nil unregister func; a refusal must not look like an admission, and handleEvents deliberately does not defer it")
			}
		})
	}
}
