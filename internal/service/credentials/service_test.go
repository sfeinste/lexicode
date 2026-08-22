package credentials_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	kernelsecrets "github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
	credentialsmod "github.com/spruce/lexicode/internal/module/credentials"
	credsvc "github.com/spruce/lexicode/internal/service/credentials"
)

// env wires store + auth + audit + the secret store + the credentials module's two sources +
// this service's routes, served the way cmd/lexicode serves them.
type env struct {
	t    *testing.T
	srv  *httptest.Server
	home string
}

func newEnv(t *testing.T, goos string, envVars map[string]string) *env {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "s19.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	sec, err := kernelsecrets.Open(kernelsecrets.Options{
		Store: st, KeyPath: filepath.Join(dir, "master.key"), Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	mod := credentialsmod.New(credentialsmod.Options{
		Secrets: sec,
		LookupEnv: func(k string) (string, bool) {
			v, ok := envVars[k]
			return v, ok
		},
	})

	mux := httpx.NewMux(httpx.Options{Logger: logger})
	authSvc := auth.New(auth.Options{Store: st, Logger: logger})
	authSvc.Routes(mux)
	auditW := audit.New(audit.Options{Store: st, Logger: logger})

	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	credsvc.New(credsvc.Options{
		Secrets: sec, Audit: auditW, Logger: logger,
		OAuth: mod.OAuth(), Env: mod.Env(),
		SecretName: credentialsmod.OAuthSecretName,
		GOOS:       goos,
		Home:       func() (string, error) { return home, nil },
	}).Routes(mux, authSvc)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, home: home}
}

func (e *env) owner() *http.Client {
	e.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	c := &http.Client{Jar: jar}
	status, _ := e.do(c, "POST", "/api/v1/auth/setup",
		`{"email":"ada@example.com","display_name":"Ada","password":"correct horse"}`)
	if status != http.StatusCreated {
		e.t.Fatalf("setup = %d, want 201", status)
	}
	return c
}

func (e *env) do(c *http.Client, method, path, body string) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	return v
}

func TestOAuthTokenLifecycleOverHTTP(t *testing.T) {
	const token = "sk-ant-oat01-abcdefghijklmnop"
	e := newEnv(t, "darwin", nil)
	c := e.owner()

	// Unconfigured: unhealthy with the setup-token instruction; import hidden on macOS.
	status, raw := e.do(c, "GET", "/api/v1/workspace/credentials", "")
	if status != http.StatusOK {
		t.Fatalf("GET credentials = %d\n%s", status, raw)
	}
	body := decode(t, raw)
	oauth := body["oauth_token"].(map[string]any)
	if oauth["configured"] != false || oauth["healthy"] != false {
		t.Errorf("unconfigured oauth = %v", oauth)
	}
	if msg := oauth["message"].(string); !strings.Contains(msg, "claude setup-token") {
		t.Errorf("message = %q, want the setup-token instruction", msg)
	}
	if imp := body["import"].(map[string]any); imp["available"] != false {
		t.Errorf("import available on darwin: %v", imp)
	}

	// A malformed paste is rejected on the field.
	status, raw = e.do(c, "PUT", "/api/v1/workspace/credentials/oauth-token",
		`{"token":"two words"}`)
	if status != http.StatusBadRequest {
		t.Errorf("malformed set = %d, want 400\n%s", status, raw)
	}

	// Set: healthy, and the token value appears in no response.
	status, raw = e.do(c, "PUT", "/api/v1/workspace/credentials/oauth-token",
		`{"token":"`+token+`"}`)
	if status != http.StatusOK {
		t.Fatalf("set token = %d\n%s", status, raw)
	}
	if bytes.Contains(raw, []byte(token)) {
		t.Error("the response echoed the token")
	}
	body = decode(t, raw)
	oauth = body["oauth_token"].(map[string]any)
	if oauth["configured"] != true || oauth["healthy"] != true || oauth["message"] != "" {
		t.Errorf("configured oauth = %v", oauth)
	}

	// Clear: back to unconfigured; clearing again is 404.
	if status, raw = e.do(c, "DELETE", "/api/v1/workspace/credentials/oauth-token", ""); status != http.StatusOK {
		t.Fatalf("clear = %d\n%s", status, raw)
	}
	if status, _ = e.do(c, "DELETE", "/api/v1/workspace/credentials/oauth-token", ""); status != http.StatusNotFound {
		t.Errorf("second clear = %d, want 404", status)
	}
}

func TestEnvFallbackHealthSurfaced(t *testing.T) {
	e := newEnv(t, "darwin", map[string]string{"ANTHROPIC_API_KEY": "sk-ant-api03-x"})
	c := e.owner()
	_, raw := e.do(c, "GET", "/api/v1/workspace/credentials", "")
	body := decode(t, raw)
	envSrc := body["env"].(map[string]any)
	if envSrc["healthy"] != true {
		t.Errorf("env source with ANTHROPIC_API_KEY set should be healthy: %v", envSrc)
	}
	if bytes.Contains(raw, []byte("sk-ant-api03-x")) {
		t.Error("the env credential value leaked into the status response")
	}
}

func TestImportIsLinuxOnlyAndReadsAtClickTime(t *testing.T) {
	// Non-Linux: 409 with the Keychain explanation, checked at request time.
	e := newEnv(t, "darwin", nil)
	c := e.owner()
	status, raw := e.do(c, "POST", "/api/v1/workspace/credentials/import", "")
	if status != http.StatusConflict || !strings.Contains(string(raw), "Keychain") {
		t.Errorf("darwin import = %d %s, want 409 naming the Keychain", status, raw)
	}

	// Linux, no file yet: 404 with the fix.
	e = newEnv(t, "linux", nil)
	c = e.owner()
	status, raw = e.do(c, "POST", "/api/v1/workspace/credentials/import", "")
	if status != http.StatusNotFound {
		t.Errorf("linux import without file = %d\n%s", status, raw)
	}

	// The file appears AFTER boot — the click-time read finds it (never cached at startup).
	const token = "sk-ant-oat01-imported-token"
	dir := filepath.Join(e.home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"),
		[]byte(`{"claudeAiOauth":{"accessToken":"`+token+`","refreshToken":"r"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	status, raw = e.do(c, "POST", "/api/v1/workspace/credentials/import", "")
	if status != http.StatusOK {
		t.Fatalf("linux import = %d\n%s", status, raw)
	}
	if bytes.Contains(raw, []byte(token)) {
		t.Error("the import response echoed the token")
	}
	body := decode(t, raw)
	oauth := body["oauth_token"].(map[string]any)
	if oauth["configured"] != true || oauth["healthy"] != true {
		t.Errorf("post-import oauth = %v", oauth)
	}
}

func TestCredentialsRoutesAreOwnerOnly(t *testing.T) {
	e := newEnv(t, "linux", nil)
	_ = e.owner() // workspace exists; the anonymous client below has no session
	jar, _ := cookiejar.New(nil)
	anon := &http.Client{Jar: jar}
	for _, rt := range []struct{ method, path string }{
		{"GET", "/api/v1/workspace/credentials"},
		{"PUT", "/api/v1/workspace/credentials/oauth-token"},
		{"DELETE", "/api/v1/workspace/credentials/oauth-token"},
		{"POST", "/api/v1/workspace/credentials/import"},
	} {
		status, _ := e.do(anon, rt.method, rt.path, `{"token":"sk-ant-oat01-x"}`)
		if status != http.StatusUnauthorized {
			t.Errorf("%s %s anonymous = %d, want 401", rt.method, rt.path, status)
		}
	}
}
