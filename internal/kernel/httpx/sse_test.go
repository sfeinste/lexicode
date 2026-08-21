package httpx_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// sseEnv is a hub fed by a real bus over a real temp-file store — the exact production path
// (persist → dispatch → hub → frame), minus auth, which the kernel wires around the hub.
type sseEnv struct {
	t   *testing.T
	st  *store.Store
	bus *bus.Bus
	hub *httpx.Hub
	srv *httptest.Server
}

func newSSEEnv(t *testing.T, mod func(*httpx.HubOptions)) *sseEnv {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "sse.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	b := bus.New(bus.Options{Store: st, Logger: logger})
	opts := httpx.HubOptions{Logger: logger, Store: st, Heartbeat: time.Hour}
	if mod != nil {
		mod(&opts)
	}
	hub := httpx.NewHub(opts)
	if err := hub.Attach(b); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hub.Close()
		_ = b.Stop(ctx)
	})

	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)
	return &sseEnv{t: t, st: st, bus: b, hub: hub, srv: srv}
}

// publishRun publishes one run-scoped event ("run.activity" on topic "run:<runID>").
func (e *sseEnv) publishRun(runID, payload string) domain.Event {
	e.t.Helper()
	// Publish defaults the ID on its own copy; set it here so the test can assert frame ids.
	ev := domain.Event{
		ID:   domain.NewID(),
		Kind: "run", ActivityType: "activity",
		SubjectKind: "run", SubjectID: &runID,
		Payload:   []byte(payload),
		DedupeKey: "test:" + domain.NewID(),
	}
	if err := e.bus.Publish(context.Background(), ev); err != nil {
		e.t.Fatalf("publish: %v", err)
	}
	return ev
}

// sseFrame is one parsed wire frame.
type sseFrame struct {
	id, event, data string
	comment         string // set instead when the frame was a comment
}

// sseClient reads one stream.
type sseClient struct {
	t      *testing.T
	body   io.ReadCloser
	frames chan sseFrame
}

// connect opens the stream and consumes frames on a goroutine; it returns after the server's
// ": connected" comment, so the connection is registered before the test publishes.
func connect(t *testing.T, base, topics, lastEventID string) *sseClient {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		base+"/?topics="+topics, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("stream status = %d, body %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	c := &sseClient{t: t, body: resp.Body, frames: make(chan sseFrame, 64)}
	t.Cleanup(c.close)
	go c.read()

	if f := c.next(2 * time.Second); f == nil || f.comment != "connected" {
		t.Fatalf("first frame = %+v, want the connected comment", f)
	}
	return c
}

func (c *sseClient) close() { _ = c.body.Close() }

func (c *sseClient) read() {
	defer close(c.frames)
	scanner := bufio.NewScanner(c.body)
	var f sseFrame
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			c.frames <- f
			f = sseFrame{}
		case strings.HasPrefix(line, ": "):
			f.comment = strings.TrimPrefix(line, ": ")
		case strings.HasPrefix(line, "id: "):
			f.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			f.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			f.data = strings.TrimPrefix(line, "data: ")
		}
	}
}

// next returns the next frame, or nil after the timeout.
func (c *sseClient) next(timeout time.Duration) *sseFrame {
	select {
	case f, ok := <-c.frames:
		if !ok {
			return nil
		}
		return &f
	case <-time.After(timeout):
		return nil
	}
}

// nextEvent skips comments and returns the next event frame, or nil after the timeout.
func (c *sseClient) nextEvent(timeout time.Duration) *sseFrame {
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil
		}
		f := c.next(remaining)
		if f == nil {
			return nil
		}
		if f.comment == "" {
			return f
		}
	}
}

// TestTwoConnectionsEachReceiveOnlyTheirTopics is S06 acceptance: two tabs subscribed to
// different topics each receive only their own frames.
func TestTwoConnectionsEachReceiveOnlyTheirTopics(t *testing.T) {
	e := newSSEEnv(t, nil)
	runA, runB := domain.NewID(), domain.NewID()

	connA := connect(t, e.srv.URL, "run:"+runA, "")
	connB := connect(t, e.srv.URL, "run:"+runB, "")

	evA := e.publishRun(runA, `{"n":1}`)
	evB := e.publishRun(runB, `{"n":2}`)

	fa := connA.nextEvent(2 * time.Second)
	if fa == nil {
		t.Fatal("conn A received nothing")
	}
	if fa.id != evA.ID || fa.event != "run.activity" {
		t.Errorf("conn A frame = %+v, want id %s event run.activity", fa, evA.ID)
	}
	wantData := fmt.Sprintf(`{"topic":"run:%s","payload":{"n":1}}`, runA)
	if fa.data != wantData {
		t.Errorf("conn A data = %s, want %s", fa.data, wantData)
	}

	fb := connB.nextEvent(2 * time.Second)
	if fb == nil {
		t.Fatal("conn B received nothing")
	}
	if fb.id != evB.ID {
		t.Errorf("conn B frame id = %s, want %s (its own topic's event)", fb.id, evB.ID)
	}

	// Neither connection sees the other's frame.
	if extra := connA.nextEvent(300 * time.Millisecond); extra != nil {
		t.Errorf("conn A also received %+v, want only its own topic", extra)
	}
	if extra := connB.nextEvent(300 * time.Millisecond); extra != nil {
		t.Errorf("conn B also received %+v, want only its own topic", extra)
	}
}

// TestResumeWithLastEventIDHasNoGapAndNoDuplicate is S06 acceptance: a reconnect replays
// exactly the missed frames from the ring, then goes live seamlessly.
func TestResumeWithLastEventIDHasNoGapAndNoDuplicate(t *testing.T) {
	e := newSSEEnv(t, nil)
	run := domain.NewID()

	c1 := connect(t, e.srv.URL, "run:"+run, "")
	e1 := e.publishRun(run, `{"n":1}`)
	e2 := e.publishRun(run, `{"n":2}`)
	for _, want := range []string{e1.ID, e2.ID} {
		f := c1.nextEvent(2 * time.Second)
		if f == nil || f.id != want {
			t.Fatalf("frame = %+v, want id %s", f, want)
		}
	}
	c1.close() // the tab drops

	// Missed while disconnected.
	e3 := e.publishRun(run, `{"n":3}`)
	e4 := e.publishRun(run, `{"n":4}`)

	// Reconnect where we left off.
	c2 := connect(t, e.srv.URL, "run:"+run, e2.ID)
	e5 := e.publishRun(run, `{"n":5}`)

	var got []string
	for range 3 {
		f := c2.nextEvent(2 * time.Second)
		if f == nil {
			break
		}
		got = append(got, f.id)
	}
	want := []string{e3.ID, e4.ID, e5.ID}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("resumed frames = %v, want exactly %v — no gap, no duplicate", got, want)
	}
	if extra := c2.nextEvent(300 * time.Millisecond); extra != nil {
		t.Errorf("also received %+v, want nothing more", extra)
	}
}

// TestStuckClientIsClosedAndTheBusNeverBlocks is S06 acceptance: a client that never reads is
// closed once its buffer fills; the publish path stays fast throughout. The client here is a
// raw TCP connection that sends the request and then reads nothing, ever — the worst case,
// because the server's writer goroutine ends up blocked inside a TCP write.
func TestStuckClientIsClosedAndTheBusNeverBlocks(t *testing.T) {
	e := newSSEEnv(t, func(o *httpx.HubOptions) { o.ConnBuffer = 1 })
	run := domain.NewID()

	u, err := url.Parse(e.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	_, err = fmt.Fprintf(raw, "GET /?topics=run:%s HTTP/1.1\r\nHost: %s\r\n\r\n", run, u.Host)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for the stream to register; the client then never reads a byte.
	deadline := time.Now().Add(5 * time.Second)
	for e.hub.Connections() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("connections = %d, want the stuck stream registered", e.hub.Connections())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// ~256KB frames fill the kernel's socket buffers within a few writes, wedging the writer
	// goroutine; with ConnBuffer 1 the next publish finds the queue full and closes the client.
	big := strings.Repeat("x", 256<<10)
	start := time.Now()
	for i := 0; i < 20; i++ {
		pubStart := time.Now()
		e.publishRun(run, fmt.Sprintf(`{"n":%d,"pad":%q}`, i, big))
		if d := time.Since(pubStart); d > time.Second {
			t.Fatalf("publish %d took %v — the bus blocked on a stuck client", i, d)
		}
	}
	t.Logf("20 publishes of 256KB frames took %v total", time.Since(start))

	// The hub noticed and closed the stream.
	deadline = time.Now().Add(5 * time.Second)
	for e.hub.Connections() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("connections = %d after 5s, want 0 — stuck client was never closed",
				e.hub.Connections())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHeartbeatComment(t *testing.T) {
	e := newSSEEnv(t, func(o *httpx.HubOptions) { o.Heartbeat = 50 * time.Millisecond })
	c := connect(t, e.srv.URL, "run:whatever", "")

	f := c.next(2 * time.Second)
	if f == nil || f.comment != "heartbeat" {
		t.Fatalf("frame = %+v, want a heartbeat comment", f)
	}
}

func TestStreamWithoutTopicsIsRefused(t *testing.T) {
	e := newSSEEnv(t, nil)
	resp, err := http.Get(e.srv.URL + "/") //nolint:noctx // test client
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), `"type":"invalid_request"`) {
		t.Errorf("no-topics stream = %d %s, want a 400 invalid_request problem", resp.StatusCode, body)
	}
}

// TestProjectTopicUsesTheProjectKey pins the §13 topic naming: "project:PAY", not the ULID.
func TestProjectTopicUsesTheProjectKey(t *testing.T) {
	e := newSSEEnv(t, nil)
	ctx := context.Background()

	owner := domain.User{
		ID: domain.NewID(), Email: "o@example.com", DisplayName: "O",
		PasswordHash: "!", Role: domain.RoleOwner, AvatarColor: "#111111",
		CreatedAt: domain.Now(),
	}
	if err := e.st.Users().Create(ctx, &owner); err != nil {
		t.Fatal(err)
	}
	p := domain.Project{
		ID: domain.NewID(), Key: "PAY", Name: "Payments", Color: "#222222",
		OwnerID: owner.ID, CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
	}
	if err := e.st.Projects().Create(ctx, &p); err != nil {
		t.Fatal(err)
	}

	c := connect(t, e.srv.URL, "project:PAY", "")
	ev := domain.Event{
		Kind: "ticket", ActivityType: "updated",
		ProjectID:   &p.ID,
		SubjectKind: "ticket",
		Payload:     []byte(`{"ticket":"T-1"}`),
		DedupeKey:   "test:" + domain.NewID(),
	}
	if err := e.bus.Publish(ctx, ev); err != nil {
		t.Fatal(err)
	}

	f := c.nextEvent(2 * time.Second)
	if f == nil {
		t.Fatal("no frame on project:PAY")
	}
	if f.event != "ticket.updated" {
		t.Errorf("event = %q, want ticket.updated", f.event)
	}
	if want := `{"topic":"project:PAY","payload":{"ticket":"T-1"}}`; f.data != want {
		t.Errorf("data = %s, want %s", f.data, want)
	}
}
