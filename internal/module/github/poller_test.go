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
	HeadRef, HeadSHA, BaseRef          string
	Labels                             []string
	Additions, Deletions, ChangedFiles int
	CreatedAt, UpdatedAt               string
}

type ghReview struct {
	ID                 int64
	PR                 int
	Login, State, Body string
	SubmittedAt        string
}

type ghComment struct {
	ID                   int64
	Subject              int // PR/issue number
	Login, Body, Path    string
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

type snapshotGH struct {
	mu             sync.Mutex
	prs            []ghPR
	reviews        []ghReview
	reviewComments []ghComment
	issueComments  []ghComment
	suites         []ghSuite
	commitEmails   map[string]string // head sha → author email
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
		"draft": pr.Draft, "user": map[string]any{"login": pr.Login},
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
		out := make([]map[string]any, 0, len(g.reviewComments))
		for _, c := range g.reviewComments {
			out = append(out, map[string]any{
				"id": c.ID, "user": map[string]any{"login": c.Login}, "body": c.Body,
				"path": c.Path, "line": c.Line,
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
		out := make([]map[string]any, 0)
		for _, rev := range g.reviews {
			if fmt.Sprint(rev.PR) == r.PathValue("n") {
				out = append(out, map[string]any{
					"id": rev.ID, "user": map[string]any{"login": rev.Login},
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
		out := make([]map[string]any, 0, len(g.issueComments))
		for _, c := range g.issueComments {
			out = append(out, map[string]any{
				"id": c.ID, "user": map[string]any{"login": c.Login}, "body": c.Body,
				"issue_url":  fmt.Sprintf("https://api.github.example%s/issues/%d", base, c.Subject),
				"html_url":   fmt.Sprintf("https://github.example/acme/payments/pull/%d#comment", c.Subject),
				"created_at": c.CreatedAt, "updated_at": c.UpdatedAt,
			})
		}
		writeAny(w, out)
	})
	mux.HandleFunc("GET "+base+"/commits/{sha}/check-suites", func(w http.ResponseWriter, r *http.Request) {
		defer g.lock()()
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
		writeAny(w, map[string]any{
			"sha": r.PathValue("sha"),
			"commit": map[string]any{
				"message":   "commit",
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
		gh:    &snapshotGH{commitEmails: map[string]string{}},
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
	ph.tick() // pr.opened — human branch, no marker: external actor

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

	// The earlier human-branch opened event stayed external.
	first := events[0]
	if first.ActivityType != "opened" || first.ActorKind != domain.ActorExternal {
		t.Fatalf("human PR opened actor = %s (%s)", first.ActorKind, first.ActivityType)
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
