package ratelimit

import (
	"container/list"
	"sync"
	"time"
)

// DefaultMaxKeys is spec §6.1's bound on the key map.
const DefaultMaxKeys = 10_000

// Decision is what the middleware needs to write RateLimit-Limit,
// RateLimit-Remaining, RateLimit-Reset and Retry-After.
type Decision struct {
	OK bool

	// Limit is the TOTAL a single key may consume in one window, i.e.
	// limit+burst — and it is that same total in EVERY window, because burst is
	// a permanent capacity add-on and not a start-of-window grace. Remaining
	// counts down from this total, so the pair is always consistent.
	//
	// Consequence Task 16 must pin KNOWINGLY rather than "fix": the general
	// /api/* scope is New(600, 120, ...) and therefore advertises
	// RateLimit-Limit: 720, not 600. That is inside the
	// availability-over-strictness trade spec §6.1 records — the 600 itself was
	// raised from the TypeScript's 100 because one engaged user costs ~103
	// requests, and a limiter that fires on a legitimate session becomes a
	// retry loop in the SPA.
	Limit      int
	Remaining  int
	ResetAfter time.Duration
}

// window is one key's fixed-window state.
type window struct {
	key   string
	count int
	start time.Time
}

// Limiter is a fixed-window counter over a BOUNDED key map.
//
// The bound is the control, not an optimization: an untrusted peer that can
// influence the key would otherwise mint entries until the process is out of
// memory. At maxKeys the limiter evicts the LEAST RECENTLY SEEN entry rather
// than refusing to track, because refusing to track means allowing, and
// refusing to SERVE when the map is full is a self-inflicted outage. Evicting
// the NEWEST entry instead would let a key-minting peer permanently lock real
// clients out of the map, which is why the order matters.
type Limiter struct {
	limit   int
	burst   int
	window  time.Duration
	maxKeys int
	now     func() time.Time

	mu      sync.Mutex
	entries map[string]*list.Element // key -> element whose Value is *window
	order   *list.List               // front = most recently seen
}

// New builds a limiter.
//
// The ceiling a single key may consume in one window is limit+burst, and it is
// that in EVERY window: burst is a permanent add-on to capacity, not an extra
// allowance granted only at the start of a window. New(600, 120, time.Minute,
// …) therefore permits 720 requests per minute per key and reports
// Decision.Limit == 720 — see Decision.Limit. Pass burst 0 for a scope whose
// advertised number must equal its enforced number.
//
// maxKeys caps the key map; pass DefaultMaxKeys. A maxKeys below 1 is coerced to
// DefaultMaxKeys so a zero-valued wiring cannot produce an unbounded map.
//
// now is injectable so tests need no wall clock; a nil now means time.Now.
func New(limit, burst int, window time.Duration, maxKeys int, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	if maxKeys < 1 {
		maxKeys = DefaultMaxKeys
	}
	return &Limiter{
		limit:   limit,
		burst:   burst,
		window:  window,
		maxKeys: maxKeys,
		now:     now,
		entries: make(map[string]*list.Element),
		order:   list.New(),
	}
}

// capacity is the total a single key may consume in one window.
func (l *Limiter) capacity() int {
	return l.limit + l.burst
}

// Allow records one hit against key and reports the decision plus the header
// values every 429 and every allowed response carries.
//
// A REFUSAL DOES NOT INCREMENT the counter. The window is fixed, not sliding,
// so a client hammering a closed window can never push its own reset further
// out; ResetAfter keeps shrinking toward the boundary and the window opens on
// time. Counting refusals would turn a burst of blocked retries — which is
// exactly what the SPA's un-throttled 429 retry loop produces — into an
// indefinite lockout. Pinned by TestAllowRefusalsDoNotExtendTheWindow.
func (l *Limiter) Allow(key string) Decision {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	w := l.touch(key, now)
	if now.Sub(w.start) >= l.window {
		w.start = now
		w.count = 0
	}

	capacity := l.capacity()
	resetAfter := l.window - now.Sub(w.start)
	if resetAfter < 0 {
		resetAfter = 0
	}

	if w.count >= capacity {
		return Decision{OK: false, Limit: capacity, Remaining: 0, ResetAfter: resetAfter}
	}
	w.count++
	return Decision{OK: true, Limit: capacity, Remaining: capacity - w.count, ResetAfter: resetAfter}
}

// touch returns key's window, creating it (and evicting the least recently
// seen entry when the map is full) and marking it most recently seen. Callers
// hold l.mu.
func (l *Limiter) touch(key string, now time.Time) *window {
	if el, ok := l.entries[key]; ok {
		l.order.MoveToFront(el)
		w, _ := el.Value.(*window)
		return w
	}
	for len(l.entries) >= l.maxKeys {
		oldest := l.order.Back()
		if oldest == nil {
			break
		}
		l.order.Remove(oldest)
		if evicted, ok := oldest.Value.(*window); ok {
			delete(l.entries, evicted.key)
		}
	}
	w := &window{key: key, start: now}
	l.entries[key] = l.order.PushFront(w)
	return w
}

// Keys is the current map size. Exported for the bounded-key test and for the
// heartbeat; it is not part of the limiting logic.
func (l *Limiter) Keys() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}
