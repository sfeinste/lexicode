package kernel_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestRevokeSessionsSignsTheUserOutAndAudits: the S37 owner action. The member's live session
// dies immediately, the endpoint is owner-only, and the revocation lands in the audit log
// attributed to the acting owner.
func TestRevokeSessionsSignsTheUserOutAndAudits(t *testing.T) {
	e := newS06Env(t)

	owner := e.client()
	resp := e.postJSON(owner, "/api/v1/auth/setup",
		`{"email":"ada@example.com","display_name":"Ada","password":"correct horse"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// A member with a live session, via invite.
	resp = e.postJSON(owner, "/api/v1/invites", `{}`)
	var inv struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&inv); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	member := e.client()
	token := strings.TrimPrefix(inv.Path, "/invite/")
	resp = e.postJSON(member, "/api/v1/invites/"+token+"/redeem",
		`{"email":"mo@example.com","display_name":"Mo","password":"correct horse"}`)
	var memberBody struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&memberBody); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	me := func(c *http.Client) int {
		resp, err := c.Get(e.srv.URL + "/api/v1/auth/me") //nolint:noctx // test client
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if me(member) != http.StatusOK {
		t.Fatal("member session should be live before revocation")
	}

	del := func(c *http.Client, id string) (int, string) {
		req, err := http.NewRequest("DELETE", e.srv.URL+"/api/v1/users/"+id+"/sessions", nil) //nolint:noctx // test client
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// A member cannot revoke anyone's sessions.
	if code, _ := del(member, memberBody.ID); code != http.StatusForbidden {
		t.Fatalf("member revoke = %d, want 403", code)
	}

	// The owner revokes the member's sessions; the member's cookie is dead on the next call.
	code, body := del(owner, memberBody.ID)
	if code != http.StatusOK || !strings.Contains(body, `"revoked":1`) {
		t.Fatalf("owner revoke = %d %s, want 200 with revoked:1", code, body)
	}
	if me(member) != http.StatusUnauthorized {
		t.Fatal("member session survived the revocation")
	}

	// The revocation is in the audit log, attributed to the owner.
	resp, err := owner.Get(e.srv.URL + "/api/v1/audit?action=user.sessions.revoke") //nolint:noctx // test client
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), `"actor_kind":"human"`) ||
		!strings.Contains(string(raw), `"target_id":"`+memberBody.ID+`"`) {
		t.Fatalf("audit entry missing or misattributed: %s", raw)
	}

	// Unknown user: 404.
	if code, _ := del(owner, "no-such-user"); code != http.StatusNotFound {
		t.Fatalf("revoke unknown user = %d, want 404", code)
	}
}
