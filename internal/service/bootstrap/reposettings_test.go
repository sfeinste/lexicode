package bootstrap_test

// The general repo-settings PATCH: the setup script (the field that decides whether a run can
// install its own toolchain, and had no API surface at all until now) and the branch-template
// override, with the same absent/null/value handling as its network sibling.

import (
	"context"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/kernel/store"
)

func TestUpdateRepoSettings(t *testing.T) {
	gh := newFakeGitHub(t, nil, nil)
	e := newEnv(t, gh)
	c := e.owner()
	ctx := context.Background()

	// A fresh connection has no setup script and no branch-template override.
	body := e.connect(c)
	if body["setup_script"] != "" {
		t.Errorf("fresh connect setup_script = %v, want the empty string", body["setup_script"])
	}
	if body["branch_template"] != nil {
		t.Errorf("fresh connect branch_template = %v, want null (inherit)", body["branch_template"])
	}

	// Set a script. It round-trips byte for byte — newlines and all.
	script := "set -eu\napt-get update\napt-get install -y python3\n"
	code, body := e.doJSON(c, "PATCH", "/api/v1/projects/PAY/repo",
		`{"setup_script":"set -eu\napt-get update\napt-get install -y python3\n"}`)
	if code != 200 {
		t.Fatalf("patch = %d, want 200: %v", code, body)
	}
	if body["setup_script"] != script {
		t.Errorf("setup_script = %q, want %q", body["setup_script"], script)
	}

	// An absent field is left alone: patching the branch template does not disturb the script.
	code, body = e.doJSON(c, "PATCH", "/api/v1/projects/PAY/repo",
		`{"branch_template":"{agent}/{ticket-key}"}`)
	if code != 200 {
		t.Fatalf("patch template = %d: %v", code, body)
	}
	if body["setup_script"] != script {
		t.Errorf("setup_script after a template-only patch = %q, want untouched", body["setup_script"])
	}
	if body["branch_template"] != "{agent}/{ticket-key}" {
		t.Errorf("branch_template = %v", body["branch_template"])
	}

	// The stored row is what provisioning reads, so assert it there too.
	rp, err := e.st.Repos().ByProject(ctx, e.projectID())
	if err != nil {
		t.Fatal(err)
	}
	if rp.SetupScript != script {
		t.Errorf("repos.setup_script = %q, want %q", rp.SetupScript, script)
	}

	// CRLF from a browser textarea is normalized: under /bin/sh a stray \r is a syntax error
	// on every line, and the user cannot see it in their own input.
	code, body = e.doJSON(c, "PATCH", "/api/v1/projects/PAY/repo",
		`{"setup_script":"echo one\r\necho two"}`)
	if code != 200 {
		t.Fatalf("patch crlf = %d: %v", code, body)
	}
	if body["setup_script"] != "echo one\necho two" {
		t.Errorf("crlf script = %q, want LF-normalized", body["setup_script"])
	}

	// Clearing back to empty is a real state, not a no-op: the sandbox skips the step
	// entirely when the script is empty.
	code, body = e.doJSON(c, "PATCH", "/api/v1/projects/PAY/repo", `{"setup_script":""}`)
	if code != 200 || body["setup_script"] != "" {
		t.Fatalf("clear = %d, setup_script = %v, want 200 + empty", code, body["setup_script"])
	}
	if got, err := e.st.Repos().ByProject(ctx, e.projectID()); err != nil || got.SetupScript != "" {
		t.Fatalf("repos.setup_script after clearing = %q (%v), want empty", got.SetupScript, err)
	}

	// null clears it too — there is nothing to inherit.
	if code, resp := e.doJSON(c, "PATCH", "/api/v1/projects/PAY/repo",
		`{"setup_script":"echo hi"}`); code != 200 {
		t.Fatalf("re-set = %d: %v", code, resp)
	}
	code, body = e.doJSON(c, "PATCH", "/api/v1/projects/PAY/repo", `{"setup_script":null}`)
	if code != 200 || body["setup_script"] != "" {
		t.Fatalf("null clear = %d, setup_script = %v, want 200 + empty", code, body["setup_script"])
	}

	// branch_template null reverts to inheriting the workspace default.
	code, body = e.doJSON(c, "PATCH", "/api/v1/projects/PAY/repo", `{"branch_template":null}`)
	if code != 200 || body["branch_template"] != nil {
		t.Fatalf("revert = %d, branch_template = %v, want 200 + null", code, body["branch_template"])
	}

	// Garbage is refused with a field error, never persisted.
	for _, bad := range []string{
		`{"branch_template":"   "}`,
		`{"setup_script":"` + strings.Repeat("x", maxScriptBytesForTest+1) + `"}`,
		// A NUL byte, JSON-escaped: not text a shell can read.
		`{"setup_script":"echo \u0000 hi"}`,
	} {
		code, resp := e.doJSON(c, "PATCH", "/api/v1/projects/PAY/repo", bad)
		if code != 400 {
			t.Errorf("patch %.60s = %d, want 400: %v", bad, code, resp)
		}
	}

	// GET carries the fields, so the settings pane needs no second call.
	code, status := e.doJSON(c, "GET", "/api/v1/projects/PAY/repo", "")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	repo, _ := status["repo"].(map[string]any)
	if repo["setup_script"] != "" || repo["branch_template"] != nil {
		t.Errorf("status settings = %v / %v, want empty / null",
			repo["setup_script"], repo["branch_template"])
	}

	// The mutation is audited, like its network sibling.
	entries, err := e.st.Audit().List(ctx, store.AuditFilter{Action: "repo.settings.update"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Error("no repo.settings.update audit entry was written")
	}
}

// maxScriptBytesForTest mirrors the service's cap; the test only needs to exceed it.
const maxScriptBytesForTest = 64 * 1024

// A reconnect keeps the setup script: it is a project setting, not part of the credential.
func TestReconnectKeepsSetupScript(t *testing.T) {
	e := newEnv(t, newFakeGitHub(t, nil, nil))
	c := e.owner()
	e.connect(c)

	if code, body := e.doJSON(c, "PATCH", "/api/v1/projects/PAY/repo",
		`{"setup_script":"apt-get install -y python3"}`); code != 200 {
		t.Fatalf("patch = %d: %v", code, body)
	}
	body := e.connect(c)
	if body["setup_script"] != "apt-get install -y python3" {
		t.Errorf("setup_script after reconnect = %v, want it kept", body["setup_script"])
	}
}

// No repo connected: the PATCH is a 404, not a 500 — same as the network sibling.
func TestUpdateRepoSettingsWithoutRepoIs404(t *testing.T) {
	e := newEnv(t, newFakeGitHub(t, nil, nil))
	c := e.owner()
	if code, _ := e.doJSON(c, "PATCH", "/api/v1/projects/PAY/repo",
		`{"setup_script":"echo hi"}`); code != 404 {
		t.Fatalf("patch without a repo = %d, want 404", code)
	}
}
