//go:build docker

// The setup script, end to end against a real daemon: repos.setup_script (as the prep builder
// carries it into the spec) installs a tool the base image does not have, and the tool is
// usable in the container the agent will get. Plus the two states around it — an empty script
// is not a step at all, and a script that exits non-zero fails provisioning with its output
// and its exit code in the error.
//
//	go test -tags docker -run TestSetupScript -timeout 30m ./internal/module/docker/
package docker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/kernel/ports"
	runsvc "github.com/spruce/lexicode/internal/service/runs"
)

// prepFor builds a spec the way a real run does: through the workspace-prep builder, from a
// domain.Repo whose SetupScript is what the project's settings pane stores.
func prepFor(t *testing.T, script string) runsvc.Prep {
	t.Helper()
	b := &runsvc.Builder{
		Forge: func(string) (ports.ForgeProvider, error) { return prepStubForge{}, nil },
		Credential: func(id string) (ports.CredentialSource, error) {
			return prepStubSource{}, nil
		},
	}
	prep, err := b.Build(context.Background(), builderInput(script))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if prep.Spec.SetupScript != script {
		t.Fatalf("the builder dropped the setup script: %q", prep.Spec.SetupScript)
	}
	return prep
}

// TestSetupScriptInstallsATool is the loop the owner could not reach: a project declares
// "install python3" once, in settings, and the container the agent starts in has python3.
// Before this test's change there was no way to set the field at all, so the agent met a bare
// `exit 127` mid-task instead.
func TestSetupScriptInstallsATool(t *testing.T) {
	// The premise, checked in a throwaway container: the base image has no python3, and this
	// machine can reach the Debian mirror. Neither is this test's subject, so a machine
	// without egress skips rather than fails.
	probe := postureContainer(t, "proj-setup-probe")
	if code, out := execOutput(t, probe, "/bin/sh", "-c", "command -v python3"); code == 0 {
		t.Skipf("the base image already has python3 at %s; this test proves a setup script "+
			"can add something absent, and needs something absent", strings.TrimSpace(out))
	}
	skipWithoutEgress(t, probe, "https://deb.debian.org/")

	bare := fixtureRepo(t)
	sb := newTestSandbox(t, bare+":/fixtures/fixture.git:ro")
	prep := prepFor(t, "set -eux\napt-get update\napt-get install -y --no-install-recommends python3\npython3 --version\n")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	sink := newTestSink(t)

	start := time.Now()
	inst, err := sb.Prepare(ctx, prep.Spec, sink)
	if err != nil {
		t.Fatalf("Prepare with an installing setup script: %v", err)
	}
	defer destroyQuietly(t, inst)
	t.Logf("Prepare (including the apt install) took %s", time.Since(start).Round(time.Second))

	// The step is in the checklist, and its output was streamed there, not swallowed.
	if got := sink.state("setup script"); got != ports.StepOK {
		t.Errorf("setup script step = %q, want ok", got)
	}
	sink.mu.Lock()
	logged := strings.Join(sink.logs, "\n")
	sink.mu.Unlock()
	if !strings.Contains(logged, "apt-get install") {
		t.Errorf("the script's own output never reached the sink:\n%s", tail(logged, 20))
	}

	// The point: the tool is present and usable in the container the agent gets.
	if code, out := execOutput(t, inst, "/bin/sh", "-c", "command -v python3"); code != 0 {
		t.Fatalf("python3 is not on PATH after the setup script: exit %d, %s", code, out)
	}
	code, out := execOutput(t, inst, "python3", "-c",
		"import sys, json; print(json.dumps({'v': sys.version_info[0]}))")
	if code != 0 || !strings.Contains(out, `{"v": 3}`) {
		t.Fatalf("python3 could not run a program: exit %d\n%s", code, out)
	}
	t.Logf("python3 in the container: %s", strings.TrimSpace(out))
}

// TestSetupScriptEmptyIsNoStep: the empty script is a genuine no-op — provisioning skips it
// entirely rather than running an empty shell, so the checklist has no row for it.
func TestSetupScriptEmptyIsNoStep(t *testing.T) {
	bare := fixtureRepo(t)
	sb := newTestSandbox(t, bare+":/fixtures/fixture.git:ro")
	prep := prepFor(t, "")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	sink := newTestSink(t)
	inst, err := sb.Prepare(ctx, prep.Spec, sink)
	if err != nil {
		t.Fatalf("Prepare with no setup script: %v", err)
	}
	defer destroyQuietly(t, inst)

	if got := sink.state("setup script"); got != "" {
		t.Errorf("empty setup script reported step state %q; it should not be a step at all", got)
	}
	// The rest of the checklist still ran, so the absence above is a skip, not a failure.
	for _, step := range []string{"image", "container", "clone", "branch"} {
		if got := sink.state(step); got != ports.StepOK {
			t.Errorf("step %q = %q, want ok", step, got)
		}
	}
}

// TestSetupScriptFailureFailsProvisioning: the failure path a user meets when the install
// command is wrong — exactly the `command not found` / exit 127 the owner's agent hit. The run
// fails in provisioning, before the agent starts, with the script's output and exit code in
// the error the run detail shows.
func TestSetupScriptFailureFailsProvisioning(t *testing.T) {
	bare := fixtureRepo(t)
	sb := newTestSandbox(t, bare+":/fixtures/fixture.git:ro")
	prep := prepFor(t, "echo about-to-fail\nnot-a-real-command --install everything\n")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	sink := newTestSink(t)
	inst, err := sb.Prepare(ctx, prep.Spec, sink)
	if err == nil {
		destroyQuietly(t, inst)
		t.Fatal("Prepare succeeded with a setup script that exits non-zero")
	}
	msg := err.Error()
	t.Logf("provisioning error:\n%s", msg)
	if !strings.Contains(msg, "about-to-fail") || !strings.Contains(msg, "not-a-real-command") {
		t.Errorf("the failure does not carry the script's output: %s", msg)
	}
	if !strings.Contains(msg, "exit 127") {
		t.Errorf("the failure does not carry the exit code: %s", msg)
	}
	if got := sink.state("setup script"); got != ports.StepFailed {
		t.Errorf("setup script step = %q, want failed", got)
	}
}

// tail returns the last n lines, for failure messages that would otherwise dump an apt log.
func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
