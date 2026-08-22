package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	kernelsecrets "github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// ---------------------------------------------------------------- stubs -----

// stubForge overrides the two methods Build calls; everything else panics via the embedded
// nil interface, which is fine — reaching it would be the bug.
type stubForge struct {
	ports.ForgeProvider
}

func (stubForge) ID() string { return "github" }

func (stubForge) CloneURL(_ context.Context, c ports.Creds, r domain.RepoRef) (string, error) {
	if c.Token == "" {
		return "https://github.test/" + r.Owner + "/" + r.Name + ".git", nil
	}
	return "https://x-access-token:" + c.Token + "@github.test/" + r.Owner + "/" + r.Name + ".git", nil
}

// stubSource is a healthy credential source with fixed env.
type stubSource struct {
	id  string
	env map[string]string
	err error
}

func (s stubSource) ID() string { return s.id }
func (s stubSource) AgentEnv(context.Context, string) (map[string]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.env, nil
}
func (s stubSource) Health(context.Context) error { return s.err }

// recordSink captures provisioning output for the hygiene test.
type recordSink struct {
	mu    sync.Mutex
	steps []string
	logs  []string
}

func (s *recordSink) Step(name string, state ports.StepState, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = append(s.steps, fmt.Sprintf("%s %s %s", name, state, detail))
}

func (s *recordSink) Log(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, line)
}

// ---------------------------------------------------------------- fixtures -----

const (
	testOAuthToken = "sk-ant-oat01-hygiene-SECRET-token-value-1234"
	testRepoToken  = "ghp_hygienePATxxxxxxxxxxxxxxxxxxxxxxxx"
)

// prepEnv is a migrated store, an open secret store and the rows Build reads.
type prepEnv struct {
	t      *testing.T
	st     *store.Store
	sec    *kernelsecrets.Store
	in     PrepInput
	repoT  string // secret id of the stored repo token
	userID string
}

func newPrepEnv(t *testing.T) *prepEnv {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "s19.db"), Logger: logger})
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

	repoTok, _, err := sec.Set(ctx, kernelsecrets.SetInput{
		Scope: domain.SecretScopeProject, ProjectID: proj.ID,
		Name: "GITHUB_TOKEN", Value: testRepoToken, CreatedBy: owner.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sec.Set(ctx, kernelsecrets.SetInput{
		Scope: domain.SecretScopeProject, ProjectID: proj.ID,
		Name: "DEPLOY_KEY", Value: "deploy-key-value", CreatedBy: owner.ID,
	}); err != nil {
		t.Fatal(err)
	}

	tokenID := repoTok.ID
	agent := domain.Agent{
		ID: domain.NewID(), ProjectID: proj.ID, Name: "Dev", Role: "developer",
		RuntimeID: "claude-code", Model: "claude-sonnet", Effort: "medium",
		Autonomy: domain.AutonomyAutoGates,
		Permissions: domain.AgentPermissions{
			ReadFiles: true, EditFiles: true, RunCommands: true, PushBranches: true,
			OpenPRs: true,
		},
		GitAuthorName: "Dev", GitAuthorEmail: "dev@agents.lexicode.local",
		MaxWallClockSeconds: 1800,
	}
	ticket := domain.Ticket{ID: domain.NewID(), ProjectID: proj.ID, Key: "PAY-14",
		Title: "Add idempotency keys to the charge endpoint"}
	run := domain.Run{ID: "run-s19-test", Seq: 7, ProjectID: proj.ID, AgentID: agent.ID,
		State: domain.RunQueued, Model: "claude-sonnet", RuntimeID: "claude-code",
		SandboxID: "docker", QueuedAt: now}

	return &prepEnv{
		t: t, st: st, sec: sec, repoT: tokenID, userID: owner.ID,
		in: PrepInput{
			Workspace: domain.WorkspaceSettings{
				DefaultBranch:         "main",
				DefaultBranchTemplate: "{agent}/{ticket-key}-{slug}",
				DefaultNetworkPolicy:  "allowlist",
			},
			Project: proj,
			Repo: domain.Repo{ProjectID: proj.ID, Provider: "github",
				Owner: "acme", Name: "payments", TokenSecretID: &tokenID},
			Agent:  agent,
			Ticket: &ticket,
			Run:    run,
		},
	}
}

func (e *prepEnv) builder() *Builder {
	return &Builder{
		Secrets:    e.sec,
		Forge:      func(string) (ports.ForgeProvider, error) { return stubForge{}, nil },
		Credential: e.credential,
		ProxyEnv: func(runID string) (map[string]string, bool) {
			u := "http://lexicode-run:proxy-token-" + runID + "@lexicode-egress:3128"
			return map[string]string{
				"HTTP_PROXY": u, "HTTPS_PROXY": u, "http_proxy": u, "https_proxy": u,
				"NO_PROXY": "localhost,127.0.0.1", "no_proxy": "localhost,127.0.0.1",
			}, true
		},
		BranchTaken: func(ctx context.Context, projectID, branch string) (bool, error) {
			return e.st.Runs().BranchInUse(ctx, projectID, branch)
		},
	}
}

func (e *prepEnv) credential(id string) (ports.CredentialSource, error) {
	if id == "oauth-token" {
		return stubSource{id: id, env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": testOAuthToken}}, nil
	}
	return nil, fmt.Errorf("credential source %q: not registered", id)
}

// ---------------------------------------------------------------- branch naming -----

func TestBranchNameDeterministicAndCollisionSafe(t *testing.T) {
	e := newPrepEnv(t)
	ctx := context.Background()
	b := e.builder()

	first, err := b.Build(ctx, e.in)
	if err != nil {
		t.Fatal(err)
	}
	want := "dev/PAY-14-add-idempotency-keys-to-the-charge"
	if first.Branch != want {
		t.Errorf("branch = %q, want %q", first.Branch, want)
	}
	again, err := b.Build(ctx, e.in)
	if err != nil {
		t.Fatal(err)
	}
	if again.Branch != first.Branch {
		t.Errorf("branch not deterministic: %q then %q", first.Branch, again.Branch)
	}
	if first.Spec.Clone.Branch != first.Branch {
		t.Errorf("CloneSpec.Branch = %q, want %q", first.Spec.Clone.Branch, first.Branch)
	}

	// Persist a run claiming the name (what S22 does); the next build gets -2, then -3.
	claim := func(branch string, seq int64) {
		r := domain.Run{ID: domain.NewID(), Seq: seq, ProjectID: e.in.Project.ID,
			AgentID: e.in.Agent.ID, State: domain.RunFailed, Model: "m",
			RuntimeID: "claude-code", SandboxID: "docker", Branch: &branch,
			QueuedAt: domain.Now()}
		if err := e.st.Runs().Create(context.Background(), &r); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.st.Agents().Create(ctx, &e.in.Agent); err != nil {
		t.Fatal(err)
	}
	claim(first.Branch, 100)
	second, err := b.Build(ctx, e.in)
	if err != nil {
		t.Fatal(err)
	}
	if second.Branch != want+"-2" {
		t.Errorf("collision branch = %q, want %q", second.Branch, want+"-2")
	}
	claim(second.Branch, 101)
	third, err := b.Build(ctx, e.in)
	if err != nil {
		t.Fatal(err)
	}
	if third.Branch != want+"-3" {
		t.Errorf("double collision branch = %q, want %q", third.Branch, want+"-3")
	}
}

func TestBranchTemplateOverrideAndTicketlessRun(t *testing.T) {
	e := newPrepEnv(t)
	b := e.builder()

	custom := "agents/{ticket-key}/{slug}"
	e.in.Repo.BranchTemplate = &custom
	p, err := b.Build(context.Background(), e.in)
	if err != nil {
		t.Fatal(err)
	}
	if want := "agents/PAY-14/add-idempotency-keys-to-the-charge"; p.Branch != want {
		t.Errorf("templated branch = %q, want %q", p.Branch, want)
	}

	e.in.Repo.BranchTemplate = nil
	e.in.Ticket = nil
	p, err = b.Build(context.Background(), e.in)
	if err != nil {
		t.Fatal(err)
	}
	if want := "dev/run-7"; p.Branch != want {
		t.Errorf("ticketless branch = %q, want %q", p.Branch, want)
	}
}

func TestSlugify(t *testing.T) {
	cases := []struct{ title, want string }{
		{"Add idempotency keys", "add-idempotency-keys"},
		{"Fix résumé parsing for naïve UTF-8 café входа", "fix-resume-parsing-for-naive-utf-8-cafe"},
		{"Ship 🚀 the 🔥 thing!!", "ship-the-thing"},
		{"修复登录页面的错误", ""},
		{"  --- ", ""},
		{"A very long ticket title that keeps going and going and definitely exceeds the bound",
			"a-very-long-ticket-title-that-keeps"},
		{"Straße überprüfen", "strasse-uberprufen"},
	}
	for _, c := range cases {
		got := slugify(c.title, maxSlugLen)
		if got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.title, got, c.want)
		}
		if len(got) > maxSlugLen {
			t.Errorf("slugify(%q) length %d exceeds %d", c.title, len(got), maxSlugLen)
		}
	}
}

func TestSanitizeRef(t *testing.T) {
	cases := []struct{ in, want string }{
		{"dev/PAY-14-fix", "dev/PAY-14-fix"},
		{"dev//PAY..14", "dev/PAY-14"},
		{"dev/.hidden/x.lock", "dev/hidden/x"},
		{"a b~c^d:e", "a-b-c-d-e"},
		{"-lead/trail-", "lead/trail"},
	}
	for _, c := range cases {
		if got := sanitizeRef(c.in); got != c.want {
			t.Errorf("sanitizeRef(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------- settings.json -----

func TestClaudeSettingsFromPermissions(t *testing.T) {
	reviewer := domain.AgentPermissions{
		ReadFiles: true, EditFiles: false, RunCommands: true,
		PushBranches: false, SubmitReviews: true,
	}
	got, err := claudeSettings(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("reviewer settings.json:\n%s", got)
	var doc struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"Edit", "Write", "NotebookEdit"} {
		if !contains(doc.Permissions.Deny, tool) {
			t.Errorf("reviewer (edit_files=false): %s missing from deny: %v", tool, doc.Permissions.Deny)
		}
		if contains(doc.Permissions.Allow, tool) {
			t.Errorf("reviewer: %s must not be allowed", tool)
		}
	}
	if !contains(doc.Permissions.Deny, "Bash(git push:*)") {
		t.Errorf("reviewer (push_branches=false): git push not denied: %v", doc.Permissions.Deny)
	}
	if !contains(doc.Permissions.Allow, "Read") || !contains(doc.Permissions.Allow, "Bash") {
		t.Errorf("reviewer: Read/Bash should be allowed: %v", doc.Permissions.Allow)
	}

	dev := domain.AgentPermissions{
		ReadFiles: true, EditFiles: true, RunCommands: true, PushBranches: true, OpenPRs: true,
	}
	got, err = claudeSettings(dev)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("dev settings.json:\n%s", got)
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"Read", "Grep", "Glob", "Edit", "Write", "NotebookEdit", "Bash"} {
		if !contains(doc.Permissions.Allow, tool) {
			t.Errorf("dev: %s missing from allow: %v", tool, doc.Permissions.Allow)
		}
	}
	if len(doc.Permissions.Deny) != 0 {
		t.Errorf("dev: deny should be empty, got %v", doc.Permissions.Deny)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- env assembly -----

func TestEnvAssembly(t *testing.T) {
	for _, tc := range []struct {
		policy    string
		wantProxy bool
	}{
		{"none", true}, {"allowlist", true}, {"open", false},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			e := newPrepEnv(t)
			policy := tc.policy
			e.in.Repo.NetworkPolicy = &policy
			p, err := e.builder().Build(context.Background(), e.in)
			if err != nil {
				t.Fatal(err)
			}
			env := p.Spec.Env

			_, hasProxy := env["HTTPS_PROXY"]
			if hasProxy != tc.wantProxy {
				t.Errorf("policy %s: HTTPS_PROXY present = %v, want %v", tc.policy, hasProxy, tc.wantProxy)
			}
			if _, lower := env["https_proxy"]; lower != tc.wantProxy {
				t.Errorf("policy %s: https_proxy present = %v, want %v", tc.policy, lower, tc.wantProxy)
			}

			// Project secrets are injected by name — the repo token among them (its secret
			// name is the conventional env var, see service/bootstrap).
			if env["DEPLOY_KEY"] != "deploy-key-value" {
				t.Errorf("DEPLOY_KEY = %q", env["DEPLOY_KEY"])
			}
			if env["GITHUB_TOKEN"] != testRepoToken {
				t.Errorf("GITHUB_TOKEN missing from env")
			}

			// The OAuth token: present, and its value appears exactly once across the env.
			if env["CLAUDE_CODE_OAUTH_TOKEN"] != testOAuthToken {
				t.Errorf("CLAUDE_CODE_OAUTH_TOKEN = %q", env["CLAUDE_CODE_OAUTH_TOKEN"])
			}
			count := 0
			for _, v := range env {
				count += strings.Count(v, testOAuthToken)
			}
			if count != 1 {
				t.Errorf("token appears %d times in env, want exactly 1", count)
			}

			for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
				"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
				if env[k] == "" {
					t.Errorf("%s missing", k)
				}
			}
			if env["LEXICODE_RUN_ID"] != e.in.Run.ID {
				t.Errorf("LEXICODE_RUN_ID = %q", env["LEXICODE_RUN_ID"])
			}

			if tc.policy == "allowlist" && p.Spec.Network.Mode != ports.NetworkAllowlist {
				t.Errorf("network mode = %q", p.Spec.Network.Mode)
			}
		})
	}
}

// TestEnvAssemblySkipsReservedCredentialNames: a stored secret named like the credential
// variables must not fight the credential source — the source is the single authority.
func TestEnvAssemblySkipsReservedCredentialNames(t *testing.T) {
	e := newPrepEnv(t)
	ctx := context.Background()
	if _, _, err := e.sec.Set(ctx, kernelsecrets.SetInput{
		Scope: domain.SecretScopeWorkspace, Name: "CLAUDE_CODE_OAUTH_TOKEN",
		Value: "sk-ant-oat01-stored-in-secrets", CreatedBy: e.userID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.sec.Set(ctx, kernelsecrets.SetInput{
		Scope: domain.SecretScopeProject, ProjectID: e.in.Project.ID,
		Name: "ANTHROPIC_API_KEY", Value: "sk-ant-api03-stored", CreatedBy: e.userID,
	}); err != nil {
		t.Fatal(err)
	}
	p, err := e.builder().Build(ctx, e.in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Spec.Env["CLAUDE_CODE_OAUTH_TOKEN"] != testOAuthToken {
		t.Errorf("credential source value was overridden: %q", p.Spec.Env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
	if _, ok := p.Spec.Env["ANTHROPIC_API_KEY"]; ok {
		t.Error("stored ANTHROPIC_API_KEY leaked into env alongside the OAuth token")
	}
}

func TestBuildFailsWithoutCredentials(t *testing.T) {
	e := newPrepEnv(t)
	b := e.builder()
	b.Credential = func(id string) (ports.CredentialSource, error) {
		return stubSource{id: id, err: fmt.Errorf("no token configured")}, nil
	}
	_, err := b.Build(context.Background(), e.in)
	if err == nil || !strings.Contains(err.Error(), "oauth-token") || !strings.Contains(err.Error(), "env") {
		t.Errorf("Build without credentials = %v, want an error naming both sources", err)
	}
}

// ---------------------------------------------------------------- files -----

func TestFilesMaterialized(t *testing.T) {
	e := newPrepEnv(t)
	p, err := e.builder().Build(context.Background(), e.in)
	if err != nil {
		t.Fatal(err)
	}
	files := p.Spec.Files
	for _, path := range []string{settingsPath, mcpConfigPath, promptPath,
		commitTemplatePath, commitHookPath} {
		if len(files[path]) == 0 {
			t.Errorf("file %s missing or empty", path)
		}
	}
	mcp := string(files[mcpConfigPath])
	if !strings.Contains(mcp, "http://host.docker.internal/mcp/"+PlaceholderRunToken) {
		t.Errorf("mcp.json lacks the placeholder endpoint:\n%s", mcp)
	}
	var doc map[string]any
	if err := json.Unmarshal(files[mcpConfigPath], &doc); err != nil {
		t.Errorf("mcp.json is not valid JSON: %v", err)
	}
	hook := string(files[commitHookPath])
	if !strings.HasPrefix(hook, "#!/bin/sh") ||
		!strings.Contains(hook, "Lexicode-Run: "+e.in.Run.ID) {
		t.Errorf("commit-msg hook wrong:\n%s", hook)
	}
	if !strings.Contains(string(files[commitTemplatePath]), "Lexicode-Run: "+e.in.Run.ID) {
		t.Errorf("commit template lacks the trailer:\n%s", files[commitTemplatePath])
	}
	if env := p.Spec.Env; env["GIT_CONFIG_KEY_1"] != "core.hooksPath" ||
		env["GIT_CONFIG_VALUE_1"] != "/workspace/.lexicode/hooks" {
		t.Errorf("hooksPath env wiring wrong: %q=%q", env["GIT_CONFIG_KEY_1"], env["GIT_CONFIG_VALUE_1"])
	}
}

// ---------------------------------------------------------------- token hygiene -----

// TestTokenHygiene is the S19 acceptance grep: after a provisioning flow whose container
// echoed its environment and whose clone failed with the URL in the error, the run's
// activities, the captured log lines and an API-shaped serialization of the run contain
// neither the OAuth token nor the repo token.
func TestTokenHygiene(t *testing.T) {
	e := newPrepEnv(t)
	ctx := context.Background()
	p, err := e.builder().Build(ctx, e.in)
	if err != nil {
		t.Fatal(err)
	}

	redactor := &Redactor{}
	redactor.Add(p.SecretValues...)

	inner := &recordSink{}
	sink := NewRedactingSink(inner, redactor)

	// What a hostile-worst-case provisioning stream looks like: the env echoed, the
	// credential-bearing clone URL in a failure detail, the setup script printing the token.
	sink.Log("+ env")
	sink.Log("CLAUDE_CODE_OAUTH_TOKEN=" + testOAuthToken)
	sink.Log("GITHUB_TOKEN=" + testRepoToken)
	sink.Step("clone", ports.StepFailed,
		"fatal: unable to access '"+p.Spec.Clone.URL+"': 403")
	sink.Log("setup: curl -H 'Authorization: Bearer " + testOAuthToken + "'")

	// The failure error S17 produces carries the script output; S22 stores it on the run
	// after cleaning. Simulate exactly that.
	failure := fmt.Errorf("setup script failed (exit 1):\nToken is %s", testOAuthToken)
	run := e.in.Run
	msg := redactor.Clean(failure.Error())
	run.ErrorMessage = msg

	// Persist activities the way the provisioning recorder will: one row per step/log line.
	if err := e.st.Agents().Create(ctx, &e.in.Agent); err != nil {
		t.Fatal(err)
	}
	branch := p.Branch
	run.Branch = &branch
	if err := e.st.Runs().Create(ctx, &run); err != nil {
		t.Fatal(err)
	}
	for _, line := range append(append([]string{}, inner.steps...), inner.logs...) {
		a := domain.Activity{RunID: run.ID, Type: domain.ActivitySystem, Level: 2,
			Title: line, CreatedAt: domain.Now()}
		if err := e.st.Activities().AppendNext(ctx, &a); err != nil {
			t.Fatal(err)
		}
	}

	// The grep. Activities as stored, the captured log stream, and the run serialized the
	// way an API response would carry it.
	stored, err := e.st.Activities().ForRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var corpus strings.Builder
	actJSON, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	corpus.Write(actJSON)
	corpus.WriteString(strings.Join(inner.logs, "\n"))
	corpus.WriteString(strings.Join(inner.steps, "\n"))
	storedRun, err := e.st.Runs().ByID(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	runJSON, err := json.Marshal(storedRun)
	if err != nil {
		t.Fatal(err)
	}
	corpus.Write(runJSON)

	for name, secret := range map[string]string{
		"oauth token": testOAuthToken,
		"repo token":  testRepoToken,
		"clone URL":   p.Spec.Clone.URL,
	} {
		if strings.Contains(corpus.String(), secret) {
			t.Errorf("the %s leaked into activities/logs/API fixtures", name)
		}
	}
	if !strings.Contains(corpus.String(), redactedPlaceholder) {
		t.Error("expected the placeholder to appear where secrets were scrubbed")
	}
}
