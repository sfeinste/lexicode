package board_test

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
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/service/board"
	"github.com/spruce/lexicode/internal/service/projects"
)

// env wires store + auth + audit + the projects and board services' routes, exactly as
// cmd/lexicode serves them.
type env struct {
	t   *testing.T
	st  *store.Store
	srv *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "s09.db"), Logger: logger})
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

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, st: st, srv: srv}
}

// owner signs up the first-run owner and returns an authenticated client.
func (e *env) owner() *http.Client {
	e.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	c := &http.Client{Jar: jar}
	status, _ := e.doJSON(c, "POST", "/api/v1/auth/setup",
		`{"email":"ada@example.com","display_name":"Ada","password":"correct horse"}`)
	if status != http.StatusCreated {
		e.t.Fatalf("setup = %d, want 201", status)
	}
	return c
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

// createProject makes a project through the API (which is what creates the default columns)
// and returns its board columns in order.
func (e *env) createProject(c *http.Client, key string) []map[string]any {
	e.t.Helper()
	status, _ := e.doJSON(c, "POST", "/api/v1/projects",
		fmt.Sprintf(`{"key":%q,"name":"Project %s"}`, key, key))
	if status != http.StatusCreated {
		e.t.Fatalf("create project = %d, want 201", status)
	}
	return e.columns(c, key)
}

func (e *env) columns(c *http.Client, key string) []map[string]any {
	e.t.Helper()
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

// addTicket inserts a ticket directly through the store — ticket CRUD is S10; S09 only needs
// rows in the table to exercise the delete guardrail.
func (e *env) addTicket(projectID, columnID string, seq int64) string {
	e.t.Helper()
	now := domain.Now()
	tk := domain.Ticket{
		ID: domain.NewID(), ProjectID: projectID, Seq: seq,
		Key: fmt.Sprintf("T-%d", seq), Title: fmt.Sprintf("ticket %d", seq),
		ColumnID: columnID, Position: float64(seq), Priority: domain.Priority("none"),
		Origin: domain.OriginHuman, CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Tickets().Create(context.Background(), &tk); err != nil {
		e.t.Fatal(err)
	}
	return tk.ID
}

// ---------------------------------------------------------------- defaults -----

func TestNewProjectHasTheSixDefaultColumnsInOrder(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	cols := e.createProject(c, "PAY")

	want := []struct{ name, category string }{
		{"Backlog", "backlog"},
		{"Ready", "ready"},
		{"In Progress", "running"},
		{"In Review", "review"},
		{"Done", "done"},
		{"Canceled", "canceled"},
	}
	if len(cols) != len(want) {
		t.Fatalf("new project has %d columns, want %d: %v", len(cols), len(want), cols)
	}
	prev := float64(0)
	for i, w := range want {
		if cols[i]["name"] != w.name || cols[i]["category"] != w.category {
			t.Errorf("column %d = %v/%v, want %s/%s",
				i, cols[i]["name"], cols[i]["category"], w.name, w.category)
		}
		pos := cols[i]["position"].(float64)
		if pos <= prev {
			t.Errorf("column %d position %v is not increasing (prev %v)", i, pos, prev)
		}
		prev = pos
		if cols[i]["auto_start_delegate"] != false || cols[i]["wip_limit"] != nil {
			t.Errorf("column %d: auto_start/wip should default off, got %v/%v",
				i, cols[i]["auto_start_delegate"], cols[i]["wip_limit"])
		}
	}
}

// ---------------------------------------------------------------- guardrails -----

func TestDeletingTheLastRequiredCategoryColumnIsATypedProblem(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	cols := e.createProject(c, "PAY")
	done := cols[4] // Done(done)

	status, body := e.doJSON(c, "DELETE", "/api/v1/columns/"+done["id"].(string), "")
	if status != http.StatusConflict {
		t.Fatalf("delete last done column = %d, want 409 (body %v)", status, body)
	}
	if body["type"] != "last_category_column" {
		t.Fatalf("problem type = %v, want last_category_column", body["type"])
	}
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "done") {
		t.Fatalf("problem detail %q does not name the category", detail)
	}

	// Changing the last done column's category away is refused for the same reason —
	// otherwise the delete guardrail could be laundered through a PATCH.
	status, body = e.doJSON(c, "PATCH", "/api/v1/columns/"+done["id"].(string),
		`{"category":"review"}`)
	if status != http.StatusConflict || body["type"] != "last_category_column" {
		t.Fatalf("re-categorise last done column = %d/%v, want 409 last_category_column",
			status, body["type"])
	}

	// With a second done column, deleting one is fine.
	status, second := e.doJSON(c, "POST", "/api/v1/projects/PAY/columns",
		`{"name":"Shipped","category":"done"}`)
	if status != http.StatusCreated {
		t.Fatalf("add second done column = %d, want 201", status)
	}
	status, _ = e.doJSON(c, "DELETE", "/api/v1/columns/"+second["id"].(string), "")
	if status != http.StatusNoContent {
		t.Fatalf("delete redundant done column = %d, want 204", status)
	}
}

func TestDeleteWithTicketsRequiresADestinationAndMovesThemAtomically(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	cols := e.createProject(c, "PAY")
	ready, review := cols[1], cols[3]
	projectID := ready["project_id"].(string)
	readyID, reviewID := ready["id"].(string), review["id"].(string)

	t1 := e.addTicket(projectID, readyID, 1)
	t2 := e.addTicket(projectID, readyID, 2)
	e.addTicket(projectID, reviewID, 3) // pre-existing destination ticket

	// No destination: a validation problem naming destination_column_id, nothing deleted.
	status, body := e.doJSON(c, "DELETE", "/api/v1/columns/"+readyID, "")
	if status != http.StatusBadRequest || body["type"] != "validation_failed" {
		t.Fatalf("delete without destination = %d/%v, want 400 validation_failed", status, body["type"])
	}
	errs, _ := body["errors"].([]any)
	if len(errs) != 1 || errs[0].(map[string]any)["field"] != "destination_column_id" {
		t.Fatalf("errors = %v, want one on destination_column_id", body["errors"])
	}

	// Destination = the column being deleted: refused.
	status, body = e.doJSON(c, "DELETE",
		"/api/v1/columns/"+readyID+"?destination_column_id="+readyID, "")
	if status != http.StatusBadRequest || body["type"] != "validation_failed" {
		t.Fatalf("self destination = %d/%v, want 400 validation_failed", status, body["type"])
	}

	// With a destination: 204, tickets in the destination, column gone — atomically.
	status, _ = e.doJSON(c, "DELETE",
		"/api/v1/columns/"+readyID+"?destination_column_id="+reviewID, "")
	if status != http.StatusNoContent {
		t.Fatalf("delete with destination = %d, want 204", status)
	}
	moved, err := e.st.Tickets().ForColumn(context.Background(), reviewID)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 3 {
		t.Fatalf("destination has %d tickets, want 3", len(moved))
	}
	// Relative order preserved, appended after the destination's own ticket.
	if moved[1].ID != t1 || moved[2].ID != t2 {
		t.Fatalf("moved order = %s, %s; want %s, %s", moved[1].ID, moved[2].ID, t1, t2)
	}
	if _, err := e.st.Columns().ByID(context.Background(), readyID); err == nil {
		t.Fatal("deleted column still exists")
	}
}

// ---------------------------------------------------------------- rename -----

// TestRenamingAColumnChangesNothingFunctional is the S09 acceptance criterion: a rename must
// leave every functional lookup — ByCategory, ticket placement, order — exactly as it was.
func TestRenamingAColumnChangesNothingFunctional(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	cols := e.createProject(c, "PAY")
	running := cols[2] // In Progress(running)
	runningID := running["id"].(string)
	projectID := running["project_id"].(string)
	tk := e.addTicket(projectID, runningID, 1)

	byCatBefore, err := e.st.Columns().ByCategory(
		context.Background(), projectID, domain.CategoryRunning)
	if err != nil {
		t.Fatal(err)
	}

	status, body := e.doJSON(c, "PATCH", "/api/v1/columns/"+runningID,
		`{"name":"Cooking"}`)
	if status != http.StatusOK || body["name"] != "Cooking" {
		t.Fatalf("rename = %d %v, want 200 with new name", status, body["name"])
	}

	// The category lookup automation depends on still finds the same column.
	byCatAfter, err := e.st.Columns().ByCategory(
		context.Background(), projectID, domain.CategoryRunning)
	if err != nil {
		t.Fatal(err)
	}
	if len(byCatBefore) != 1 || len(byCatAfter) != 1 || byCatAfter[0].ID != byCatBefore[0].ID {
		t.Fatalf("ByCategory(running) changed across rename: before %v after %v",
			byCatBefore, byCatAfter)
	}
	if byCatAfter[0].Category != domain.CategoryRunning ||
		byCatAfter[0].Position != byCatBefore[0].Position {
		t.Fatal("rename altered category or position")
	}
	// Its ticket did not move.
	got, err := e.st.Tickets().ByID(context.Background(), tk)
	if err != nil {
		t.Fatal(err)
	}
	if got.ColumnID != runningID {
		t.Fatalf("ticket moved on rename: in %s, want %s", got.ColumnID, runningID)
	}
	// Board order is unchanged.
	after := e.columns(c, "PAY")
	if len(after) != len(cols) {
		t.Fatalf("column count changed: %d -> %d", len(cols), len(after))
	}
	for i := range cols {
		if after[i]["id"] != cols[i]["id"] {
			t.Fatalf("board order changed at %d: %v -> %v", i, cols[i]["id"], after[i]["id"])
		}
	}
}

// ---------------------------------------------------------------- reorder, wip, auto-start -----

func TestReorderWipLimitAndAutoStart(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	cols := e.createProject(c, "PAY")
	ids := func(list []map[string]any) []string {
		out := make([]string, len(list))
		for i, m := range list {
			out[i] = m["id"].(string)
		}
		return out
	}
	orig := ids(cols)

	// Move Canceled (last) to the front: after_id null.
	status, _ := e.doJSON(c, "PATCH", "/api/v1/columns/"+orig[5], `{"after_id":null}`)
	if status != http.StatusOK {
		t.Fatalf("reorder to front = %d, want 200", status)
	}
	got := ids(e.columns(c, "PAY"))
	want := []string{orig[5], orig[0], orig[1], orig[2], orig[3], orig[4]}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("after move-to-front: %v, want %v", got, want)
	}

	// Move it back after Done — the midpoint path.
	status, _ = e.doJSON(c, "PATCH", "/api/v1/columns/"+orig[5],
		fmt.Sprintf(`{"after_id":%q}`, orig[4]))
	if status != http.StatusOK {
		t.Fatalf("reorder after done = %d, want 200", status)
	}
	if got := ids(e.columns(c, "PAY")); fmt.Sprint(got) != fmt.Sprint(orig) {
		t.Fatalf("after move-back: %v, want %v", got, orig)
	}

	// Squeeze the gap between columns 0 and 1 until a renumber must happen; order stays right.
	for i := 0; i < 12; i++ {
		status, _ = e.doJSON(c, "PATCH", "/api/v1/columns/"+orig[2],
			fmt.Sprintf(`{"after_id":%q}`, orig[0]))
		if status != http.StatusOK {
			t.Fatalf("squeeze %d = %d, want 200", i, status)
		}
		status, _ = e.doJSON(c, "PATCH", "/api/v1/columns/"+orig[1],
			fmt.Sprintf(`{"after_id":%q}`, orig[0]))
		if status != http.StatusOK {
			t.Fatalf("squeeze %d = %d, want 200", i, status)
		}
	}
	if got := ids(e.columns(c, "PAY")); fmt.Sprint(got) != fmt.Sprint(orig) {
		t.Fatalf("after gap exhaustion: %v, want %v", got, orig)
	}

	// WIP limit set and clear.
	status, body := e.doJSON(c, "PATCH", "/api/v1/columns/"+orig[2], `{"wip_limit":3}`)
	if status != http.StatusOK || body["wip_limit"] != float64(3) {
		t.Fatalf("set wip = %d %v, want 200 wip 3", status, body["wip_limit"])
	}
	status, body = e.doJSON(c, "PATCH", "/api/v1/columns/"+orig[2], `{"wip_limit":null}`)
	if status != http.StatusOK || body["wip_limit"] != nil {
		t.Fatalf("clear wip = %d %v, want 200 null", status, body["wip_limit"])
	}
	status, _ = e.doJSON(c, "PATCH", "/api/v1/columns/"+orig[2], `{"wip_limit":0}`)
	if status != http.StatusBadRequest {
		t.Fatalf("wip 0 = %d, want 400", status)
	}

	// Auto-start toggle round-trips.
	status, body = e.doJSON(c, "PATCH", "/api/v1/columns/"+orig[2], `{"auto_start_delegate":true}`)
	if status != http.StatusOK || body["auto_start_delegate"] != true {
		t.Fatalf("enable auto-start = %d %v", status, body["auto_start_delegate"])
	}
}

// ---------------------------------------------------------------- audit -----

func TestColumnMutationsWriteAudit(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	cols := e.createProject(c, "PAY")

	status, created := e.doJSON(c, "POST", "/api/v1/projects/PAY/columns",
		`{"name":"Later","category":"backlog"}`)
	if status != http.StatusCreated {
		t.Fatalf("create column = %d, want 201", status)
	}
	if _, body := e.doJSON(c, "PATCH",
		"/api/v1/columns/"+created["id"].(string), `{"name":"Much later"}`); body["name"] != "Much later" {
		t.Fatalf("rename failed: %v", body)
	}
	if status, _ := e.doJSON(c, "DELETE", "/api/v1/columns/"+created["id"].(string), ""); status != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", status)
	}

	entries, err := e.st.Audit().List(context.Background(), store.AuditFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, en := range entries {
		if en.TargetKind == "column" {
			seen[en.Action] = true
			if en.ActorKind != domain.ActorHuman {
				t.Errorf("action %s attributed to %v, want human", en.Action, en.ActorKind)
			}
		}
	}
	for _, want := range []string{"column.create", "column.update", "column.delete"} {
		if !seen[want] {
			t.Errorf("no audit entry with action %s (have %v)", want, seen)
		}
	}
	_ = cols
}
