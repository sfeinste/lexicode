package bootstrap_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/spruce/lexicode/internal/kernel/store"
)

// TestRotateTokenVerifiesBeforeReplacing is DoD item 5: a bad new token is a 400 and the old
// token stays stored; a good one replaces the secret in place.
func TestRotateTokenVerifiesBeforeReplacing(t *testing.T) {
	gh := newFakeGitHub(t, fixtureFiles(), nil)
	e := newEnv(t, gh)
	c := e.owner()
	ctx := context.Background()
	e.connect(c) // stores ghp_fixturetoken1234567890 as GITHUB_TOKEN

	readToken := func() string {
		t.Helper()
		rp, err := e.st.Repos().ByProject(ctx, e.projectID())
		if err != nil || rp.TokenSecretID == nil {
			t.Fatalf("repo/token: %v %+v", err, rp)
		}
		val, err := e.sec.Get(ctx, *rp.TokenSecretID)
		if err != nil {
			t.Fatal(err)
		}
		return val
	}
	if got := readToken(); got != "ghp_fixturetoken1234567890" {
		t.Fatalf("stored token = %q", got)
	}

	// Bad new token: the forge rejects it, the rotate answers 400, the old token is kept.
	gh.rejectToken = "ghp_badnewtoken0000000000"
	status, body := e.doJSON(c, "POST", "/api/v1/projects/PAY/repo/token",
		`{"token":"ghp_badnewtoken0000000000"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("rotate with bad token = %d, want 400: %v", status, body)
	}
	if got := readToken(); got != "ghp_fixturetoken1234567890" {
		t.Fatalf("old token was replaced by a failed rotate: %q", got)
	}

	// Good new token: verified, then replaced in place.
	status, body = e.doJSON(c, "POST", "/api/v1/projects/PAY/repo/token",
		`{"token":"ghp_newtoken9876543210xyz"}`)
	if status != http.StatusOK {
		t.Fatalf("rotate = %d: %v", status, body)
	}
	if got := readToken(); got != "ghp_newtoken9876543210xyz" {
		t.Fatalf("token after rotate = %q, want the new one", got)
	}

	// The rotation is audited on the project.
	entries, err := e.st.Audit().List(ctx, store.AuditFilter{Action: "repo.token.rotate"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("repo.token.rotate audit entries = %d, want 1", len(entries))
	}
}

// TestRotateTokenRequiresAConnectedRepo: no repo, 404.
func TestRotateTokenWithoutRepoIs404(t *testing.T) {
	e := newEnv(t, newFakeGitHub(t, fixtureFiles(), nil))
	c := e.owner()
	status, _ := e.doJSON(c, "POST", "/api/v1/projects/PAY/repo/token", `{"token":"ghp_x"}`)
	if status != http.StatusNotFound {
		t.Fatalf("rotate without repo = %d, want 404", status)
	}
}
