package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// The poller tests run against a mutable snapshot server: the harness's httptest mux serves
// the fake GitHub's *current* data (the S15/S24 fixture pattern), the test mutates the data
// between ticks — "snapshot A, snapshot B" — and events land on a real bus over a real
// migrated store, so dedupe is the actual events-table unique index, not a simulation.

// ------------------------------------------------------------------ fake snapshot GitHub -----

type ghPR struct {
	Number                             int
	Title, Body, State                 string
	Draft                              bool
	MergedAt                           string // non-empty → merged
	Login                              string
	Type                               string // GitHub's user.type: "User" | "Bot" | "" (unreported)
	HeadRef, HeadSHA, BaseRef          string
	Labels                             []string
	Additions, Deletions, ChangedFiles int
	CreatedAt, UpdatedAt               string
}

type ghReview struct {
	ID                 int64
	PR                 int
	Login, State, Body string
	Type               string // GitHub's user.type: "User" | "Bot" | "" (unreported)
	SubmittedAt        string
}

type ghComment struct {
	ID                   int64
	Subject              int // PR/issue number
	Login, Body, Path    string
	Type                 string // GitHub's user.type: "User" | "Bot" | "" (unreported)
	ReviewID             int64  // the review this comment belongs to; 0 = a lone comment
	Line                 int
	CreatedAt, UpdatedAt string
}

type ghSuite struct {
	ID                  int64
	HeadSHA, HeadBranch string
	Status, Conclusion  string
	App                 string
	UpdatedAt           string
}

// ghUser renders the API's `user` object. An empty type is a user object without a `type`
// key at all — the "the forge did not tell us" case, which must not read as a person.
func ghUser(login, userType string) map[string]any {
	u := map[string]any{"login": login}
	if userType != "" {
		u["type"] = userType
	}
	return u
}

// ghFault makes one poll endpoint answer with an error status instead of data, so a test can
// put exactly one of the five passes into the 403 or the 500 and watch what the other four do
// (LEXI-9). hits counts the requests it actually served, which is how "the disabled resource
// stopped being asked" is asserted.
type ghFault struct {
	status int
	body   string
	hits   int
}

type snapshotGH struct {
	mu             sync.Mutex
	prs            []ghPR
	reviews        []ghReview
	reviewComments []ghComment
	issueComments  []ghComment
	suites         []ghSuite
	commitEmails   map[string]string // head sha → author email
	commitMessages map[string]string // head sha → full commit message (trailers included)
	faults         map[string]*ghFault
}

// failWith arms a fault on one poll resource (a resXxx constant).
func (g *snapshotGH) failWith(resource string, status int, body string) {
	defer g.lock()()
	g.faults[resource] = &ghFault{status: status, body: body}
}

func (g *snapshotGH) clearFault(resource string) {
	defer g.lock()()
	delete(g.faults, resource)
}

func (g *snapshotGH) faultHits(resource string) int {
	defer g.lock()()
	if f := g.faults[resource]; f != nil {
		return f.hits
	}
	return 0
}

// faulted serves an armed fault. Callers already hold g.mu.
func (g *snapshotGH) faulted(w http.ResponseWriter, resource string) bool {
	f := g.faults[resource]
	if f == nil {
		return false
	}
	f.hits++
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.status)
	_, _ = w.Write([]byte(f.body))
	return true
}

func (g *snapshotGH) lock() func() { g.mu.Lock(); return g.mu.Unlock }

func (g *snapshotGH) upsertPR(pr ghPR) {
	defer g.lock()()
	for i := range g.prs {
		if g.prs[i].Number == pr.Number {
			g.prs[i] = pr
			return
		}
	}
	g.prs = append(g.prs, pr)
}

func (g *snapshotGH) prJSON(pr ghPR, detail bool) map[string]any {
	labels := make([]map[string]any, 0, len(pr.Labels))
	for _, l := range pr.Labels {
		labels = append(labels, map[string]any{"name": l})
	}
	m := map[string]any{
		"number": pr.Number, "title": pr.Title, "body": pr.Body, "state": pr.State,
		"draft": pr.Draft, "user": ghUser(pr.Login, pr.Type),
		"head":       map[string]any{"ref": pr.HeadRef, "sha": pr.HeadSHA},
		"base":       map[string]any{"ref": pr.BaseRef},
		"labels":     labels,
		"html_url":   fmt.Sprintf("https://github.example/acme/payments/pull/%d", pr.Number),
		"created_at": pr.CreatedAt, "updated_at": pr.UpdatedAt,
	}
	if pr.MergedAt != "" {
		m["merged_at"] = pr.MergedAt
	}
	if detail {
		m["additions"] = pr.Additions
		m["deletions"] = pr.Deletions
		m["changed_files"] = pr.ChangedFiles
		m["merged"] = pr.MergedAt != ""
	}
	return m
}

// install registers the poller's endpoints on the harness mux.
func (g *snapshotGH) install(mux *http.ServeMux) {
	base := "/repos/acme/payments"
	writeAny := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}

	mux.HandleFunc("GET "+base+"/pulls", func(w http.ResponseWriter, _ *http.Request) {
		defer g.lock()()
		if g.faulted(w, resPulls) {
			return
		}
		prs := append([]ghPR(nil), g.prs...)
		// sort=updated&direction=desc, which the forge's since-cutoff relies on
		for i := 0; i < len(prs); i++ {
			for j := i + 1; j < len(prs); j++ {
				if prs[j].UpdatedAt > prs[i].UpdatedAt {
					prs[i], prs[j] = prs[j], prs[i]
				}
			}
		}
		out := make([]map[string]any, 0, len(prs))
		for _, pr := range prs {
			out = append(out, g.prJSON(pr, false))
		}
		writeAny(w, out)
	})
	mux.HandleFunc("GET "+base+"/pulls/comments", func(w http.ResponseWriter, _ *http.Request) {
		defer g.lock()()
		if g.faulted(w, resReviewComments) {
			return
		}
		out := make([]map[string]any, 0, len(g.reviewComments))
		for _, c := range g.reviewComments {
			out = append(out, map[string]any{
				"id": c.ID, "user": ghUser(c.Login, c.Type), "body": c.Body,
				"pull_request_review_id": c.ReviewID,
				"path":                   c.Path, "line": c.Line,
				"pull_request_url": fmt.Sprintf("https://api.github.example%s/pulls/%d", base, c.Subject),
				"html_url":         fmt.Sprintf("https://github.example/acme/payments/pull/%d#discussion", c.Subject),
				"created_at":       c.CreatedAt, "updated_at": c.UpdatedAt,
			})
		}
		writeAny(w, out)
	})
	mux.HandleFunc("GET "+base+"/pulls/{n}", func(w http.ResponseWriter, r *http.Request) {
		defer g.lock()()
		for _, pr := range g.prs {
			if fmt.Sprint(pr.Number) == r.PathValue("n") {
				writeAny(w, g.prJSON(pr, true))
				return
			}
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("GET "+base+"/pulls/{n}/reviews", func(w http.ResponseWriter, r *http.Request) {
		defer g.lock()()
		if g.faulted(w, resReviews) {
			return
		}
		out := make([]map[string]any, 0)
		for _, rev := range g.reviews {
			if fmt.Sprint(rev.PR) == r.PathValue("n") {
				out = append(out, map[string]any{
					"id": rev.ID, "user": ghUser(rev.Login, rev.Type),
					"state": rev.State, "body": rev.Body,
					"html_url":     fmt.Sprintf("https://github.example/acme/payments/pull/%d#review-%d", rev.PR, rev.ID),
					"submitted_at": rev.SubmittedAt,
				})
			}
		}
		writeAny(w, out)
	})
	mux.HandleFunc("GET "+base+"/issues/comments", func(w http.ResponseWriter, _ *http.Request) {
		defer g.lock()()
		if g.faulted(w, resIssueComments) {
			return
		}
		out := make([]map[string]any, 0, len(g.issueComments))
		for _, c := range g.issueComments {
			out = append(out, map[string]any{
				"id": c.ID, "user": ghUser(c.Login, c.Type), "body": c.Body,
				"issue_url":  fmt.Sprintf("https://api.github.example%s/issues/%d", base, c.Subject),
				"html_url":   fmt.Sprintf("https://github.example/acme/payments/pull/%d#comment", c.Subject),
				"created_at": c.CreatedAt, "updated_at": c.UpdatedAt,
			})
		}
		writeAny(w, out)
	})
	mux.HandleFunc("GET "+base+"/commits/{sha}/check-suites", func(w http.ResponseWriter, r *http.Request) {
		defer g.lock()()
		if g.faulted(w, resCheckSuites) {
			return
		}
		suites := make([]map[string]any, 0)
		for _, s := range g.suites {
			if s.HeadSHA == r.PathValue("sha") {
				suites = append(suites, map[string]any{
					"id": s.ID, "head_sha": s.HeadSHA, "head_branch": s.HeadBranch,
					"status": s.Status, "conclusion": s.Conclusion,
					"app":        map[string]any{"name": s.App},
					"url":        fmt.Sprintf("https://api.github.example%s/check-suites/%d", base, s.ID),
					"updated_at": s.UpdatedAt,
				})
			}
		}
		writeAny(w, map[string]any{"total_count": len(suites), "check_suites": suites})
	})
	mux.HandleFunc("GET "+base+"/commits/{sha}", func(w http.ResponseWriter, r *http.Request) {
		defer g.lock()()
		email := g.commitEmails[r.PathValue("sha")]
		message := g.commitMessages[r.PathValue("sha")]
		if message == "" {
			message = "commit"
		}
		writeAny(w, map[string]any{
			"sha": r.PathValue("sha"),
			"commit": map[string]any{
				"message":   message,
				"author":    map[string]any{"email": email},
				"committer": map[string]any{"email": email},
			},
		})
	})
}

// ------------------------------------------------------------------------- poll harness -----

type pollHarness struct {
	*harness
	t       *testing.T
	st      *store.Store
	bus     *bus.Bus
	p       *Poller
	gh      *snapshotGH
	project domain.Project
	repo    domain.Repo
	agent   domain.Agent
	clock   time.Time

	emu    sync.Mutex
	events []domain.Event // successfully published, duplicates excluded
}

func newPollHarness(t *testing.T) *pollHarness {
	t.Helper()
	h := newHarness(t)

	st, err := store.Open(store.Options{
		Path:   filepath.Join(t.TempDir(), "poll.db"),
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ph := &pollHarness{
		harness: h, t: t, st: st,
		gh: &snapshotGH{
			commitEmails:   map[string]string{},
			commitMessages: map[string]string{},
			faults:         map[string]*ghFault{},
		},
		clock: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}
	ph.gh.install(h.mux)
	ph.seed()

	ph.bus = bus.New(bus.Options{Store: st, Logger: discardLogger()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ph.bus.Stop(ctx)
	})

	p := newPoller(h.forge)
	p.store = st
	p.logger = h.forge.logger
	p.now = func() time.Time { return ph.clock }
	p.creds = func(context.Context, domain.Repo) (ports.Creds, error) { return testCreds, nil }
	p.emit = func(ctx context.Context, e domain.Event) error {
		// The bus assigns the id on its own copy, so stamp it here — the collected events are
		// what the chain tests link runs to.
		if e.ID == "" {
			e.ID = domain.NewID()
		}
		err := ph.bus.Publish(ctx, e)
		if errors.Is(err, bus.ErrDuplicate) {
			return nil
		}
		if err != nil {
			return err
		}
		ph.emu.Lock()
		ph.events = append(ph.events, e)
		ph.emu.Unlock()
		return nil
	}
	ph.p = p
	return ph
}

func (ph *pollHarness) seed() {
	ph.t.Helper()
	ctx := context.Background()
	now := domain.Now()
	u := domain.User{
		ID: domain.NewID(), Email: "ada@example.com", DisplayName: "Ada",
		PasswordHash: "x", Role: domain.RoleOwner, AvatarColor: "#123456", CreatedAt: now,
	}
	if err := ph.st.Users().Create(ctx, &u); err != nil {
		ph.t.Fatal(err)
	}
	ph.project = domain.Project{
		ID: domain.NewID(), Key: "PAY", Name: "Payments", OwnerID: u.ID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := ph.st.Projects().Create(ctx, &ph.project); err != nil {
		ph.t.Fatal(err)
	}
	branch := "main"
	ph.repo = domain.Repo{
		ProjectID: ph.project.ID, Provider: "github", Owner: "acme", Name: "payments",
		DefaultBranch: &branch, CreatedAt: now, UpdatedAt: now,
	}
	if err := ph.st.Repos().Create(ctx, &ph.repo); err != nil {
		ph.t.Fatal(err)
	}
	ph.agent = domain.Agent{
		ID: domain.NewID(), ProjectID: ph.project.ID, Name: "Dev", Role: "developer",
		Color: "#888888", RuntimeID: "claude-code", Model: "fake", Effort: "medium",
		Autonomy: domain.AutonomyAuto, GitAuthorName: "Dev",
		GitAuthorEmail: "dev@agents.lexicode.local", ConcurrencyCap: 1,
		MaxWallClockSeconds: 300, MaxSteps: 50, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := ph.st.Agents().Create(ctx, &ph.agent); err != nil {
		ph.t.Fatal(err)
	}
}

// run inserts a run row for the attribution fallback.
func (ph *pollHarness) run(state domain.RunState, branch string, endedAt *time.Time) domain.Run {
	ph.t.Helper()
	nowStr := domain.Now()
	seq, err := ph.st.Runs().NextSeq(context.Background(), ph.project.ID)
	if err != nil {
		ph.t.Fatal(err)
	}
	run := domain.Run{
		ID: domain.NewID(), Seq: seq, ProjectID: ph.project.ID,
		AgentID: ph.agent.ID, State: state, Autonomy: domain.AutonomyAuto,
		Model: "fake", Effort: "medium", Prompt: "p", RuntimeID: "claude-code",
		SandboxID: "docker", SubjectKey: "repo", QueuedAt: nowStr,
	}
	if branch != "" {
		run.Branch = &branch
	}
	if endedAt != nil {
		s := domain.FormatTime(*endedAt)
		run.EndedAt = &s
	}
	if err := ph.st.Runs().Create(context.Background(), &run); err != nil {
		ph.t.Fatal(err)
	}
	return run
}

// mkAgent inserts a second (third, …) agent row for the multi-agent chain tests.
func (ph *pollHarness) mkAgent(name string) domain.Agent {
	ph.t.Helper()
	now := domain.Now()
	a := domain.Agent{
		ID: domain.NewID(), ProjectID: ph.project.ID, Name: name, Role: "reviewer",
		Color: "#888888", RuntimeID: "claude-code", Model: "fake", Effort: "medium",
		Autonomy: domain.AutonomyAuto, GitAuthorName: name,
		GitAuthorEmail: strings.ToLower(name) + "@agents.lexicode.local", ConcurrencyCap: 1,
		MaxWallClockSeconds: 300, MaxSteps: 50, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := ph.st.Agents().Create(context.Background(), &a); err != nil {
		ph.t.Fatal(err)
	}
	return a
}

// trigger inserts a trigger row (the loop guard reads the project and the ledger by its IDs).
func (ph *pollHarness) trigger(name, event, loopConfig string) domain.Trigger {
	ph.t.Helper()
	now := domain.Now()
	tr := domain.Trigger{
		ID: domain.NewID(), ProjectID: ph.project.ID, Name: name, Enabled: true,
		SourceID: pollSourceID, Event: event,
		ActivityTypes: json.RawMessage(`[]`), Filters: json.RawMessage(`{}`),
		Conditions: json.RawMessage(`{"all":[]}`), Actions: json.RawMessage(`[]`),
		LoopConfig: json.RawMessage(loopConfig), CreatedAt: now, UpdatedAt: now,
	}
	if err := ph.st.Triggers().Create(context.Background(), &tr); err != nil {
		ph.t.Fatal(err)
	}
	return tr
}

// chainRun inserts the run row the scheduler would insert for a proceeding verdict: the
// subject key, the guard's computed depth, and the cause event that links it into the chain.
func (ph *pollHarness) chainRun(agentID, subjectKey, causeEventID string, depth int64) domain.Run {
	ph.t.Helper()
	ctx := context.Background()
	seq, err := ph.st.Runs().NextSeq(ctx, ph.project.ID)
	if err != nil {
		ph.t.Fatal(err)
	}
	cause := causeEventID
	run := domain.Run{
		ID: domain.NewID(), Seq: seq, ProjectID: ph.project.ID, AgentID: agentID,
		State: domain.RunCompleted, Autonomy: domain.AutonomyAuto, Model: "fake",
		Effort: "medium", Prompt: "p", RuntimeID: "claude-code", SandboxID: "docker",
		SubjectKey: subjectKey, Depth: depth, CauseEventID: &cause, QueuedAt: domain.Now(),
	}
	if err := ph.st.Runs().Create(ctx, &run); err != nil {
		ph.t.Fatal(err)
	}
	return run
}

// lastEvent returns the most recent collected event of a kind and activity type.
func (ph *pollHarness) lastEvent(kind, activity string) domain.Event {
	ph.t.Helper()
	events := ph.collected()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == kind && events[i].ActivityType == activity {
			return events[i]
		}
	}
	ph.t.Fatalf("no %s/%s event among %v", kind, activity, eventKinds(events))
	return domain.Event{}
}

func (ph *pollHarness) tick() {
	ph.t.Helper()
	if err := ph.p.tick(context.Background(), ph.project.ID); err != nil {
		ph.t.Fatalf("tick: %v", err)
	}
}

func (ph *pollHarness) collected() []domain.Event {
	ph.emu.Lock()
	defer ph.emu.Unlock()
	return append([]domain.Event(nil), ph.events...)
}

func (ph *pollHarness) payload(e domain.Event) map[string]any {
	ph.t.Helper()
	var m map[string]any
	if err := json.Unmarshal(e.Payload, &m); err != nil {
		ph.t.Fatalf("payload: %v", err)
	}
	return m
}

// at renders a fixture timestamp.
func at(hour, minute int) string {
	return time.Date(2026, 8, 1, hour, minute, 0, 0, time.UTC).Format(time.RFC3339)
}

// --------------------------------------------------------------------------- the tests -----

// TestPollerSnapshotSequence is the story's acceptance spine: snapshot A (two open PRs)
// baselines silently; snapshot B (push, review, comment, failed check) yields exactly the
// expected events with derived activity types and contracts §4 payloads; replaying snapshot B
// emits nothing new.
func TestPollerSnapshotSequence(t *testing.T) {
	ph := newPollHarness(t)

	// Snapshot A: two open PRs, one draft.
	ph.gh.upsertPR(ghPR{
		Number: 101, Title: "Fix rounding", Body: "Fix the rounding", State: "open",
		Login: "alice", HeadRef: "feature/rounding", HeadSHA: "sha101a", BaseRef: "main",
		Labels: []string{"bug"}, Additions: 10, Deletions: 2, ChangedFiles: 1,
		CreatedAt: at(9, 0), UpdatedAt: at(10, 0),
	})
	ph.gh.upsertPR(ghPR{
		Number: 102, Title: "Refactor ledger", Body: "WIP", State: "open", Draft: true,
		Login: "bob", HeadRef: "feature/ledger", HeadSHA: "sha102a", BaseRef: "main",
		CreatedAt: at(9, 30), UpdatedAt: at(10, 30),
	})

	ph.tick() // baseline

	if got := ph.collected(); len(got) != 0 {
		t.Fatalf("baseline emitted %d events, want 0: %+v", len(got), got)
	}
	if !strings.Contains(ph.logText(), "baseline — no events emitted") {
		t.Fatalf("baseline log line missing; logs:\n%s", ph.logText())
	}

	// Snapshot B: PR 101 pushed (agent's commit email), a review approved on 101, an issue
	// comment on 102, and a failed check suite on 101's new head.
	ph.gh.upsertPR(ghPR{
		Number: 101, Title: "Fix rounding", Body: "Fix the rounding", State: "open",
		Login: "alice", HeadRef: "feature/rounding", HeadSHA: "sha101b", BaseRef: "main",
		Labels: []string{"bug"}, Additions: 14, Deletions: 3, ChangedFiles: 2,
		CreatedAt: at(9, 0), UpdatedAt: at(12, 10),
	})
	ph.gh.commitEmails["sha101b"] = "dev@agents.lexicode.local"
	ph.gh.reviews = append(ph.gh.reviews, ghReview{
		ID: 9001, PR: 101, Login: "carol", State: "APPROVED", Body: "Ship it",
		SubmittedAt: at(12, 11),
	})
	ph.gh.upsertPR(ghPR{
		Number: 102, Title: "Refactor ledger", Body: "WIP", State: "open", Draft: true,
		Login: "bob", HeadRef: "feature/ledger", HeadSHA: "sha102a", BaseRef: "main",
		CreatedAt: at(9, 30), UpdatedAt: at(12, 12),
	})
	ph.gh.issueComments = append(ph.gh.issueComments, ghComment{
		ID: 7001, Subject: 102, Login: "alice", Body: "Needs a test",
		CreatedAt: at(12, 12), UpdatedAt: at(12, 12),
	})
	ph.gh.suites = append(ph.gh.suites, ghSuite{
		ID: 5001, HeadSHA: "sha101b", HeadBranch: "feature/rounding",
		Status: "completed", Conclusion: "failure", App: "GitHub Actions",
		UpdatedAt: at(12, 13),
	})

	ph.tick()

	got := ph.collected()
	type key struct{ kind, act string }
	var keys []key
	for _, e := range got {
		keys = append(keys, key{e.Kind, e.ActivityType})
	}
	want := []key{
		{"pull_request", "synchronize"},
		{"pull_request_review", "submitted"},
		{"issue_comment", "created"},
		{"check_suite", "completed"},
	}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("event stream = %v, want %v", keys, want)
	}

	// Golden payloads.
	sync := got[0]
	if sync.SubjectKind != "pr" || sync.SubjectNumber == nil || *sync.SubjectNumber != 101 {
		t.Fatalf("synchronize subject = %s/%v", sync.SubjectKind, sync.SubjectNumber)
	}
	if sync.ActorKind != domain.ActorAgent || sync.ActorID == nil || *sync.ActorID != ph.agent.ID {
		t.Fatalf("synchronize actor = %s/%v, want agent %s", sync.ActorKind, sync.ActorID, ph.agent.ID)
	}
	wantSyncPayload := map[string]any{
		"pr": map[string]any{
			"number": float64(101), "title": "Fix rounding", "author": "alice",
			"author_kind": "human", "branch": "feature/rounding", "base": "main",
			"draft": false, "merged": false, "state": "open",
			"additions": float64(14), "deletions": float64(3), "files_changed": float64(2),
			"labels": []any{"bug"}, "body": "Fix the rounding",
			"url": "https://github.example/acme/payments/pull/101",
			// The head commit message rides the synchronize payload (S27): the loop
			// guard scans pr.head_commit_message for skip tokens.
			"head_commit_message": "commit",
		},
		"repo":  map[string]any{"owner": "acme", "name": "payments", "default_branch": "main"},
		"actor": map[string]any{"kind": "agent", "login": "", "agent": "Dev"},
	}
	if diff := ph.payload(sync); !reflect.DeepEqual(diff, wantSyncPayload) {
		t.Fatalf("synchronize payload =\n%v\nwant\n%v", diff, wantSyncPayload)
	}

	review := got[1]
	rp := ph.payload(review)
	wantReview := map[string]any{
		"id": "9001", "author": "carol", "state": "approved", "body": "Ship it",
	}
	if !reflect.DeepEqual(rp["review"], wantReview) {
		t.Fatalf("review payload = %v, want %v", rp["review"], wantReview)
	}
	if rp["pr"].(map[string]any)["number"] != float64(101) {
		t.Fatalf("review pr context = %v", rp["pr"])
	}

	comment := got[2]
	cp := ph.payload(comment)
	wantComment := map[string]any{"id": "7001", "author": "alice", "body": "Needs a test"}
	if !reflect.DeepEqual(cp["comment"], wantComment) {
		t.Fatalf("comment payload = %v, want %v", cp["comment"], wantComment)
	}

	check := got[3]
	kp := ph.payload(check)
	wantCheck := map[string]any{
		"suite_id": "5001", "name": "GitHub Actions", "conclusion": "failure",
		"url": "https://api.github.example/repos/acme/payments/check-suites/5001",
	}
	if !reflect.DeepEqual(kp["check"], wantCheck) {
		t.Fatalf("check payload = %v, want %v", kp["check"], wantCheck)
	}

	// Replay snapshot B unchanged: cursors + dedupe make the second pass silent.
	before := len(ph.collected())
	ph.tick()
	if after := len(ph.collected()); after != before {
		t.Fatalf("replay emitted %d new events, want 0", after-before)
	}
}

// TestPollerDraftReadyAndMergedClose covers the two §7 derivations the loop depends on next:
// draft→ready yields ready_for_review, open→closed carries pr.merged.
func TestPollerDraftReadyAndMergedClose(t *testing.T) {
	ph := newPollHarness(t)

	draft := ghPR{
		Number: 7, Title: "Ship checkout", Body: "…", State: "open", Draft: true,
		Login: "bob", HeadRef: "feature/checkout", HeadSHA: "sha7a", BaseRef: "main",
		CreatedAt: at(9, 0), UpdatedAt: at(10, 0),
	}
	ph.gh.upsertPR(draft)
	ph.tick() // baseline

	draft.Draft = false
	draft.UpdatedAt = at(12, 5)
	ph.gh.upsertPR(draft)
	ph.tick()

	got := ph.collected()
	if len(got) != 1 || got[0].ActivityType != "ready_for_review" {
		t.Fatalf("draft flip yielded %+v, want one ready_for_review", eventKinds(got))
	}

	draft.State = "closed"
	draft.MergedAt = at(12, 30)
	draft.UpdatedAt = at(12, 30)
	ph.gh.upsertPR(draft)
	ph.tick()

	got = ph.collected()
	last := got[len(got)-1]
	if last.ActivityType != "closed" {
		t.Fatalf("close yielded %v, want closed", eventKinds(got))
	}
	pr := ph.payload(last)["pr"].(map[string]any)
	if pr["merged"] != true || pr["state"] != "closed" {
		t.Fatalf("closed payload pr = %v, want merged=true state=closed", pr)
	}
}

// TestPollerActorAttribution exercises the three D-9 signals: the marker comment (agent + run
// directly), the commit email on a push (agent, run via the most-recent-run fallback), and the
// branch template prefix on a PR open.
func TestPollerActorAttribution(t *testing.T) {
	ph := newPollHarness(t)
	ph.tick() // baseline over an empty repo

	// Signal 2 setup: a running run on the branch the push will land on.
	pushRun := ph.run(domain.RunRunning, "feature/pay", nil)
	// Signal 1 setup: a run the marker will name.
	markerRun := ph.run(domain.RunCompleted, "dev/pay-3-add-keys", nil)

	pr := ghPR{
		Number: 3, Title: "Add keys", Body: "adds keys", State: "open",
		Login: "svc-bot", HeadRef: "feature/pay", HeadSHA: "sha3a", BaseRef: "main",
		CreatedAt: at(12, 1), UpdatedAt: at(12, 1),
	}
	ph.gh.upsertPR(pr)
	ph.tick() // pr.opened — no marker, no branch match, no reported user type: external

	// Marker comment (signal 1): agent + run straight from the marker.
	marker := domain.Actor{AgentID: ph.agent.ID, RunID: markerRun.ID}.Marker()
	ph.gh.issueComments = append(ph.gh.issueComments, ghComment{
		ID: 71, Subject: 3, Login: "svc-bot", Body: "Done.\n\n" + marker,
		CreatedAt: at(12, 20), UpdatedAt: at(12, 20),
	})
	pr.UpdatedAt = at(12, 20)
	ph.gh.upsertPR(pr)
	ph.tick()

	events := ph.collected()
	commentEvt := events[len(events)-1]
	if commentEvt.Kind != "issue_comment" {
		t.Fatalf("expected issue_comment, got %v", eventKinds(events))
	}
	if commentEvt.ActorKind != domain.ActorAgent || commentEvt.ActorID == nil ||
		*commentEvt.ActorID != ph.agent.ID {
		t.Fatalf("marker comment actor = %s/%v", commentEvt.ActorKind, commentEvt.ActorID)
	}
	if commentEvt.CauseRunID == nil || *commentEvt.CauseRunID != markerRun.ID {
		t.Fatalf("marker comment cause_run = %v, want %s", commentEvt.CauseRunID, markerRun.ID)
	}

	// Agent-email push (signal 2): synchronize attributed via commit email, run via the
	// most-recent-run-on-branch fallback.
	pr.HeadSHA = "sha3b"
	pr.UpdatedAt = at(12, 40)
	ph.gh.upsertPR(pr)
	ph.gh.commitEmails["sha3b"] = "DEV@agents.lexicode.local" // case-insensitive match
	ph.tick()

	events = ph.collected()
	syncEvt := events[len(events)-1]
	if syncEvt.ActivityType != "synchronize" {
		t.Fatalf("expected synchronize, got %v", eventKinds(events))
	}
	if syncEvt.ActorKind != domain.ActorAgent || syncEvt.ActorID == nil ||
		*syncEvt.ActorID != ph.agent.ID {
		t.Fatalf("push actor = %s/%v", syncEvt.ActorKind, syncEvt.ActorID)
	}
	if syncEvt.CauseRunID == nil || *syncEvt.CauseRunID != pushRun.ID {
		t.Fatalf("push cause_run = %v, want %s", syncEvt.CauseRunID, pushRun.ID)
	}

	// Branch-prefix PR open (signal 3): default template {agent}/{ticket-key}-{slug},
	// agent "Dev" → prefix dev/.
	ph.gh.upsertPR(ghPR{
		Number: 4, Title: "Add idempotency", Body: "no marker here", State: "open",
		Login: "svc-bot", HeadRef: "dev/pay-4-add-idempotency", HeadSHA: "sha4a",
		BaseRef: "main", CreatedAt: at(12, 50), UpdatedAt: at(12, 50),
	})
	ph.tick()

	events = ph.collected()
	var opened *domain.Event
	for i := range events {
		if events[i].ActivityType == "opened" && events[i].SubjectNumber != nil &&
			*events[i].SubjectNumber == 4 {
			opened = &events[i]
		}
	}
	if opened == nil {
		t.Fatalf("no opened event for PR 4: %v", eventKinds(events))
	}
	if opened.ActorKind != domain.ActorAgent || opened.ActorID == nil ||
		*opened.ActorID != ph.agent.ID {
		t.Fatalf("branch-prefix actor = %s/%v", opened.ActorKind, opened.ActorID)
	}
	if p := ph.payload(*opened); p["pr"].(map[string]any)["author_kind"] != "agent" {
		t.Fatalf("branch-prefix pr.author_kind = %v, want agent", p["pr"])
	}

	// The earlier unattributed open, whose author carried no reported user type, stayed
	// external — unknown is not human (see actorKindFor).
	first := events[0]
	if first.ActivityType != "opened" || first.ActorKind != domain.ActorExternal {
		t.Fatalf("untyped PR opened actor = %s (%s)", first.ActorKind, first.ActivityType)
	}
}

// TestPollerPushAttributesToThePushingRun is the depth-counter regression (brief D5). A pull
// request's body carries the marker of the run that OPENED it — forever. Attributing a push by
// that body pins every later push on the PR to the opening run, so the chain
// events.cause_run_id → runs.cause_event_id never accumulates and layer 4 cannot see the cycle
// the brief names. For a `synchronize`, commit identity wins: the head commit's
// `Lexicode-Run:` trailer names the run that produced it.
func TestPollerPushAttributesToThePushingRun(t *testing.T) {
	ph := newPollHarness(t)
	ph.tick() // baseline over an empty repo

	branch := "dev/pay-9-idempotency"
	endedA := time.Date(2026, 8, 1, 12, 30, 0, 0, time.UTC)
	runA := ph.run(domain.RunCompleted, branch, &endedA) // opened the PR, owns the branch name
	// Every run gets a fresh branch, so the follow-up run's own branch is NOT the PR's — it
	// checks the PR's branch out and pushes to it. That is why "the agent's latest run on
	// this branch" resolves to run A and cannot be the signal here.
	runB := ph.run(domain.RunRunning, "dev/run-b", nil)

	pr := ghPR{
		Number: 9, Title: "Add idempotency keys", State: "open", Login: "svc-bot",
		Body: "Automated change by agent **Dev**.\n\n" +
			domain.Actor{AgentID: ph.agent.ID, RunID: runA.ID}.Marker(),
		HeadRef: branch, HeadSHA: "sha9a", BaseRef: "main",
		CreatedAt: at(12, 0), UpdatedAt: at(12, 0),
	}
	ph.gh.upsertPR(pr)
	ph.gh.commitEmails["sha9a"] = "dev@agents.lexicode.local"
	ph.gh.commitMessages["sha9a"] = "feat: idempotency keys\n\nLexicode-Run: " + runA.ID
	ph.tick()

	// The PR-open event does attribute to run A, through the marker. That path is unchanged.
	opened := ph.collected()[len(ph.collected())-1]
	if opened.ActivityType != "opened" {
		t.Fatalf("expected opened, got %v", eventKinds(ph.collected()))
	}
	if opened.CauseRunID == nil || *opened.CauseRunID != runA.ID {
		t.Fatalf("opened cause_run = %v, want the opening run %s", opened.CauseRunID, runA.ID)
	}

	// Run B checks the PR's branch out and pushes to it. The PR body still names run A.
	pr.HeadSHA = "sha9b"
	pr.UpdatedAt = at(12, 40)
	ph.gh.upsertPR(pr)
	ph.gh.commitEmails["sha9b"] = "dev@agents.lexicode.local"
	ph.gh.commitMessages["sha9b"] = "fix: address the review\n\nLexicode-Run: " + runB.ID
	ph.tick()

	events := ph.collected()
	sync := events[len(events)-1]
	if sync.ActivityType != "synchronize" {
		t.Fatalf("expected synchronize, got %v", eventKinds(events))
	}
	if sync.ActorKind != domain.ActorAgent || sync.ActorID == nil || *sync.ActorID != ph.agent.ID {
		t.Fatalf("push actor = %s/%v", sync.ActorKind, sync.ActorID)
	}
	if sync.CauseRunID == nil {
		t.Fatalf("push cause_run is nil, want the pushing run %s", runB.ID)
	}
	if *sync.CauseRunID == runA.ID {
		t.Fatalf("push cause_run = the OPENING run %s; the PR body won over commit identity, "+
			"so the depth chain can never accumulate", runA.ID)
	}
	if *sync.CauseRunID != runB.ID {
		t.Fatalf("push cause_run = %v, want the pushing run %s", *sync.CauseRunID, runB.ID)
	}

	// A trailer naming a run this project does not own is no attribution at all; the commit
	// email still names the agent, and the run falls back to the branch.
	pr.HeadSHA = "sha9c"
	pr.UpdatedAt = at(13, 10)
	ph.gh.upsertPR(pr)
	ph.gh.commitEmails["sha9c"] = "dev@agents.lexicode.local"
	ph.gh.commitMessages["sha9c"] = "chore: something\n\nLexicode-Run: not-a-run-id"
	ph.tick()

	events = ph.collected()
	sync2 := events[len(events)-1]
	if sync2.ActivityType != "synchronize" || sync2.ActorID == nil || *sync2.ActorID != ph.agent.ID {
		t.Fatalf("forged-trailer push = %s actor %v", sync2.ActivityType, sync2.ActorID)
	}
	if sync2.CauseRunID == nil || *sync2.CauseRunID != runA.ID {
		t.Fatalf("forged-trailer push cause_run = %v, want the branch fallback %s",
			sync2.CauseRunID, runA.ID)
	}
}

// TestPollerColdStartWithManyPRs is the brief's own number: connecting a repo with 40 open PRs
// fires nothing.
func TestPollerColdStartWithManyPRs(t *testing.T) {
	ph := newPollHarness(t)
	for i := 1; i <= 40; i++ {
		ph.gh.upsertPR(ghPR{
			Number: i, Title: fmt.Sprintf("PR %d", i), State: "open", Login: "alice",
			HeadRef: fmt.Sprintf("feature/%d", i), HeadSHA: fmt.Sprintf("sha%d", i),
			BaseRef: "main", CreatedAt: at(9, 0), UpdatedAt: at(10, 0),
		})
	}
	ph.tick()
	if got := ph.collected(); len(got) != 0 {
		t.Fatalf("cold start emitted %d events, want 0", len(got))
	}
	if !strings.Contains(ph.logText(), "baseline — no events emitted") {
		t.Fatalf("baseline log line missing")
	}
	// The baseline is durable: an audit row records it for the trigger-history surface.
	_, audits := ph.snapshot()
	found := false
	for _, a := range audits {
		if a.action == "github.poll.baseline" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no github.poll.baseline audit row; audits: %+v", audits)
	}
	// And the state was recorded: a follow-up tick with a push emits exactly one event.
	ph.gh.upsertPR(ghPR{
		Number: 17, Title: "PR 17", State: "open", Login: "alice",
		HeadRef: "feature/17", HeadSHA: "sha17b", BaseRef: "main",
		CreatedAt: at(9, 0), UpdatedAt: at(12, 30),
	})
	ph.tick()
	got := ph.collected()
	if len(got) != 1 || got[0].ActivityType != "synchronize" {
		t.Fatalf("post-baseline push yielded %v, want one synchronize", eventKinds(got))
	}
}

// TestPollerIntervalFloor: workspace poll_interval_seconds drives the tick interval, default
// 30s, floor 10s — a 5s setting clamps.
func TestPollerIntervalFloor(t *testing.T) {
	ph := newPollHarness(t)
	ctx := context.Background()

	if got := ph.p.interval(ctx); got != 30*time.Second {
		t.Fatalf("default interval = %v, want 30s", got)
	}

	ws, err := ph.st.Workspace().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ws.PollIntervalSeconds = 5
	if err := ph.st.Workspace().Update(ctx, &ws); err != nil {
		t.Fatal(err)
	}
	if got := ph.p.interval(ctx); got != 10*time.Second {
		t.Fatalf("interval with 5s setting = %v, want the 10s floor", got)
	}

	ws.PollIntervalSeconds = 60
	if err := ph.st.Workspace().Update(ctx, &ws); err != nil {
		t.Fatal(err)
	}
	if got := ph.p.interval(ctx); got != time.Minute {
		t.Fatalf("interval with 60s setting = %v, want 1m", got)
	}
}

// TestPollerLifecycle runs the real worker goroutine once: Start finds the connected repo,
// baselines it, and Stop drains cleanly.
func TestPollerLifecycle(t *testing.T) {
	ph := newPollHarness(t)
	ctx := context.Background()

	if err := ph.p.Start(ctx, ph.p.emit); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := ph.st.PollCursors().Get(ctx, ph.project.ID, resPulls); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker never baselined; logs:\n%s", ph.logText())
		}
		time.Sleep(10 * time.Millisecond)
	}
	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ph.p.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func eventKinds(events []domain.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind+"."+e.ActivityType)
	}
	return out
}

// TestPollerReviewActorKindFollowsUserType is the fix for the bug that made the loop guard's
// depth reset dead code for anything happening on the forge: every non-agent actor was
// stamped `external`, and `NewestHumanActionAt` matches `human`, so commenting on a
// loop-stopped pull request left the next agent event still loop-stopped.
//
// GitHub reports `user.type` on reviews, comments and issues. A non-agent "User" is a person
// (`human`, which resets the depth counter and satisfies `actor.is_human`); a "Bot" — and an
// unreported type — stays `external`. Agent attribution still runs first and still wins, run
// id included: relabelling an unattributed agent as human is exactly the weakening this must
// not do.
func TestPollerReviewActorKindFollowsUserType(t *testing.T) {
	ph := newPollHarness(t)
	ph.tick() // baseline over an empty repo

	agentRun := ph.run(domain.RunCompleted, "dev/pay-5-keys", nil)
	pr := ghPR{
		Number: 5, Title: "Add keys", Body: "adds keys", State: "open",
		Login: "svc-bot", HeadRef: "feature/pay", HeadSHA: "sha5a", BaseRef: "main",
		CreatedAt: at(12, 1), UpdatedAt: at(12, 1),
	}
	ph.gh.upsertPR(pr)
	ph.tick()

	// Three reviews on one pull request, differing only in who submitted them.
	marker := domain.Actor{AgentID: ph.agent.ID, RunID: agentRun.ID}.Marker()
	ph.gh.reviews = append(ph.gh.reviews,
		ghReview{ID: 901, PR: 5, Login: "ada", Type: "User", State: "COMMENTED",
			Body: "Two things here.", SubmittedAt: at(12, 10)},
		ghReview{ID: 902, PR: 5, Login: "dependabot[bot]", Type: "Bot", State: "COMMENTED",
			Body: "Bumped a dependency.", SubmittedAt: at(12, 11)},
		ghReview{ID: 903, PR: 5, Login: "svc-bot", Type: "User", State: "COMMENTED",
			Body: "Reviewed.\n\n" + marker, SubmittedAt: at(12, 12)},
	)
	pr.UpdatedAt = at(12, 12)
	ph.gh.upsertPR(pr)
	ph.tick()

	byID := map[string]domain.Event{}
	for _, e := range ph.collected() {
		if e.Kind != kindReview {
			continue
		}
		byID[ph.payload(e)["review"].(map[string]any)["id"].(string)] = e
	}
	if len(byID) != 3 {
		t.Fatalf("review events = %d, want 3: %v", len(byID), byID)
	}

	// A person: human, with the login recorded and NO actor id — the kind is what we learned,
	// the identity is still unknowable (a GitHub login is not a workspace user, D-9/S25).
	human := byID["901"]
	if human.ActorKind != domain.ActorHuman {
		t.Fatalf("human review actor_kind = %s, want human", human.ActorKind)
	}
	if human.ActorID != nil {
		t.Fatalf("human review actor_id = %v, want nil — the login does not identify a user", *human.ActorID)
	}
	if human.ActorLogin == nil || *human.ActorLogin != "ada" {
		t.Fatalf("human review actor_login = %v, want ada", human.ActorLogin)
	}
	// The payload's actor sub-object is what `actor.is_human` reads (it is not the column),
	// so the two have to agree.
	if got := ph.payload(human)["actor"]; !reflect.DeepEqual(got,
		map[string]any{"kind": "human", "login": "ada", "agent": ""}) {
		t.Fatalf("human review payload actor = %v", got)
	}

	// A bot: external. It must not reset the depth counter, and `actor.is_human` must not
	// match it.
	bot := byID["902"]
	if bot.ActorKind != domain.ActorExternal {
		t.Fatalf("bot review actor_kind = %s, want external", bot.ActorKind)
	}
	if got := ph.payload(bot)["actor"].(map[string]any)["kind"]; got != "external" {
		t.Fatalf("bot review payload actor.kind = %v, want external", got)
	}

	// One of ours: agent, with the run the marker names — user.type says "User" here too,
	// because every agent writes through the project's shared PAT (D-9). Attribution wins.
	agent := byID["903"]
	if agent.ActorKind != domain.ActorAgent || agent.ActorID == nil || *agent.ActorID != ph.agent.ID {
		t.Fatalf("agent review actor = %s/%v, want agent %s", agent.ActorKind, agent.ActorID, ph.agent.ID)
	}
	if agent.CauseRunID == nil || *agent.CauseRunID != agentRun.ID {
		t.Fatalf("agent review cause_run = %v, want %s", agent.CauseRunID, agentRun.ID)
	}
	if got := ph.payload(agent)["actor"]; !reflect.DeepEqual(got,
		map[string]any{"kind": "agent", "login": "svc-bot", "agent": "Dev"}) {
		t.Fatalf("agent review payload actor = %v", got)
	}
}

// TestPollerDerivedPushStaysExternal pins the other half of the decision: an event the poller
// DERIVES from state diffing has no API actor to ask, so it gets no user type and stays
// external. Calling it human would let an unattributed agent push reset the depth counter,
// which is the loop guard failing open — the opposite of the point.
func TestPollerDerivedPushStaysExternal(t *testing.T) {
	ph := newPollHarness(t)
	ph.tick()

	pr := ghPR{
		Number: 6, Title: "Tidy", Body: "no marker", State: "open",
		Login: "ada", Type: "User", HeadRef: "tidy", HeadSHA: "sha6a", BaseRef: "main",
		CreatedAt: at(12, 1), UpdatedAt: at(12, 1),
	}
	ph.gh.upsertPR(pr)
	ph.tick()

	// The open names its author, and that author is a person.
	opened := ph.lastEvent(kindPullRequest, "opened")
	if opened.ActorKind != domain.ActorHuman {
		t.Fatalf("opened actor_kind = %s, want human", opened.ActorKind)
	}

	// The push does not: no endpoint says who pushed, and the commit read attributes nothing.
	pr.HeadSHA = "sha6b"
	pr.UpdatedAt = at(12, 30)
	ph.gh.upsertPR(pr)
	ph.tick()

	sync := ph.lastEvent(kindPullRequest, "synchronize")
	if sync.ActorKind != domain.ActorExternal {
		t.Fatalf("synchronize actor_kind = %s, want external", sync.ActorKind)
	}
	if got := ph.payload(sync)["actor"].(map[string]any)["kind"]; got != "external" {
		t.Fatalf("synchronize payload actor.kind = %v, want external", got)
	}
}

// TestPollerReviewCommentCarriesItsReview: every inline comment of a review says which review
// it belongs to, which is what lets the guard coalesce the fragments into one run and the
// `review` context provider assemble them into one prompt section (LEXI-10). A comment the
// forge grouped into no review says so with an empty string rather than a wrong id.
func TestPollerReviewCommentCarriesItsReview(t *testing.T) {
	ph := newPollHarness(t)
	ph.gh.upsertPR(ghPR{
		Number: 219, Title: "Add rate limiting", State: "open", Login: "alice",
		HeadRef: "feature/rate", HeadSHA: "sha219a", BaseRef: "main",
		CreatedAt: at(9, 0), UpdatedAt: at(10, 0),
	})
	ph.tick() // baseline

	ph.gh.upsertPR(ghPR{
		Number: 219, Title: "Add rate limiting", State: "open", Login: "alice",
		HeadRef: "feature/rate", HeadSHA: "sha219a", BaseRef: "main",
		CreatedAt: at(9, 0), UpdatedAt: at(12, 0),
	})
	ph.gh.reviewComments = append(ph.gh.reviewComments,
		ghComment{ID: 2001, Subject: 219, Login: "bob", Body: "off by one",
			Path: "internal/api/rate.go", Line: 42, ReviewID: 555,
			CreatedAt: at(12, 1), UpdatedAt: at(12, 1)},
		ghComment{ID: 2002, Subject: 219, Login: "bob", Body: "a lone remark",
			Path: "README.md", Line: 4,
			CreatedAt: at(12, 2), UpdatedAt: at(12, 2)},
	)
	ph.tick()

	events := ph.collected()
	var comments []domain.Event
	for _, e := range events {
		if e.Kind == kindReviewComment {
			comments = append(comments, e)
		}
	}
	if len(comments) != 2 {
		t.Fatalf("emitted %d review comment events, want 2: %v", len(comments), eventKinds(events))
	}
	first := ph.payload(comments[0])["comment"].(map[string]any)
	if first["review_id"] != "555" {
		t.Errorf("comment.review_id = %v, want \"555\"", first["review_id"])
	}
	if first["path"] != "internal/api/rate.go" || first["line"] != float64(42) {
		t.Errorf("comment payload = %+v", first)
	}
	second := ph.payload(comments[1])["comment"].(map[string]any)
	if second["review_id"] != "" {
		t.Errorf("a comment in no review has review_id %v, want \"\"", second["review_id"])
	}
}
