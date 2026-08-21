package kernel_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// s06Env is the S06 wiring under test: a kernel built with store, auth, audit and the SSE hub,
// served exactly as cmd/lexicode serves it (minus the SPA and the /api/ prefix wrapping, which
// live in cmd and have their own tests).
type s06Env struct {
	t     *testing.T
	st    *store.Store
	audit *audit.Writer
	srv   *httptest.Server
}

func newS06Env(t *testing.T) *s06Env {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "s06.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	mux := httpx.NewMux(httpx.Options{Logger: logger})
	authSvc := auth.New(auth.Options{Store: st, Logger: logger})
	authSvc.Routes(mux)
	auditW := audit.New(audit.Options{Store: st, Logger: logger})
	hub := httpx.NewHub(httpx.HubOptions{Logger: logger, Store: st})
	t.Cleanup(hub.Close)

	k := kernel.New(kernel.Options{
		Logger: logger, Mux: mux, Store: st, Bus: nil, Auth: authSvc, Audit: auditW, SSE: hub,
	})
	if k.Audit() != auditW || k.SSE() != hub || k.Mux() != mux {
		t.Fatal("kernel accessors do not return the wired subsystems")
	}

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &s06Env{t: t, st: st, audit: auditW, srv: srv}
}

// client returns an http client with its own cookie jar.
func (e *s06Env) client() *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func (e *s06Env) postJSON(c *http.Client, path, body string) *http.Response {
	e.t.Helper()
	resp, err := c.Post(e.srv.URL+path, "application/json", strings.NewReader(body)) //nolint:noctx // test client
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

// TestAuditEndpointIsOwnerOnlyAndCarriesTheActor wires S06 end to end: the owner sees the audit
// rows their own actions produced; a member is refused; an anonymous request is refused.
func TestAuditEndpointIsOwnerOnlyAndCarriesTheActor(t *testing.T) {
	e := newS06Env(t)

	// Owner via first-run setup.
	owner := e.client()
	resp := e.postJSON(owner, "/api/v1/auth/setup",
		`{"email":"ada@example.com","display_name":"Ada","password":"correct horse"}`)
	var ownerBody struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ownerBody); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup = %d, want 201", resp.StatusCode)
	}

	// A mutation on the owner's behalf: the actor comes from the request context in real
	// handlers; here the test stands in for the service layer.
	ctx := auth.WithActor(context.Background(), auth.Actor{Kind: domain.ActorHuman, ID: ownerBody.ID})
	if err := e.audit.Write(ctx, "project.update",
		audit.Target{Kind: "project", ID: "p1"}, nil, map[string]string{"name": "Payments"}); err != nil {
		t.Fatal(err)
	}

	// Owner reads the log and finds their entry, attributed to them.
	getAudit := func(c *http.Client) (int, string) {
		resp, err := c.Get(e.srv.URL + "/api/v1/audit") //nolint:noctx // test client
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	code, body := getAudit(owner)
	if code != http.StatusOK {
		t.Fatalf("owner GET /api/v1/audit = %d %s, want 200", code, body)
	}
	if !strings.Contains(body, `"action":"project.update"`) ||
		!strings.Contains(body, `"actor_id":"`+ownerBody.ID+`"`) {
		t.Errorf("audit body = %s, want the entry attributed to the owner", body)
	}

	// A member (via invite) is refused with a 403 problem.
	resp = e.postJSON(owner, "/api/v1/invites", `{}`)
	var inv struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&inv); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	member := e.client()
	token := strings.TrimPrefix(inv.Path, "/invite/") // the path is the SPA screen; the API takes the token
	resp = e.postJSON(member, "/api/v1/invites/"+token+"/redeem",
		`{"email":"mo@example.com","display_name":"Mo","password":"correct horse"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("redeem = %d, want 201", resp.StatusCode)
	}
	_ = resp.Body.Close()
	code, body = getAudit(member)
	if code != http.StatusForbidden || !strings.Contains(body, `"type":"forbidden"`) {
		t.Errorf("member GET /api/v1/audit = %d %s, want a 403 forbidden problem", code, body)
	}

	// Anonymous is a 401.
	if code, _ := getAudit(e.client()); code != http.StatusUnauthorized {
		t.Errorf("anonymous GET /api/v1/audit = %d, want 401", code)
	}
}

// TestStreamRouteRequiresAuth: the SSE endpoint is registered and sits behind RequireAuth.
func TestStreamRouteRequiresAuth(t *testing.T) {
	e := newS06Env(t)

	// Anonymous: 401 problem, not a stream.
	resp, err := e.client().Get(e.srv.URL + "/api/v1/stream?topics=inbox") //nolint:noctx // test client
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous stream = %d, want 401", resp.StatusCode)
	}

	// Authenticated: a live event-stream.
	owner := e.client()
	setup := e.postJSON(owner, "/api/v1/auth/setup",
		`{"email":"ada@example.com","display_name":"Ada","password":"correct horse"}`)
	_ = setup.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		e.srv.URL+"/api/v1/stream?topics=inbox", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = owner.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("authed stream = %d %q, want 200 text/event-stream",
			resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	// The first bytes are the connected comment; read just that much and hang up.
	buf := make([]byte, len(": connected\n\n"))
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != ": connected\n\n" {
		t.Errorf("stream opened with %q, want the connected comment", buf)
	}
}
