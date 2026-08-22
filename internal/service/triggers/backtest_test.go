package triggers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
)

// backtestResponse mirrors the wire shape of POST /triggers/{id}/backtest.
type backtestResponse struct {
	Days      int  `json:"days"`
	Scanned   int  `json:"scanned"`
	Matched   int  `json:"matched"`
	Truncated bool `json:"truncated"`
	Events    []struct {
		EventID      string  `json:"event_id"`
		Kind         string  `json:"kind"`
		ActivityType string  `json:"activity_type"`
		ActorKind    string  `json:"actor_kind"`
		ActorLogin   *string `json:"actor_login"`
		Subject      string  `json:"subject"`
		OccurredAt   string  `json:"occurred_at"`
	} `json:"events"`
	WouldDo   []string `json:"would_do"`
	NoHistory bool     `json:"no_history"`
}

// seedEvent inserts one stored pull_request event for the project, `ago` before now, with
// the given activity type and pr.files_changed. Subject is pr:<n>.
func seedEvent(t *testing.T, e *env, projectID string, n int64, activity string, filesChanged int, ago time.Duration) string {
	t.Helper()
	now := time.Now().UTC()
	occurred := domain.FormatTime(now.Add(-ago))
	login := "ada"
	payload, err := json.Marshal(map[string]any{
		"pr": map[string]any{
			"number": n, "branch": "dev/x", "files_changed": filesChanged,
		},
		"actor": map[string]any{"kind": "human", "login": login},
	})
	if err != nil {
		t.Fatal(err)
	}
	ev := domain.Event{
		ID: domain.NewID(), ProjectID: &projectID, Source: "github.poll",
		Kind: "pull_request", ActivityType: activity,
		ActorKind: domain.ActorHuman, ActorLogin: &login,
		SubjectKind: "pr", SubjectNumber: &n,
		Payload:       payload,
		DedupeKey:     fmt.Sprintf("seed-%s-%d-%s-%d-%s", projectID, n, activity, filesChanged, occurred),
		DispatchState: domain.DispatchDone,
		OccurredAt:    occurred, CreatedAt: occurred,
	}
	if err := e.st.Events().Insert(context.Background(), &ev); err != nil {
		t.Fatal(err)
	}
	return ev.ID
}

// backtestFixture creates the project, a rule (pull_request · opened, files_changed < 400,
// one registered `picky` action) and five stored events:
//
//	#1  opened, 10 files,  1 day ago   → matches
//	#2  opened, 500 files, 2 days ago  → conditions fail
//	#3  synchronize, 10 files, 1 day ago → stage-1 fail (activity)
//	#4  opened, 50 files, 10 days ago  → matches, but outside the 7-day window
//	#5  opened, 10 files, 40 days ago  → outside any window (max 30)
func backtestFixture(t *testing.T, e *env, c *http.Client, key string) (triggerID, projectID string) {
	t.Helper()
	e.project(c, key)
	p, err := e.st.Projects().ByKey(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}

	status, body := e.doJSON(c, "POST", "/api/v1/projects/"+key+"/triggers", `{
		"name": "PR opened → picky",
		"event": "pull_request",
		"activity_types": ["opened"],
		"conditions": {"all": [{"field": "pr.files_changed", "op": "number.lt", "value": 400}]},
		"actions": [{"action_id": "picky", "params": {"agent": "Reviewer"}}]
	}`)
	if status != http.StatusCreated {
		t.Fatalf("create trigger = %d: %s", status, body)
	}
	var tr struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		t.Fatal(err)
	}

	seedEvent(t, e, p.ID, 219, "opened", 10, 24*time.Hour)
	seedEvent(t, e, p.ID, 220, "opened", 500, 48*time.Hour)
	seedEvent(t, e, p.ID, 221, "synchronize", 10, 24*time.Hour)
	seedEvent(t, e, p.ID, 218, "opened", 50, 10*24*time.Hour)
	seedEvent(t, e, p.ID, 200, "opened", 10, 40*24*time.Hour)
	return tr.ID, p.ID
}

func postBacktest(t *testing.T, e *env, c *http.Client, path, body string) (int, backtestResponse, []byte) {
	t.Helper()
	status, raw := e.doJSON(c, "POST", path, body)
	var res backtestResponse
	if status == http.StatusOK {
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Fatalf("unmarshal backtest response: %v: %s", err, raw)
		}
	}
	return status, res, raw
}

func TestBacktestStoredRule(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	trID, _ := backtestFixture(t, e, c, "PAY")

	// Default window: 7 days → events #1..#3 scanned, #1 matches.
	status, res, raw := postBacktest(t, e, c, "/api/v1/triggers/"+trID+"/backtest", "")
	if status != http.StatusOK {
		t.Fatalf("backtest = %d: %s", status, raw)
	}
	if res.Days != 7 || res.Scanned != 3 || res.Matched != 1 || res.Truncated {
		t.Fatalf("got days=%d scanned=%d matched=%d truncated=%v, want 7/3/1/false",
			res.Days, res.Scanned, res.Matched, res.Truncated)
	}
	if len(res.Events) != 1 || res.Events[0].Subject != "pr:219" ||
		res.Events[0].ActivityType != "opened" || res.Events[0].ActorKind != "human" {
		t.Fatalf("events = %+v, want one pr:219 opened by a human", res.Events)
	}
	if len(res.WouldDo) != 1 || res.WouldDo[0] != "picky Reviewer" {
		t.Fatalf("would_do = %v, want [picky Reviewer]", res.WouldDo)
	}
	if res.NoHistory {
		t.Fatal("no_history = true on a project with events")
	}

	// A 14-day window picks up event #4 too, newest first.
	status, res, raw = postBacktest(t, e, c, "/api/v1/triggers/"+trID+"/backtest?days=14", "")
	if status != http.StatusOK {
		t.Fatalf("backtest days=14 = %d: %s", status, raw)
	}
	if res.Days != 14 || res.Scanned != 4 || res.Matched != 2 {
		t.Fatalf("got days=%d scanned=%d matched=%d, want 14/4/2", res.Days, res.Scanned, res.Matched)
	}
	if len(res.Events) != 2 || res.Events[0].Subject != "pr:219" || res.Events[1].Subject != "pr:218" {
		t.Fatalf("events = %+v, want pr:219 then pr:218 (newest first)", res.Events)
	}
}

func TestBacktestDraftDoesNotSave(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	trID, _ := backtestFixture(t, e, c, "PAY")

	before, err := e.st.Triggers().ByID(context.Background(), trID)
	if err != nil {
		t.Fatal(err)
	}

	// The draft tightens the condition: files_changed < 5 excludes every event.
	draft := `{
		"name": "PR opened → picky",
		"event": "pull_request",
		"activity_types": ["opened"],
		"conditions": {"all": [{"field": "pr.files_changed", "op": "number.lt", "value": 5}]},
		"actions": [{"action_id": "picky", "params": {"agent": "Reviewer"}}]
	}`
	status, res, raw := postBacktest(t, e, c, "/api/v1/triggers/"+trID+"/backtest?days=14", draft)
	if status != http.StatusOK {
		t.Fatalf("draft backtest = %d: %s", status, raw)
	}
	if res.Matched != 0 || res.Scanned != 4 {
		t.Fatalf("draft got scanned=%d matched=%d, want 4/0", res.Scanned, res.Matched)
	}
	if res.NoHistory {
		t.Fatal("no_history = true, but the project has history — this is a plain zero")
	}

	// A draft that widens the condition sees more — same stored row.
	widened := `{"conditions": {"all": []}}`
	status, res, raw = postBacktest(t, e, c, "/api/v1/triggers/"+trID+"/backtest?days=14", widened)
	if status != http.StatusOK {
		t.Fatalf("widened draft backtest = %d: %s", status, raw)
	}
	if res.Matched != 3 { // #1, #2 (no condition now), #4 — activity still narrows out #3
		t.Fatalf("widened draft matched = %d, want 3", res.Matched)
	}

	// The stored rule is unchanged in the database.
	after, err := e.st.Triggers().ByID(context.Background(), trID)
	if err != nil {
		t.Fatal(err)
	}
	if string(after.Conditions) != string(before.Conditions) || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("stored trigger changed: conditions %s → %s, updated_at %s → %s",
			before.Conditions, after.Conditions, before.UpdatedAt, after.UpdatedAt)
	}

	// An invalid draft is refused with the field-level 400, like save would be.
	status, _, raw = postBacktest(t, e, c, "/api/v1/triggers/"+trID+"/backtest",
		`{"conditions": {"all": [{"field": "pr.x", "op": "not.an.operator", "value": 1}]}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("invalid draft = %d: %s, want 400", status, raw)
	}
}

func TestBacktestDaysClamping(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	trID, _ := backtestFixture(t, e, c, "PAY")

	for _, tc := range []struct {
		query string
		want  int
	}{
		{"?days=99", 30},
		{"?days=31", 30},
		{"?days=0", 1},
		{"?days=-3", 1},
		{"?days=14", 14},
		{"", 7},
	} {
		status, res, raw := postBacktest(t, e, c, "/api/v1/triggers/"+trID+"/backtest"+tc.query, "")
		if status != http.StatusOK {
			t.Fatalf("backtest %q = %d: %s", tc.query, status, raw)
		}
		if res.Days != tc.want {
			t.Fatalf("backtest %q → days=%d, want %d", tc.query, res.Days, tc.want)
		}
	}

	// Even the 30-day maximum never reaches event #5 (40 days ago).
	status, res, _ := postBacktest(t, e, c, "/api/v1/triggers/"+trID+"/backtest?days=99", "")
	if status != http.StatusOK || res.Scanned != 4 {
		t.Fatalf("days=99 scanned=%d, want 4 (the 40-day-old event stays out)", res.Scanned)
	}

	status, _, raw := postBacktest(t, e, c, "/api/v1/triggers/"+trID+"/backtest?days=abc", "")
	if status != http.StatusBadRequest {
		t.Fatalf("days=abc = %d: %s, want 400", status, raw)
	}
}

func TestBacktestNoHistoryIsDistinct(t *testing.T) {
	e := newEnv(t)
	c := e.owner()

	// A project whose only events are internal bookkeeping → the distinct empty state.
	// (A real project carries `internal`-source events — project.created and the like —
	// from its first second; they are scanned but are not "history" in the empty-state
	// sense, which builds up from repo connection.)
	e.project(c, "EMP")
	pEmp, err := e.st.Projects().ByKey(context.Background(), "EMP")
	if err != nil {
		t.Fatal(err)
	}
	occurred := domain.FormatTime(time.Now().UTC().Add(-time.Hour))
	internal := domain.Event{
		ID: domain.NewID(), ProjectID: &pEmp.ID, Source: "internal",
		Kind: "project", ActivityType: "created", ActorKind: domain.ActorSystem,
		SubjectKind: "project", Payload: json.RawMessage(`{}`),
		DedupeKey: "internal-emp-1", DispatchState: domain.DispatchDone,
		OccurredAt: occurred, CreatedAt: occurred,
	}
	if err := e.st.Events().Insert(context.Background(), &internal); err != nil {
		t.Fatal(err)
	}
	status, body := e.doJSON(c, "POST", "/api/v1/projects/EMP/triggers",
		`{"name": "Empty", "event": "pull_request", "activity_types": ["opened"]}`)
	if status != http.StatusCreated {
		t.Fatalf("create trigger = %d: %s", status, body)
	}
	var tr struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		t.Fatal(err)
	}
	st, res, raw := postBacktest(t, e, c, "/api/v1/triggers/"+tr.ID+"/backtest", "")
	if st != http.StatusOK {
		t.Fatalf("backtest = %d: %s", st, raw)
	}
	if !res.NoHistory || res.Scanned != 1 || res.Matched != 0 {
		t.Fatalf("got no_history=%v scanned=%d matched=%d, want true/1/0 — the internal event is scanned but is not history",
			res.NoHistory, res.Scanned, res.Matched)
	}

	// A project whose only external history is out of the window is NOT the no-history state.
	e.project(c, "OLD")
	p, err := e.st.Projects().ByKey(context.Background(), "OLD")
	if err != nil {
		t.Fatal(err)
	}
	seedEvent(t, e, p.ID, 1, "opened", 10, 40*24*time.Hour)
	status, body = e.doJSON(c, "POST", "/api/v1/projects/OLD/triggers",
		`{"name": "Old", "event": "pull_request", "activity_types": ["opened"]}`)
	if status != http.StatusCreated {
		t.Fatalf("create trigger = %d: %s", status, body)
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		t.Fatal(err)
	}
	st, res, raw = postBacktest(t, e, c, "/api/v1/triggers/"+tr.ID+"/backtest", "")
	if st != http.StatusOK {
		t.Fatalf("backtest = %d: %s", st, raw)
	}
	if res.NoHistory || res.Scanned != 0 {
		t.Fatalf("got no_history=%v scanned=%d, want false/0 — history exists, just not in the window",
			res.NoHistory, res.Scanned)
	}
}
