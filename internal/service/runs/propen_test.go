package runs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	kernelsecrets "github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// refusingForge answers OpenPullRequest with a fixed error, the way the adapter surfaces a
// forge refusal: wrapErr("open pull request", repo, <go-github *ErrorResponse>).
type refusingForge struct {
	ports.ForgeProvider
	err error
}

func (refusingForge) ID() string { return "github" }

func (f refusingForge) OpenPullRequest(context.Context, ports.Creds, domain.RepoRef,
	domain.Actor, ports.PRSpec,
) (domain.PullRequest, error) {
	return domain.PullRequest{}, f.err
}

// openerEnv is a migrated store holding exactly the rows PROpener reads.
type openerEnv struct {
	t   *testing.T
	st  *store.Store
	sec *kernelsecrets.Store
	run domain.Run
}

func newOpenerEnv(t *testing.T, branch string) *openerEnv {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "propen.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	sec, err := kernelsecrets.Open(kernelsecrets.Options{
		Store: st, KeyPath: filepath.Join(dir, "master.key"), Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := domain.Now()
	owner := domain.User{ID: domain.NewID(), Email: "o@example.com", DisplayName: "O",
		PasswordHash: "x", Role: domain.RoleOwner, AvatarColor: "#000", CreatedAt: now}
	if err := st.Users().Create(ctx, &owner); err != nil {
		t.Fatal(err)
	}
	proj := domain.Project{ID: domain.NewID(), Key: "PAY", Name: "Payments", Color: "#fff",
		OwnerID: owner.ID, CreatedAt: now, UpdatedAt: now}
	if err := st.Projects().Create(ctx, &proj); err != nil {
		t.Fatal(err)
	}
	tok, _, err := sec.Set(ctx, kernelsecrets.SetInput{
		Scope: domain.SecretScopeProject, ProjectID: proj.ID,
		Name: "GITHUB_TOKEN", Value: "ghp_xxx", CreatedBy: owner.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	tokenID := tok.ID
	base := "main"
	repo := domain.Repo{ProjectID: proj.ID, Provider: "github", Owner: "acme",
		Name: "payments", DefaultBranch: &base, TokenSecretID: &tokenID,
		CreatedAt: now, UpdatedAt: now}
	if err := st.Repos().Create(ctx, &repo); err != nil {
		t.Fatal(err)
	}
	agent := domain.Agent{
		ID: domain.NewID(), ProjectID: proj.ID, Name: "Dev", Role: "developer",
		Color: "#888888", RuntimeID: "claude-code", Model: "fake", Effort: "medium",
		Autonomy: domain.AutonomyAuto,
		Permissions: domain.AgentPermissions{
			ReadFiles: true, EditFiles: true, RunCommands: true,
			PushBranches: true, OpenPRs: true,
		},
		GitAuthorName: "Dev", GitAuthorEmail: "dev@agents.lexicode.local",
		ConcurrencyCap: 1, MaxWallClockSeconds: 1800, MaxSteps: 50, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Agents().Create(ctx, &agent); err != nil {
		t.Fatal(err)
	}
	seq, err := st.Runs().NextSeq(ctx, proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: domain.NewID(), Seq: seq, ProjectID: proj.ID, AgentID: agent.ID,
		State: domain.RunCompleted, Autonomy: domain.AutonomyAuto, Model: "fake",
		Effort: "medium", Prompt: "p", RuntimeID: "claude-code", SandboxID: "docker",
		Branch: &branch, QueuedAt: now,
	}
	if err := st.Runs().Create(ctx, &run); err != nil {
		t.Fatal(err)
	}

	return &openerEnv{t: t, st: st, sec: sec, run: run}
}

func (e *openerEnv) opener(err error) *PROpener {
	return &PROpener{
		Store:   e.st,
		Secrets: e.sec,
		Forge: func(string) (ports.ForgeProvider, error) {
			return refusingForge{err: err}, nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func (e *openerEnv) activities() []domain.Activity {
	e.t.Helper()
	acts, err := e.st.Activities().ForRun(context.Background(), e.run.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return acts
}

// TestOpenForRunNoHeadBranchIsNotAnError is the S24 follow-up-run regression. A run always
// gets a fresh branch, so a run that worked on an existing pull request's branch has nothing
// pushed under its own name and the forge answers 422. That is not a failure of an otherwise
// successful run: nothing was there to open. The run must end with a plain, level-2 system
// activity — no error, no ERROR log line for the user to worry about.
func TestOpenForRunNoHeadBranchIsNotAnError(t *testing.T) {
	const branch = "dev/pay-14-idempotency-keys-2"
	cases := []struct {
		name      string
		forgeErr  error
		wantTitle string
	}{
		{
			name: "the fake forge's spelling",
			forgeErr: fmt.Errorf("github: open pull request for acme/payments: "+
				"POST http://127.0.0.1:7797/repos/acme/payments/pulls: 422 "+
				"Validation Failed: head branch %s does not exist []", branch),
			wantTitle: "nothing was pushed to",
		},
		{
			name: "go-github's structured validation error",
			forgeErr: errors.New("github: open pull request for acme/payments: " +
				"POST https://api.github.com/repos/acme/payments/pulls: 422 Validation Failed " +
				"[{Resource:PullRequest Field:head Code:invalid}]"),
			wantTitle: "nothing was pushed to",
		},
		{
			name: "a pull request already exists",
			forgeErr: errors.New("github: open pull request for acme/payments: " +
				"POST https://api.github.com/repos/acme/payments/pulls: 422 Validation Failed " +
				"[{Resource:PullRequest Code:custom Message:A pull request already exists for acme:dev/x.}]"),
			wantTitle: "one already exists for",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newOpenerEnv(t, branch)
			opened, err := e.opener(c.forgeErr).OpenForRun(context.Background(), e.run)
			if err != nil {
				t.Fatalf("OpenForRun returned an error, want a no-op: %v", err)
			}
			if opened {
				t.Fatal("OpenForRun reported a pull request opened")
			}
			acts := e.activities()
			if len(acts) != 1 {
				t.Fatalf("got %d activities, want exactly one system note: %+v", len(acts), acts)
			}
			a := acts[0]
			if a.Type != domain.ActivitySystem || a.Level != 2 {
				t.Errorf("activity type/level = %s/%d, want system/2", a.Type, a.Level)
			}
			if !strings.Contains(a.Title, c.wantTitle) || !strings.Contains(a.Title, branch) {
				t.Errorf("activity title = %q, want it to say %q and name %s",
					a.Title, c.wantTitle, branch)
			}
			if a.OK != nil && !*a.OK {
				t.Errorf("the note is marked failed: %+v", a)
			}
			t.Logf("system activity: %s\n  payload: %s", a.Title, a.Payload)
		})
	}
}

// TestOpenForRunRealFailureStaysAnError: the classification is narrow. Anything that is not
// one of the "nothing to open" 422s still reaches the scheduler as an error, which is what
// puts it in the run's transcript as a failure.
func TestOpenForRunRealFailureStaysAnError(t *testing.T) {
	for _, forgeErr := range []error{
		errors.New("github: open pull request for acme/payments: " +
			"POST https://api.github.com/repos/acme/payments/pulls: 403 Resource not accessible []"),
		errors.New("github: open pull request for acme/payments: " +
			"POST https://api.github.com/repos/acme/payments/pulls: 500 Internal Server Error []"),
		// A 422 for a reason that is NOT "nothing to open".
		errors.New("github: open pull request for acme/payments: " +
			"POST https://api.github.com/repos/acme/payments/pulls: 422 Validation Failed " +
			"[{Resource:PullRequest Field:base Code:invalid}]"),
	} {
		e := newOpenerEnv(t, "dev/pay-14-idempotency-keys")
		opened, err := e.opener(forgeErr).OpenForRun(context.Background(), e.run)
		if err == nil {
			t.Errorf("OpenForRun(%v) returned no error, want it surfaced", forgeErr)
		}
		if opened {
			t.Error("OpenForRun reported a pull request opened")
		}
		if acts := e.activities(); len(acts) != 0 {
			t.Errorf("a real failure wrote %d activities here; the scheduler writes that one: %+v",
				len(acts), acts)
		}
	}
}
