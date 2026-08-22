package bootstrap_test

// S18: the network-settings endpoint — tri-state policy PATCH (override / inherit / leave
// alone), allowlist normalization and validation, and the workspace default riding on every
// repo body for the settings pane's inheritance line.

import (
	"testing"
)

func TestUpdateRepoNetworkSettings(t *testing.T) {
	gh := newFakeGitHub(t, nil, nil)
	e := newEnv(t, gh)
	c := e.owner()

	// Connect: the repo body carries the network triple from day one — no override, the
	// workspace default visible (migration 0001 seeds it; migration 0005 sets it to 'open').
	body := e.connect(c)
	if body["network_policy"] != nil {
		t.Errorf("fresh connect network_policy = %v, want null (inherit)", body["network_policy"])
	}
	if body["workspace_network_policy"] != "open" {
		t.Errorf("workspace_network_policy = %v, want the seeded workspace default", body["workspace_network_policy"])
	}

	// Override the policy and set an allowlist; entries are trimmed, lowercased, deduped.
	code, body := e.doJSON(c, "PATCH", "/api/v1/projects/PAY/repo/network",
		`{"network_policy":"allowlist","network_allowlist":[" Registry.NPMJS.org ","*.pypi.org","registry.npmjs.org",""]}`)
	if code != 200 {
		t.Fatalf("patch = %d, want 200: %v", code, body)
	}
	if body["network_policy"] != "allowlist" {
		t.Errorf("network_policy = %v, want allowlist", body["network_policy"])
	}
	got, _ := body["network_allowlist"].([]any)
	if len(got) != 2 || got[0] != "registry.npmjs.org" || got[1] != "*.pypi.org" {
		t.Errorf("network_allowlist = %v, want [registry.npmjs.org *.pypi.org]", got)
	}

	// An absent field leaves the other unchanged.
	code, body = e.doJSON(c, "PATCH", "/api/v1/projects/PAY/repo/network",
		`{"network_policy":"none"}`)
	if code != 200 {
		t.Fatalf("patch policy only = %d: %v", code, body)
	}
	if got, _ := body["network_allowlist"].([]any); len(got) != 2 {
		t.Errorf("allowlist after policy-only patch = %v, want untouched", body["network_allowlist"])
	}

	// Explicit null reverts to inherit.
	code, body = e.doJSON(c, "PATCH", "/api/v1/projects/PAY/repo/network",
		`{"network_policy":null}`)
	if code != 200 || body["network_policy"] != nil {
		t.Fatalf("revert = %d, network_policy = %v, want 200 + null", code, body["network_policy"])
	}

	// Garbage is refused with a field error, never persisted.
	for _, bad := range []string{
		`{"network_policy":"unbounded"}`,
		`{"network_allowlist":["https://registry.npmjs.org"]}`,
		`{"network_allowlist":["*"]}`,
		`{"network_allowlist":["localhost"]}`,
	} {
		code, resp := e.doJSON(c, "PATCH", "/api/v1/projects/PAY/repo/network", bad)
		if code != 400 {
			t.Errorf("patch %s = %d, want 400: %v", bad, code, resp)
		}
	}

	// The GET status carries the same fields, so the settings pane needs no second call.
	code, status := e.doJSON(c, "GET", "/api/v1/projects/PAY/repo", "")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	repo, _ := status["repo"].(map[string]any)
	if repo["network_policy"] != nil || repo["workspace_network_policy"] != "open" {
		t.Errorf("status network fields = %v / %v, want null / open",
			repo["network_policy"], repo["workspace_network_policy"])
	}
	if got, _ := repo["network_allowlist"].([]any); len(got) != 2 {
		t.Errorf("status allowlist = %v, want the stored 2 entries", repo["network_allowlist"])
	}
}
