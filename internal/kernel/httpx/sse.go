package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// DefaultHeartbeat is how often an idle connection receives a comment frame, per S06: often
// enough that a dead TCP path is noticed, rare enough to cost nothing.
const DefaultHeartbeat = 20 * time.Second

// DefaultRingSize is how many frames the resume buffer holds when Options.RingSize is zero.
// At typical event rates this is minutes of history — plenty for the reconnect-with-backoff
// window the frontend uses (architecture §13).
const DefaultRingSize = 256

// DefaultConnBuffer is each connection's queue depth when Options.ConnBuffer is zero. A client
// this far behind is not reading; the hub closes it rather than ever holding the bus up.
const DefaultConnBuffer = 64

// subscriberName is the hub's one bus subscription.
const subscriberName = "sse-hub"

// Hub is the SSE fan-out (contracts §5.1): one bus subscription in, one frame per mapped event
// out to every connection subscribed to that frame's topic.
//
// Frames follow §5.1 exactly: `id:` is the event's ULID, `event:` is the event type
// ("run.activity", "ticket.updated"), `data:` is {"topic":"…","payload":…}. Each event maps to
// exactly one topic — run events to "run:<id>", notification events to "inbox", everything else
// with a project to "project:<key>" — so a frame id is unique and resume needs no per-topic
// bookkeeping. Events that map to no topic (no run subject, no project) are not streamed.
//
// Resume is a single global ring buffer of the last RingSize frames, shared by every topic.
// A reconnect with Last-Event-ID replays, from the ring, the frames after that id that match
// the connection's topics — computed and the connection registered under one lock, so within
// the buffer window there is no gap and no duplicate. An id that has already fallen out of the
// ring (or was never in it) resumes live-only; the frontend's reducer refetches on that.
//
// Backpressure: the bus-facing handler never blocks — a connection whose buffer is full is
// closed (the browser's EventSource reconnects and resumes). A publish is therefore O(conns)
// channel sends and returns immediately, whatever any client is doing.
type Hub struct {
	logger    *slog.Logger
	st        *store.Store
	heartbeat time.Duration
	connBuf   int

	mu       sync.Mutex
	ring     []frame // ordered, len ≤ ringSize
	ringSize int
	conns    map[*conn]struct{}
	closed   bool

	keyMu   sync.Mutex
	keyByID map[string]string // project id → key, for topic naming (§13: "project:PAY")
}

// HubOptions configures NewHub. The zero value is usable in tests.
type HubOptions struct {
	// Logger receives stuck-client and mapping lines. Nil means slog.Default().
	Logger *slog.Logger
	// Store resolves project IDs to keys for topic naming. Nil (tests) falls back to the raw
	// project id in the topic.
	Store *store.Store
	// Heartbeat overrides DefaultHeartbeat; tests set it to milliseconds.
	Heartbeat time.Duration
	// RingSize overrides DefaultRingSize.
	RingSize int
	// ConnBuffer overrides DefaultConnBuffer.
	ConnBuffer int
}

// NewHub builds a hub. Call Attach to feed it from the bus, and register its ServeHTTP (behind
// RequireAuth) on GET /api/v1/stream.
func NewHub(opts HubOptions) *Hub {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	hb := opts.Heartbeat
	if hb <= 0 {
		hb = DefaultHeartbeat
	}
	ringSize := opts.RingSize
	if ringSize <= 0 {
		ringSize = DefaultRingSize
	}
	connBuf := opts.ConnBuffer
	if connBuf <= 0 {
		connBuf = DefaultConnBuffer
	}
	return &Hub{
		logger:    logger,
		st:        opts.Store,
		heartbeat: hb,
		connBuf:   connBuf,
		ringSize:  ringSize,
		conns:     map[*conn]struct{}{},
		keyByID:   map[string]string{},
	}
}

// Attach subscribes the hub to every event on the bus. Call it before bus.Start so boot
// recovery flows through to connected tabs too. Handlers never write to SSE directly — this
// subscription is the only way a frame is born (§5.1).
func (h *Hub) Attach(b *bus.Bus) error {
	return b.SubscribeTopic(subscriberName, "*", h.handleEvent)
}

// Close ends every open connection and refuses new ones; call it before http.Server.Shutdown,
// which would otherwise wait its full grace period on streams that never end on their own.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	conns := make([]*conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.kill()
	}
}

// Connections is how many streams are open right now (module status, tests).
func (h *Hub) Connections() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

// frame is one SSE message, fully rendered except for the wire framing.
type frame struct {
	id    string // the event's ULID; the client echoes it in Last-Event-ID
	event string // SSE event type: "run.activity", "ticket.updated", …
	topic string // the one topic this frame belongs to
	data  []byte // {"topic":"…","payload":…}, single line
}

// handleEvent is the bus subscription: map, buffer, fan out. It must never block — see the
// type comment — so channel sends are non-blocking and a full buffer kills that connection.
func (h *Hub) handleEvent(ctx context.Context, e domain.Event) error {
	f, ok := h.frameFor(ctx, e)
	if !ok {
		return nil
	}

	h.mu.Lock()
	h.ring = append(h.ring, f)
	if len(h.ring) > h.ringSize {
		h.ring = h.ring[len(h.ring)-h.ringSize:]
	}
	var stuck []*conn
	for c := range h.conns {
		if !c.topics[f.topic] {
			continue
		}
		select {
		case c.ch <- f:
		default:
			stuck = append(stuck, c)
		}
	}
	h.mu.Unlock()

	for _, c := range stuck {
		h.logger.Warn("sse: closing stuck client — its buffer is full and the bus never waits",
			slog.String("remote", c.remote),
			slog.String("topic", f.topic),
			slog.String("event", f.id))
		c.kill()
	}
	return nil
}

// frameFor maps one bus event onto its SSE frame, or reports that this event is not streamed.
func (h *Hub) frameFor(ctx context.Context, e domain.Event) (frame, bool) {
	topic := h.topicFor(ctx, e)
	if topic == "" {
		return frame{}, false
	}
	eventType := e.Kind
	if e.ActivityType != "" {
		eventType += "." + e.ActivityType
	}

	// The payload goes out compacted: SSE frames a message per line, so an embedded newline
	// from a pretty-printed stored payload would split the frame.
	payload := json.RawMessage("null")
	if len(e.Payload) > 0 {
		var buf bytes.Buffer
		if err := json.Compact(&buf, e.Payload); err == nil {
			payload = json.RawMessage(buf.Bytes())
		}
	}
	data, err := json.Marshal(struct {
		Topic   string          `json:"topic"`
		Payload json.RawMessage `json:"payload"`
	}{Topic: topic, Payload: payload})
	if err != nil {
		h.logger.Warn("sse: could not marshal frame; event not streamed",
			slog.String("event", e.ID), slog.String("error", err.Error()))
		return frame{}, false
	}
	return frame{id: e.ID, event: eventType, topic: topic, data: data}, true
}

// topicFor is the topic naming from architecture §13: "run:01H…", "project:PAY", "inbox".
func (h *Hub) topicFor(ctx context.Context, e domain.Event) string {
	switch {
	case e.SubjectKind == "run" && e.SubjectID != nil && *e.SubjectID != "":
		return "run:" + *e.SubjectID
	case e.Kind == "notification":
		return "inbox"
	case e.ProjectID != nil && *e.ProjectID != "":
		return "project:" + h.projectKey(ctx, *e.ProjectID)
	}
	return ""
}

// projectKey resolves a project id to its key ("PAY"), caching forever — keys are immutable
// (data model §3). Without a store, or for an unknown id, the id itself is the topic suffix.
func (h *Hub) projectKey(ctx context.Context, id string) string {
	h.keyMu.Lock()
	key, ok := h.keyByID[id]
	h.keyMu.Unlock()
	if ok {
		return key
	}
	key = id
	if h.st != nil {
		if p, err := h.st.Projects().ByID(ctx, id); err == nil {
			key = p.Key
		} else {
			return key // do not cache a failed lookup
		}
	}
	h.keyMu.Lock()
	h.keyByID[id] = key
	h.keyMu.Unlock()
	return key
}

// conn is one open stream.
type conn struct {
	topics map[string]bool
	ch     chan frame
	done   chan struct{}
	once   sync.Once
	remote string
}

// kill ends the connection from outside its own goroutine (stuck client, hub Close).
func (c *conn) kill() { c.once.Do(func() { close(c.done) }) }

// ServeHTTP is GET /api/v1/stream?topics=a,b,c. The kernel registers it behind RequireAuth.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	topics := parseTopics(r.URL.Query().Get("topics"))
	if len(topics) == 0 {
		WriteProblem(w, http.StatusBadRequest, TypeInvalidRequest,
			"No topics", "Pass ?topics=a,b,c — a stream with no topics would never say anything.")
		return
	}

	c := &conn{
		topics: topics,
		ch:     make(chan frame, h.connBuf),
		done:   make(chan struct{}),
		remote: r.RemoteAddr,
	}
	backlog, ok := h.register(c, r.Header.Get("Last-Event-ID"))
	if !ok {
		WriteProblem(w, http.StatusServiceUnavailable, "shutting_down",
			"Server is shutting down", "Reconnect in a moment.")
		return
	}
	defer h.unregister(c)
	// kill on the way out too, so the deadline watcher below always exits.
	defer c.kill()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)

	// A killed connection may be blocked inside a TCP write to a client that stopped reading —
	// closing c.done alone cannot unblock that. An expired write deadline can: the pending
	// write fails, the loop returns, the connection tears down.
	go func() {
		<-c.done
		_ = rc.SetWriteDeadline(time.Now())
	}()

	// An immediate comment both flushes the headers to the client and proves the path writes.
	if err := writeComment(w, rc, "connected"); err != nil {
		return
	}
	for _, f := range backlog {
		if err := writeFrame(w, rc, f); err != nil {
			return
		}
	}

	ticker := time.NewTicker(h.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case f := <-c.ch:
			if err := writeFrame(w, rc, f); err != nil {
				return
			}
		case <-ticker.C:
			if err := writeComment(w, rc, "heartbeat"); err != nil {
				return
			}
		case <-c.done:
			return
		case <-r.Context().Done():
			return
		}
	}
}

// register adds the connection and, in the same critical section, snapshots its replay: every
// buffered frame after lastID that matches its topics. Atomicity is the no-gap-no-duplicate
// guarantee — a frame published after the snapshot lands on the channel, never in both.
func (h *Hub) register(c *conn, lastID string) ([]frame, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, false
	}
	var backlog []frame
	if lastID != "" {
		start := -1
		for i, f := range h.ring {
			if f.id == lastID {
				start = i + 1
				break
			}
		}
		if start >= 0 {
			for _, f := range h.ring[start:] {
				if c.topics[f.topic] {
					backlog = append(backlog, f)
				}
			}
		}
	}
	h.conns[c] = struct{}{}
	return backlog, true
}

func (h *Hub) unregister(c *conn) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
}

func parseTopics(raw string) map[string]bool {
	topics := map[string]bool{}
	for _, t := range strings.Split(raw, ",") {
		if t = strings.TrimSpace(t); t != "" {
			topics[t] = true
		}
	}
	return topics
}

func writeFrame(w http.ResponseWriter, rc *http.ResponseController, f frame) error {
	if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", f.id, f.event, f.data); err != nil {
		return err
	}
	return rc.Flush()
}

func writeComment(w http.ResponseWriter, rc *http.ResponseController, text string) error {
	if _, err := fmt.Fprintf(w, ": %s\n\n", text); err != nil {
		return err
	}
	return rc.Flush()
}
