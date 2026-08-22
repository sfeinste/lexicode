package contextmod

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "s34.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func seedProject(t *testing.T, st *store.Store) domain.Project {
	t.Helper()
	ctx := context.Background()
	now := domain.Now()
	u := domain.User{
		ID: domain.NewID(), Email: "ada@example.com", DisplayName: "Ada",
		PasswordHash: "x", Role: domain.RoleOwner, AvatarColor: "#123456", CreatedAt: now,
	}
	if err := st.Users().Create(ctx, &u); err != nil {
		t.Fatal(err)
	}
	p := domain.Project{
		ID: domain.NewID(), Key: "PAY", Name: "Payments", OwnerID: u.ID,
		Color: "#336699", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Projects().Create(ctx, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func seedPage(t *testing.T, st *store.Store, projectID, title string, scope domain.AgentScope, mutate func(*domain.WikiPage)) domain.WikiPage {
	t.Helper()
	now := domain.Now()
	pg := domain.WikiPage{
		ID: domain.NewID(), ProjectID: projectID, Slug: domain.NewID() + "-" + title,
		Title: title, Position: 1, AgentScope: scope,
		Body: "Body of " + title + ".", TokenEstimate: int64(len(title)),
		State: domain.WikiLive, CreatedAt: now, UpdatedAt: now,
	}
	if mutate != nil {
		mutate(&pg)
	}
	if err := st.Wiki().CreatePage(context.Background(), &pg); err != nil {
		t.Fatal(err)
	}
	return pg
}

// TestWikiProviderScopes is the architecture §11 provider-table contract: `always` pages
// with reason exactly "always"; `paths` pages whose globs match a changed path with the
// matched path named; `auto` pages retrieved by keyword with the keyword named; `manual`
// and `never` pages never auto-injected.
func TestWikiProviderScopes(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)

	seedPage(t, st, p.ID, "Working agreements", domain.ScopeAlways, nil)
	seedPage(t, st, p.ID, "Deploy pitfalls", domain.ScopePaths, func(pg *domain.WikiPage) {
		pg.ScopePaths = []string{"infra/**"}
	})
	seedPage(t, st, p.ID, "Deployment runbook", domain.ScopeAuto, func(pg *domain.WikiPage) {
		pg.Tags = []string{"deployment"}
	})
	seedPage(t, st, p.ID, "Manual notes", domain.ScopeManual, nil)
	seedPage(t, st, p.ID, "Never page", domain.ScopeNever, nil)

	prov := NewWikiProvider(st)
	items, err := prov.Resolve(context.Background(), ports.ContextRequest{
		ProjectID:    p.ID,
		TaskSummary:  "Fix the deployment pipeline",
		ChangedPaths: []string{"README.md", "infra/deploy.ts"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3 (always, paths, auto): %+v", len(items), items)
	}
	wantReasons := []string{"always", "matched path infra/deploy.ts", `retrieved for "deployment"`}
	for i, want := range wantReasons {
		if items[i].Reason != want {
			t.Errorf("items[%d].Reason = %q, want %q", i, items[i].Reason, want)
		}
		if !items[i].Injected {
			t.Errorf("items[%d].Injected = false, want true", i)
		}
		if items[i].SourceKind != "wiki" {
			t.Errorf("items[%d].SourceKind = %q, want wiki", i, items[i].SourceKind)
		}
		if items[i].Body == "" {
			t.Errorf("items[%d].Body empty; wiki pages are injected", i)
		}
	}
}

// TestWikiProviderDryYieldsAlwaysOnly: a dry resolve carries no changed paths and no task
// summary, so only `always` pages appear — "what every run of this agent sees".
func TestWikiProviderDryYieldsAlwaysOnly(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	seedPage(t, st, p.ID, "Working agreements", domain.ScopeAlways, nil)
	seedPage(t, st, p.ID, "Deploy pitfalls", domain.ScopePaths, func(pg *domain.WikiPage) {
		pg.ScopePaths = []string{"**"}
	})
	seedPage(t, st, p.ID, "Deployment runbook", domain.ScopeAuto, func(pg *domain.WikiPage) {
		pg.Tags = []string{"deployment"}
	})

	items, err := NewWikiProvider(st).Resolve(context.Background(),
		ports.ContextRequest{ProjectID: p.ID, Dry: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Reason != "always" {
		t.Fatalf("dry items = %+v, want the one always page", items)
	}
}

// TestWikiProviderSkipsProposalsAndArchived: proposed and archived pages never steer runs.
func TestWikiProviderSkipsProposalsAndArchived(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	seedPage(t, st, p.ID, "Proposed page", domain.ScopeAlways, func(pg *domain.WikiPage) {
		pg.State = domain.WikiProposed
	})
	now := domain.Now()
	seedPage(t, st, p.ID, "Archived page", domain.ScopeAlways, func(pg *domain.WikiPage) {
		pg.ArchivedAt = &now
	})

	items, err := NewWikiProvider(st).Resolve(context.Background(),
		ports.ContextRequest{ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %+v, want none", items)
	}
}

func TestPathGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"infra/**", "infra/deploy.ts", true},
		{"infra/**", "infra/a/b/c.ts", true},
		{"infra/**", "web/deploy.ts", false},
		{"infra/*.ts", "infra/deploy.ts", true},
		{"infra/*.ts", "infra/sub/deploy.ts", false}, // * never crosses /
		{"**/deploy.ts", "infra/deploy.ts", true},
		{"**", "anything/at/all", true},
		{"*.md", "README.md", true},
		{"*.md", "docs/README.md", false},
		{"docs/[", "docs/x", false}, // malformed matches nothing
	}
	for _, c := range cases {
		if got := pathGlobMatch(c.pattern, c.path); got != c.want {
			t.Errorf("pathGlobMatch(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestKeywordMatchStopwordsAndTags(t *testing.T) {
	pg := domain.WikiPage{Title: "Notes about this page", Tags: nil}
	if kw, ok := keywordMatch(pg, "this page is about notes"); ok {
		t.Fatalf("stopword-only title matched via %q", kw)
	}
	pg = domain.WikiPage{Title: "X", Tags: []string{"CI"}}
	kw, ok := keywordMatch(pg, "the ci build is red")
	if !ok || kw != "ci" {
		t.Fatalf("tag match = %q, %v; want ci, true", kw, ok)
	}
}

// fakeDocs is a DocLister over an in-memory file map, the same shape the S15 bootstrap
// fixtures use.
type fakeDocs struct {
	files map[string]string
	err   error
}

func (f *fakeDocs) ReadFileIfExists(_ context.Context, _ ports.Creds, _ domain.RepoRef, _ string, path string) ([]byte, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	content, ok := f.files[path]
	if !ok {
		return nil, false, nil
	}
	return []byte(content), true, nil
}

func (f *fakeDocs) ListDir(_ context.Context, _ ports.Creds, _ domain.RepoRef, _ string, dir string) ([]domain.DirEntry, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []domain.DirEntry
	for p := range f.files {
		if filepath.Dir(p) == dir {
			out = append(out, domain.DirEntry{Name: filepath.Base(p), Path: p, Type: "file"})
		}
	}
	return out, nil
}

func openSecrets(t *testing.T, st *store.Store) *secrets.Store {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sec, err := secrets.Open(secrets.Options{
		Store: st, KeyPath: filepath.Join(t.TempDir(), "master.key"), Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sec
}

func connectRepo(t *testing.T, st *store.Store, sec *secrets.Store, p domain.Project) {
	t.Helper()
	ctx := context.Background()
	info, _, err := sec.Set(ctx, secrets.SetInput{
		Scope: domain.SecretScopeProject, ProjectID: p.ID, Name: "GITHUB_TOKEN",
		Value: "ghp_test", CreatedBy: p.OwnerID,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := domain.Now()
	repo := domain.Repo{
		ProjectID: p.ID, Provider: "github", Owner: "acme", Name: "payments",
		TokenSecretID: &info.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Repos().Create(ctx, &repo); err != nil {
		t.Fatal(err)
	}
}

// TestRepoFilesProviderLists is D-11: instruction files present on the default branch are
// LISTED — Injected=false, empty body, reason exactly "repo file" — never injected.
func TestRepoFilesProviderLists(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	sec := openSecrets(t, st)
	connectRepo(t, st, sec, p)

	docs := &fakeDocs{files: map[string]string{
		"AGENTS.md":                  "# Working agreements\n\nAlways run make check.",
		".cursor/rules/frontend.mdc": "Prefer CSS modules.",
	}}
	prov := NewRepoFilesProvider(st, sec, docs, nil)
	items, err := prov.Resolve(context.Background(), ports.ContextRequest{ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v, want AGENTS.md and the cursor rule", items)
	}
	for _, it := range items {
		if it.Reason != "repo file" {
			t.Errorf("Reason = %q, want %q", it.Reason, "repo file")
		}
		if it.Injected {
			t.Errorf("%s Injected = true; repo files are listed, not injected (D-11)", it.SourceRef)
		}
		if it.Body != "" {
			t.Errorf("%s Body = %q, want empty", it.SourceRef, it.Body)
		}
		if it.SourceKind != "repo_file" {
			t.Errorf("%s SourceKind = %q, want repo_file", it.SourceRef, it.SourceKind)
		}
		if it.Tokens <= 0 {
			t.Errorf("%s Tokens = %d, want > 0", it.SourceRef, it.Tokens)
		}
	}
	if items[0].SourceRef != "AGENTS.md" {
		t.Errorf("items[0] = %s, want AGENTS.md first", items[0].SourceRef)
	}
}

// TestRepoFilesProviderGraceful: no connected repo → empty; a forge outage → empty, never
// a failed enqueue.
func TestRepoFilesProviderGraceful(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	sec := openSecrets(t, st)

	// No repo connected.
	items, err := NewRepoFilesProvider(st, sec, &fakeDocs{}, nil).
		Resolve(context.Background(), ports.ContextRequest{ProjectID: p.ID})
	if err != nil || len(items) != 0 {
		t.Fatalf("no-repo resolve = %+v, %v; want empty, nil", items, err)
	}

	// Repo connected, forge down.
	connectRepo(t, st, sec, p)
	down := &fakeDocs{err: fmt.Errorf("github: 502")}
	items, err = NewRepoFilesProvider(st, sec, down, nil).
		Resolve(context.Background(), ports.ContextRequest{ProjectID: p.ID})
	if err != nil || len(items) != 0 {
		t.Fatalf("forge-down resolve = %+v, %v; want empty, nil", items, err)
	}
}
