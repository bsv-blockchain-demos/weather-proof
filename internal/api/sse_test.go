package api

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
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
