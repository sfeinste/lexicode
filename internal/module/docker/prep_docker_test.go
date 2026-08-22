//go:build docker

// S19 acceptance against a real daemon: a full Prepare driven by a spec the workspace-prep
// builder produced — clone, run branch, materialized files, executable commit hook, setup
// script — plus the failing-setup-script path with the script's output in the error.
package docker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	runsvc "github.com/spruce/lexicode/internal/service/runs"
)

// prepStubForge hands out the bind-mounted fixture path; the embedded nil interface panics on
// anything else, which would be the bug.
type prepStubForge struct {
	ports.ForgeProvider
}

func (prepStubForge) ID() string { return "github" }

func (prepStubForge) CloneURL(context.Context, ports.Creds, domain.RepoRef) (string, error) {
	return "file:///fixtures/fixture.git", nil
}

// prepStubSource is a healthy oauth-token source with a fixed token.
type prepStubSource struct{}

func (prepStubSource) ID() string { return "oauth-token" }
func (prepStubSource) AgentEnv(context.Context, string) (map[string]string, error) {
	return map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-docker-test"}, nil
}
func (prepStubSource) Health(context.Context) error { return nil }

func builderInput(setupScript string) runsvc.PrepInput {
	open := "open"
	return runsvc.PrepInput{
		Workspace: domain.WorkspaceSettings{
			DefaultBranch:         "main",
			DefaultBranchTemplate: "{agent}/{ticket-key}-{slug}",
			DefaultNetworkPolicy:  "allowlist",
		},
		Project: domain.Project{ID: "proj-s19", Key: "PAY"},
		Repo: domain.Repo{ProjectID: "proj-s19", Provider: "github",
			Owner: "acme", Name: "payments", NetworkPolicy: &open, SetupScript: setupScript},
		Agent: domain.Agent{
			ID: "agent-dev", Name: "Dev",
			Permissions: domain.AgentPermissions{
				ReadFiles: true, EditFiles: true, RunCommands: true, PushBranches: true,
			},
			GitAuthorName: "Dev", GitAuthorEmail: "dev@agents.lexicode.local",
		},
		Ticket: &domain.Ticket{Key: "PAY-14", Title: "Add idempotency keys"},
		Run:    domain.Run{ID: "run-s19-prep", Seq: 1},
	}
}

func TestPrepareWithBuilderSpec(t *testing.T) {
	bare := fixtureRepo(t)
	sb := newTestSandbox(t, bare+":/fixtures/fixture.git:ro")

	b := &runsvc.Builder{
		Forge: func(string) (ports.ForgeProvider, error) { return prepStubForge{}, nil },
		Credential: func(id string) (ports.CredentialSource, error) {
			return prepStubSource{}, nil
		},
	}
	prep, err := b.Build(context.Background(),
		builderInput("echo setup-ran > setup-marker.txt"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if want := "dev/PAY-14-add-idempotency-keys"; prep.Branch != want {
		t.Fatalf("branch = %q, want %q", prep.Branch, want)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	sink := newTestSink(t)
	inst, err := sb.Prepare(ctx, prep.Spec, sink)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer destroyQuietly(t, inst)

	// Clone succeeded on the run branch.
	if code, out := execOutput(t, inst, "git", "rev-parse", "--abbrev-ref", "HEAD"); code != 0 ||
		strings.TrimSpace(out) != prep.Branch {
		t.Errorf("HEAD branch = %q (exit %d), want %q", strings.TrimSpace(out), code, prep.Branch)
	}

	// Files materialized: settings.json is the permissions doc, prompt.md is present.
	rawSettings, err := inst.ReadFile(ctx, ".claude/settings.json")
	if err != nil {
		t.Fatalf("ReadFile settings.json: %v", err)
	}
	var settings struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(rawSettings, &settings); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v\n%s", err, rawSettings)
	}
	if len(settings.Permissions.Allow) == 0 {
		t.Errorf("settings.json has no allow rules:\n%s", rawSettings)
	}
	prompt, err := inst.ReadFile(ctx, ".lexicode/prompt.md")
	if err != nil || len(prompt) == 0 {
		t.Errorf("ReadFile prompt.md: %v (len %d)", err, len(prompt))
	}

	// Setup script ran in the workspace.
	if marker, err := inst.ReadFile(ctx, "setup-marker.txt"); err != nil ||
		strings.TrimSpace(string(marker)) != "setup-ran" {
		t.Errorf("setup marker = %q, %v", marker, err)
	}

	// The commit trailer machinery (D-9) is live: a commit made the way an agent makes one
	// carries the run trailer, appended by the hook — `-m` included.
	if code, out := execOutput(t, inst, "git", "commit", "--allow-empty", "-m", "test commit"); code != 0 {
		t.Fatalf("git commit failed (%d):\n%s", code, out)
	}
	if code, out := execOutput(t, inst, "git", "log", "-1", "--format=%B"); code != 0 ||
		!strings.Contains(out, "Lexicode-Run: run-s19-prep") {
		t.Errorf("commit body lacks the run trailer (exit %d):\n%s", code, out)
	}
	if code, out := execOutput(t, inst, "git", "log", "-1", "--format=%ae"); code != 0 ||
		strings.TrimSpace(out) != "dev@agents.lexicode.local" {
		t.Errorf("commit author email = %q (exit %d)", strings.TrimSpace(out), code)
	}
}

func TestPrepareFailingSetupScriptSurfacesOutput(t *testing.T) {
	bare := fixtureRepo(t)
	sb := newTestSandbox(t, bare+":/fixtures/fixture.git:ro")

	b := &runsvc.Builder{
		Forge: func(string) (ports.ForgeProvider, error) { return prepStubForge{}, nil },
		Credential: func(id string) (ports.CredentialSource, error) {
			return prepStubSource{}, nil
		},
	}
	prep, err := b.Build(context.Background(),
		builderInput("echo the-dependency-explanation; exit 3"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	sink := newTestSink(t)
	inst, err := sb.Prepare(ctx, prep.Spec, sink)
	if err == nil {
		destroyQuietly(t, inst)
		t.Fatal("Prepare with a failing setup script succeeded")
	}
	if !strings.Contains(err.Error(), "the-dependency-explanation") {
		t.Errorf("failure lacks the script's output: %v", err)
	}
	if !strings.Contains(err.Error(), "exit 3") {
		t.Errorf("failure lacks the exit code: %v", err)
	}
	if sink.state("setup script") != ports.StepFailed {
		t.Errorf("setup script step state = %q, want failed", sink.state("setup script"))
	}
}
