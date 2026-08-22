package tickets_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/sched"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/service/board"
	"github.com/spruce/lexicode/internal/service/projects"
	"github.com/spruce/lexicode/internal/service/tickets"
)

// schedRecorder is the test double behind the scheduler seam: it records every request and
// behaves exactly like sched.Unscheduled — ErrNotImplemented, nothing started — so the tests
// can assert both "the intent was written" and "no run exists".
type schedRecorder struct {
	mu       sync.Mutex
	requests []sched.RunRequest
	cancels  []string
}

func (r *schedRecorder) RequestRun(_ context.Context, req sched.RunRequest) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
	return "", sched.ErrNotImplemented
}

func (r *schedRecorder) CancelTicketRuns(_ context.Context, ticketID, reason string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels = append(r.cancels, ticketID+"|"+reason)
	return 0, sched.ErrNotImplemented
}

func (r *schedRecorder) requestCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

// env wires store + auth + audit + the projects, board and tickets services' routes, exactly
// as cmd/lexicode serves them (with the scheduler seam swapped for the recorder).
type env struct {
	t   *testing.T
	st  *store.Store
	srv *httptest.Server
	rec *schedRecorder
}

func newEnv(t *testing.T) *env {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "s10.db"), Logger: logger})
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
	projects.New(projects.Options{Store: st, Audit: auditW, Logger: logger}).Routes(mux, authSvc)
	board.New(board.Options{Store: st, Audit: auditW, Logger: logger}).Routes(mux, authSvc)
	rec := &schedRecorder{}
	tickets.New(tickets.Options{Store: st, Audit: auditW, Sched: rec, Logger: logger}).
		Routes(mux, authSvc)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, st: st, srv: srv, rec: rec}
}

// owner signs up the first-run owner and returns an authenticated client plus the owner's ID.
func (e *env) owner() (*http.Client, string) {
	e.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	c := &http.Client{Jar: jar}
	status, body := e.doJSON(c, "POST", "/api/v1/auth/setup",
		`{"email":"ada@example.com","display_name":"Ada","password":"correct horse"}`)
	if status != http.StatusCreated {
		e.t.Fatalf("setup = %d, want 201", status)
	}
	id, _ := body["id"].(string)
	if id == "" {
		e.t.Fatal("setup returned no user id")
	}
	return c, id
}

func (e *env) doJSON(c *http.Client, method, path, body string) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var v map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			e.t.Fatalf("%s %s: not JSON: %v\n%s", method, path, err, raw)
		}
	}
	return resp.StatusCode, v
}

// createProject makes a project through the API and returns its board columns in order.
func (e *env) createProject(c *http.Client, key string) []map[string]any {
	e.t.Helper()
	status, _ := e.doJSON(c, "POST", "/api/v1/projects",
		fmt.Sprintf(`{"key":%q,"name":"Project %s"}`, key, key))
	if status != http.StatusCreated {
		e.t.Fatalf("create project = %d, want 201", status)
	}
	status, body := e.doJSON(c, "GET", "/api/v1/projects/"+key+"/columns", "")
	if status != http.StatusOK {
		e.t.Fatalf("list columns = %d, want 200", status)
	}
	raw, _ := body["columns"].([]any)
	out := make([]map[string]any, len(raw))
	for i, r := range raw {
		out[i] = r.(map[string]any)
	}
	return out
}

// columnByCategory finds the first column of a category — never by name (plan rule 3).
func columnByCategory(cols []map[string]any, category string) map[string]any {
	for _, c := range cols {
		if c["category"] == category {
			return c
		}
	}
	return nil
}

// createTicket makes a ticket through the API and returns its body.
func (e *env) createTicket(c *http.Client, projectKey, title string, extra string) map[string]any {
	e.t.Helper()
	body := fmt.Sprintf(`{"title":%q%s}`, title, extra)
	status, out := e.doJSON(c, "POST", "/api/v1/projects/"+projectKey+"/tickets", body)
	if status != http.StatusCreated {
		e.t.Fatalf("create ticket = %d, want 201: %v", status, out)
	}
	return out
}

// listTickets lists a project's tickets, optionally including archived ones.
func (e *env) listTickets(c *http.Client, projectKey string, archived bool) []map[string]any {
	e.t.Helper()
	path := "/api/v1/projects/" + projectKey + "/tickets"
	if archived {
		path += "?archived=1"
	}
	status, body := e.doJSON(c, "GET", path, "")
	if status != http.StatusOK {
		e.t.Fatalf("list tickets = %d, want 200: %v", status, body)
	}
	raw, _ := body["tickets"].([]any)
	out := make([]map[string]any, len(raw))
	for i, r := range raw {
		out[i] = r.(map[string]any)
	}
	return out
}

// stream fetches a ticket's unified stream.
func (e *env) stream(c *http.Client, ticketID string) []map[string]any {
	e.t.Helper()
	status, body := e.doJSON(c, "GET", "/api/v1/tickets/"+ticketID+"/stream", "")
	if status != http.StatusOK {
		e.t.Fatalf("stream = %d, want 200: %v", status, body)
	}
	raw, _ := body["entries"].([]any)
	out := make([]map[string]any, len(raw))
	for i, r := range raw {
		out[i] = r.(map[string]any)
	}
	return out
}

// addAgent inserts an agent row directly — the agents service is S15's; S10 only needs the
// row to exist so a delegate can be validated.
func (e *env) addAgent(projectID, name string) string {
	e.t.Helper()
	now := domain.Now()
	a := domain.Agent{
		ID: domain.NewID(), ProjectID: projectID, Name: name, Role: "dev", Color: "#336699",
		RuntimeID: "claude-code", Model: "default", Effort: "medium",
		Autonomy: domain.AutonomyAuto, ConcurrencyCap: 1,
		MaxWallClockSeconds: 3600, MaxSteps: 100, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Agents().Create(context.Background(), &a); err != nil {
		e.t.Fatal(err)
	}
	return a.ID
}

// auditActions returns the actions of every audit entry for one target ID, oldest first.
func (e *env) auditActions(targetID string) []string {
	e.t.Helper()
	entries, err := e.st.Audit().List(context.Background(), store.AuditFilter{})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for i := len(entries) - 1; i >= 0; i-- { // List is newest first
		if entries[i].TargetID == targetID {
			out = append(out, entries[i].Action)
		}
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
