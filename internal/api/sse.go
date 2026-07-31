package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/bsv-blockchain-demos/weather-proof/internal/ratelimit"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

// ErrHubFull and ErrPerIPFull are the two refusal reasons. They are distinct
// errors, not one, because the handler answers 503 for the global cap and 429
// for the per-IP cap: one is our capacity, the other is one client's share.
// Collapsing them would tell a single well-behaved client that the service is
// down, or tell an operator that a capacity ceiling was one client's fault.
var (
	ErrHubFull   = errors.New("api: sse global capacity reached")
	ErrPerIPFull = errors.New("api: sse per-client capacity reached")
)

// sseClientBuffer is the depth of every client channel. One, deliberately.
//
// A stats_update replaces the WHOLE stats slice in the frontend (spec §13.7),
// so a queued update is worth nothing once a newer one exists — the only
// payload a reconnecting or slow reader needs is the latest. Depth 1 plus a
// non-blocking send means the newest value a client can still absorb is always
// in flight, and a reader that has fallen behind loses only intermediate
// states it would have overwritten anyway. Any deeper buffer would trade real
// memory for stale frames; an UNBOUNDED channel would make one hung browser
// tab a memory vector.
const sseClientBuffer = 1

// sseClient is one live /api/events stream's delivery endpoint. It is
// identified by pointer identity in the hub's set, so two clients from the
// same ipKey are distinct entries and unregistering one cannot remove the
// other.
type sseClient struct {
	ch    chan statsDTO
	ipKey string
}

// Hub fans stats_update payloads out to every connected /api/events client.
//
// It owns the bounded client set and nothing about HTTP. The TypeScript's
// sse.ts:17 is an UNBOUNDED Set and each client pins a Response, a 30 s
// interval timer and a socket, so its 10/min rate limit was the only thing
// between the app and unbounded goroutine and socket growth. The caps here are
// the replacement.
//
// # Bounded-set policy: refuse, NEVER evict
//
// Every entry in the set corresponds to a stream that is still open, so there
// is no such thing as a cold entry to evict. At either cap the hub REFUSES the
// newcomer and returns an error; it never drops an incumbent to make room.
// Eviction and liveness interact badly in exactly two ways, and both are
// silent: dropping an entry whose handler is still running leaks that stream
// (it keeps its goroutine, its socket and its slot in the per-IP count forever
// because the handler never learns it was evicted), and if the handler later
// runs its own unregister, the slot is released twice — driving the per-IP
// counter below zero and handing that one key unlimited streams. Refusal has
// neither failure mode: the caller learns immediately and never gets a channel
// it has to clean up. Pinned by TestHubNeverEvictsALiveStream.
//
// Both maps are therefore bounded by globalMax: clients directly, and
// perIPCount because a key exists only while it has at least one live stream —
// the counter's entry is DELETED when it reaches zero rather than left at 0.
// Without that delete, a rotating (spoofed, or merely NATed) source address
// would mint one permanent map key per connection, which is the same
// unbounded-key memory vector internal/ratelimit's bounded map exists to
// close. Pinned by TestPerIPMapDropsKeysWithNoLiveStreams.
//
// # Shutdown contract
//
// The hub owns NO goroutines and starts none, so there is nothing to leak and
// no Close to call: its whole lifetime is the process's. It also never closes a
// client channel. Broadcast copies the client set under the lock and sends
// after releasing it, so a send can legitimately race with an unregister that
// has already completed; if unregister closed the channel, that send would
// panic with "send on closed channel" on the publisher's goroutine and take
// down the request that triggered the broadcast. Instead, unregister only
// removes the client from the set — the abandoned buffered channel holds at
// most one statsDTO and is garbage collected with the client. A reader's own
// termination signal is its request context (spec §13.7's
// r.Context().Done()), not a channel close, which means Task 18's handler must
// never range over the channel expecting it to end. The unregister func
// returned by Register is idempotent and safe to call from any goroutine, so
// the handler can both defer it and call it on an error path.
type Hub struct {
	mu      sync.Mutex
	clients map[*sseClient]struct{}

	// perIPCount is the live stream count per ip key. See the type comment:
	// an entry at zero is deleted, never retained.
	perIPCount map[string]int

	globalMax int
	perIP     int

	log *slog.Logger
}

// NewHub builds a hub. globalMax is the total concurrent stream cap
// (sseGlobalMax); perIP is the per-client cap (sseConcurrentPerIP).
//
// A non-positive cap is coerced to 1 rather than treated as unlimited, for the
// same reason newLimiters floors PROOF_RATE_LIMIT_PER_MIN: a zero that means
// "no limit" turns a misconfiguration into the unbounded growth these caps
// exist to prevent. A nil logger falls back to slog.Default so a caller that
// forgot one gets logs rather than a panic on the first refusal.
func NewHub(globalMax, perIP int, log *slog.Logger) *Hub {
	if globalMax < 1 {
		globalMax = 1
	}
	if perIP < 1 {
		perIP = 1
	}
	if log == nil {
		log = slog.Default()
	}
	return &Hub{
		clients:    make(map[*sseClient]struct{}),
		perIPCount: make(map[string]int),
		globalMax:  globalMax,
		perIP:      perIP,
		log:        log,
	}
}

// Register admits a client and returns its channel plus an unregister func.
// The channel is buffered (depth 1) and Broadcast NEVER blocks on it: a slow
// client drops an update rather than stalling the publisher. Dropping is
// correct here — stats_update replaces the whole stats slice wholesale, so the
// next event carries everything the dropped one did.
//
// The GLOBAL cap is checked BEFORE the per-IP cap, and the order is part of
// the contract: at global capacity the refusal is ours, not the caller's, and
// the handler must answer 503 rather than blaming a client that is inside its
// own share with 429. Pinned by TestGlobalCapTakesPrecedenceOverThePerIPCap.
//
// On refusal the returned channel is nil and the returned func is nil: there
// is nothing to clean up, and a non-nil no-op func would invite a handler to
// treat a refusal as an admission.
func (h *Hub) Register(ipKey string) (<-chan statsDTO, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.clients) >= h.globalMax {
		h.log.Warn("sse stream refused: global capacity reached",
			"clients", len(h.clients), "global_max", h.globalMax)
		return nil, nil, ErrHubFull
	}
	if h.perIPCount[ipKey] >= h.perIP {
		h.log.Warn("sse stream refused: per-client capacity reached",
			"streams", h.perIPCount[ipKey], "per_ip_max", h.perIP)
		return nil, nil, ErrPerIPFull
	}

	c := &sseClient{ch: make(chan statsDTO, sseClientBuffer), ipKey: ipKey}
	h.clients[c] = struct{}{}
	h.perIPCount[ipKey]++

	// sync.OnceFunc, not a bool guarded by the hub's own lock, because the
	// idempotence must hold across goroutines too: Task 18's handler defers
	// this AND calls it on its error paths, and a double call that decremented
	// twice would drive the per-IP counter negative and hand one key unlimited
	// streams. Pinned by TestUnregisterIsIdempotent.
	unregister := sync.OnceFunc(func() { h.remove(c) })
	return c.ch, unregister, nil
}

// remove drops one client and releases both its slots. The client channel is
// NOT closed — see the shutdown contract on Hub for why closing it would turn
// a benign lost update into a panic on a publisher's goroutine.
func (h *Hub) remove(c *sseClient) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.clients[c]; !ok {
		return
	}
	delete(h.clients, c)

	n := h.perIPCount[c.ipKey] - 1
	if n <= 0 {
		// Delete rather than store 0: a retained zero entry is an unbounded
		// key vector. See the type comment.
		delete(h.perIPCount, c.ipKey)
		return
	}
	h.perIPCount[c.ipKey] = n
}

// Broadcast sends s to every registered client, dropping for any client whose
// buffer is full. It returns the number of clients it delivered to, which is
// what makes the fan-out testable without inspecting internals.
//
// The client set is copied under the lock and every send happens AFTER the
// lock is released. Sending under the lock — even non-blockingly — would
// serialize every Register, Clients and unregister call behind the fan-out;
// sending under the lock BLOCKINGLY would let one client that stopped reading
// stall every other client's registration indefinitely.
func (h *Hub) Broadcast(s statsDTO) int {
	h.mu.Lock()
	targets := make([]*sseClient, 0, len(h.clients))
	for c := range h.clients {
		targets = append(targets, c)
	}
	h.mu.Unlock()

	delivered := 0
	for _, c := range targets {
		select {
		case c.ch <- s:
			delivered++
		default:
			// Dropped: this client has an unread update already queued, and
			// the queued one is not older information than this one in any way
			// that matters — the payload is a whole-state replacement.
		}
	}
	if dropped := len(targets) - delivered; dropped > 0 {
		h.log.Debug("sse stats_update dropped for slow clients",
			"dropped", dropped, "delivered", delivered)
	}
	return delivered
}

// Clients is the current count, reported as sseClients in the heartbeat.
func (h *Hub) Clients() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// The SSE wire literals. Every one is asserted verbatim by the tests because
// every one is load-bearing: the client registers a listener for the NAMED
// event stats_update (spec §13.7), and a default/unnamed message event is
// ignored entirely — the Live dot would stay green (onopen fires) while the
// stats never update.
const (
	sseEventName      = "stats_update"
	sseHeartbeat      = ":ping\n\n"
	sseContentType    = "text/event-stream"
	sseCacheControl   = "no-cache"
	sseConnection     = "keep-alive"
	sseAccelBuffer    = "X-Accel-Buffering"
	sseAccelBufferOff = "no"
)

// The two header names the stream sets that no other response path sets.
// headerContentType lives in respond.go and is reused here.
const (
	headerCacheControl = "Cache-Control"
	headerConnection   = "Connection"
)

// sseFramePrefix is everything before the JSON payload of a stats_update
// frame. It is composed FROM sseEventName rather than pasted, so changing the
// event name changes the wire — which is what makes the name's mutation
// observable in exactly one place.
//
// Note the single space after each colon and the single newline between the
// two field lines: `event:stats_update` (no space) is a different field value
// to EventSource, and a second newline here would terminate the frame before
// its data line.
const sseFramePrefix = "event: " + sseEventName + "\ndata: "

// sseFrameSuffix terminates the frame. The BLANK line is what dispatches the
// event; without it the client buffers the fields forever and never fires the
// listener.
const sseFrameSuffix = "\n\n"

// The two refusal bodies. They are DISTINCT from msgTooManyRequests (the
// limiter's 429 message) on purpose: the per-IP concurrency cap and the
// per-minute new-stream rate limit both answer 429 on the same path, and a
// shared message would make a test — and an operator reading a log — unable
// to tell which of the two refused.
const (
	msgTooManyStreams        = "Too many concurrent streams, please try again later"
	msgStreamCapacityReached = "Stream capacity reached, please try again later"
)

// sseHeartbeatInterval is the comment-frame period. Cloudflare tunnels drop
// idle streams, so this is not decoration. It is a package var rather than a
// const so the tests can shorten it and restore it with t.Cleanup — a real
// 30-second wait in a unit test is how a suite becomes something nobody runs.
var sseHeartbeatInterval = 30 * time.Second

// handleEvents serves GET /api/events (spec §13.7).
//
// The heartbeat is an SSE COMMENT (":ping\n\n") and never a stats_update: the
// client's handler does JSON.parse inside a bare `catch {}`, so a malformed
// stats_update is swallowed silently and a heartbeat shaped like one would be
// invisible breakage.
//
// It sets NO write deadline — it is registered behind markSSEExempt — and
// relies on r.Context().Done() for cleanup. It calls Flush after EVERY write.
//
// A failing Stats read does NOT refuse the connection: the stream's job is to
// deliver FUTURE updates, and refusing to open it because one stats read
// failed converts a transient DB blip into a dead Live dot until the user
// reloads the page. The first push is omitted and logged at WARN instead.
// Pinned by TestEventsStatsFailureStillOpensTheStream.
// log is the INJECTED logger, not slog's package default. The two register
// failures below are the only error-level records this handler can emit, and
// emitting them through slog.ErrorContext sent them somewhere the caller's
// Deps.Logger does not reach: a cmd/ that configures a logger without also
// calling slog.SetDefault would split this handler's records away from every
// other record the router produces, so the one place an operator looks for them
// would be the one place they are not.
func handleEvents(h *Hub, sts store.StationStore, res ratelimit.Resolver, log *slog.Logger) http.HandlerFunc {
	log = orDefaultLogger(log)
	return func(w http.ResponseWriter, r *http.Request) {
		ch, unregister, regErr := h.Register(res.Key(r))
		if regErr != nil {
			// The hub returns a nil channel AND a nil unregister func on
			// refusal, so there is deliberately nothing to defer here. Pinned
			// by TestRegisterRefusalReturnsANilChannelAndANilUnregister.
			switch {
			case errors.Is(regErr, ErrHubFull):
				writeError(w, r, http.StatusServiceUnavailable, msgStreamCapacityReached)
			case errors.Is(regErr, ErrPerIPFull):
				writeError(w, r, http.StatusTooManyRequests, msgTooManyStreams)
			default:
				log.ErrorContext(r.Context(), "sse register failed", "error", regErr)
				writeError(w, r, http.StatusInternalServerError, msgInternal)
			}
			return
		}
		defer unregister()

		header := w.Header()
		header.Set(headerContentType, sseContentType)
		header.Set(headerCacheControl, sseCacheControl)
		header.Set(headerConnection, sseConnection)
		header.Set(sseAccelBuffer, sseAccelBufferOff)
		w.WriteHeader(http.StatusOK)

		// Flush the header block immediately: EventSource fires onopen off the
		// response head, and the frontend's Live dot goes green there.
		rc := http.NewResponseController(w)
		if flushErr := rc.Flush(); flushErr != nil {
			return
		}

		// The FIRST push, on connect, so the tiles populate without waiting a
		// broadcast interval (spec §13.7).
		if first, statsErr := sts.Stats(r.Context()); statsErr != nil {
			log.WarnContext(r.Context(), "sse first stats push omitted",
				"request_id", requestIDFrom(r.Context()), "error", statsErr)
		} else if frameErr := writeStatsFrame(w, rc, toStats(first)); frameErr != nil {
			return
		}

		ticker := time.NewTicker(sseHeartbeatInterval)
		defer ticker.Stop()

		ctx := r.Context()
		for {
			select {
			case s := <-ch:
				// NEVER `range ch`: the hub never closes a client channel (see
				// its shutdown contract), so a range would block forever after
				// the client is gone. ctx.Done below is the only termination
				// signal.
				if frameErr := writeStatsFrame(w, rc, s); frameErr != nil {
					return
				}
			case <-ticker.C:
				if pingErr := writeHeartbeat(w, rc); pingErr != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}
}

// writeStatsFrame writes one complete stats_update frame and flushes it.
//
// json.Marshal, NOT json.Encoder.Encode: Encode appends a newline of its own,
// which would end the data line early and hand the client an event whose
// payload is an empty string — parsed inside a bare catch, so silently.
func writeStatsFrame(w io.Writer, rc *http.ResponseController, s statsDTO) error {
	payload, marshalErr := json.Marshal(s)
	if marshalErr != nil {
		return marshalErr
	}

	frame := make([]byte, 0, len(sseFramePrefix)+len(payload)+len(sseFrameSuffix))
	frame = append(frame, sseFramePrefix...)
	frame = append(frame, payload...)
	frame = append(frame, sseFrameSuffix...)

	if _, writeErr := w.Write(frame); writeErr != nil {
		return writeErr
	}
	// A flush failure ends the stream: a client that cannot be flushed to is
	// gone, and with WriteTimeout 0 on the API server nothing else would ever
	// notice.
	return rc.Flush()
}

// writeHeartbeat writes the comment frame and flushes it.
func writeHeartbeat(w io.Writer, rc *http.ResponseController) error {
	if _, writeErr := io.WriteString(w, sseHeartbeat); writeErr != nil {
		return writeErr
	}
	return rc.Flush()
}
