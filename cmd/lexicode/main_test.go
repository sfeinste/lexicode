package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/config"
)

func TestVersionFlagPrintsInjectedVersion(t *testing.T) {
	original := version
	version = "1.2.3-test"
	t.Cleanup(func() { version = original })

	var out, errOut bytes.Buffer
	if code := run([]string{"--version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != "1.2.3-test" {
		t.Errorf("output = %q, want the ldflags-injected version", got)
	}
}

func TestVersionSubcommand(t *testing.T) {
	original := version
	version = "9.9.9"
	t.Cleanup(func() { version = original })

	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != "9.9.9" {
		t.Errorf("output = %q", got)
	}
}

func TestMigrateIsWiredUp(t *testing.T) {
	home := t.TempDir()
	var out, errOut bytes.Buffer
	code := run([]string{"migrate", "--data-dir", home, "--config", home + "/absent.yaml"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "applied 0001_init") {
		t.Errorf("output = %q, want it to name the applied migration", out.String())
	}

	// Running it again is a no-op that says so.
	out.Reset()
	errOut.Reset()
	code = run([]string{"migrate", "--data-dir", home, "--config", home + "/absent.yaml"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("second run exit code = %d, want 0 (stderr: %s)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "up to date") {
		t.Errorf("second run output = %q, want the up-to-date line", out.String())
	}
}

func TestUnknownCommandExitsTwo(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"frobnicate"}, &out, &errOut); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "frobnicate") {
		t.Errorf("stderr = %q, want it to name the unknown command", errOut.String())
	}
}

func TestNoCommandExitsTwo(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

// TestAPINamespaceNeverReturnsHTML pins the routing rule that makes the SPA fallback safe: an
// unknown /api path is a problem+json 404, not the app shell. Setup runs first because before
// it, the S05 setup gate answers every API path with its own problem+json (401 setup_required)
// — also never HTML, but not the 404 this test pins.
func TestAPINamespaceNeverReturnsHTML(t *testing.T) {
	srv := newTestServer(t)

	setup, err := http.Post(srv+"/api/v1/auth/setup", "application/json",
		strings.NewReader(`{"email":"ada@example.com","display_name":"Ada","password":"correct horse"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = setup.Body.Close()
	if setup.StatusCode != http.StatusCreated {
		t.Fatalf("setup status = %d, want 201", setup.StatusCode)
	}

	resp, err := http.Get(srv + "/api/v1/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("content-type = %q, want application/problem+json", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"type":"not_found"`) {
		t.Errorf("body = %q, want a stable problem type slug", body)
	}
}

func TestDeepRouteReturnsTheAppShell(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Get(srv + "/projects/abc/board")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
}

// TestGracefulShutdown covers the S01 acceptance criterion "killing it with SIGINT exits 0 within
// 2s" at the level the test suite can reach: cancelling serve's context returns nil promptly.
func TestGracefulShutdown(t *testing.T) {
	cfg := config.Config{Host: "127.0.0.1", Port: freePort(t), DataDir: t.TempDir(), LogLevel: "error"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, logger, io.Discard, false) }()

	waitForServer(t, cfg.Addr())
	start := time.Now()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v, want nil (a clean stop must exit 0)", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("shutdown took %s, want under 2s", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not return within 3s of the signal")
	}
}

func newTestServer(t *testing.T) string {
	t.Helper()
	cfg := config.Config{Host: "127.0.0.1", Port: freePort(t), DataDir: t.TempDir(), LogLevel: "error"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, logger, io.Discard, false) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("test server did not shut down")
		}
	})

	waitForServer(t, cfg.Addr())
	return "http://" + cfg.Addr()
}

// TestSystemModulesIsServed checks the kernel is actually wired into serve: its route answers on
// the real server, and the /api/ catch-all does not shadow it. Since S05 the route sits behind
// auth, so the test walks the first-run path: with zero users every API call is 401
// "setup_required", and after setup the cookie unlocks the list. Since S14 the github module is
// wired in; with no repository connected it has nothing to verify at boot, so it reports ready.
// Since S17 the docker module is wired in; its state depends on whether a daemon is reachable
// on the machine running the test. Since S20 the claude-code runtime module is wired in.
// The rest arrive with the stories that build them.
func TestSystemModulesIsServed(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Get(srv + "/api/v1/system/modules")
	if err != nil {
		t.Fatal(err)
	}
	gated, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("pre-setup status = %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(string(gated), `"setup_required"`) {
		t.Fatalf("pre-setup body = %s, want problem type setup_required", gated)
	}

	setup, err := http.Post(srv+"/api/v1/auth/setup", "application/json",
		strings.NewReader(`{"email":"ada@example.com","display_name":"Ada","password":"correct horse"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = setup.Body.Close()
	if setup.StatusCode != http.StatusCreated {
		t.Fatalf("setup status = %d, want 201", setup.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range setup.Cookies() {
		if c.Name == "lexicode_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("setup did not set the session cookie")
	}

	req, _ := http.NewRequest(http.MethodGet, srv+"/api/v1/system/modules", nil)
	req.AddCookie(cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	var got struct {
		Modules []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body %s is not the modules shape: %v", body, err)
	}
	if len(got.Modules) != 8 || got.Modules[0].Name != "github" ||
		got.Modules[1].Name != "cron" || got.Modules[2].Name != "docker" ||
		got.Modules[3].Name != "claude-code" || got.Modules[4].Name != "credentials" ||
		got.Modules[5].Name != "context" || got.Modules[6].Name != "notify" ||
		got.Modules[7].Name != "actions" {
		t.Fatalf("modules = %s, want github, cron, docker, claude-code, credentials, context, notify, actions in registration order", body)
	}
	if got.Modules[0].State != "ready" {
		t.Errorf("github state = %q, want ready", got.Modules[0].State)
	}
	if got.Modules[1].State != "ready" {
		t.Errorf("cron state = %q, want ready", got.Modules[1].State)
	}
	if got.Modules[3].State != "ready" {
		t.Errorf("claude-code state = %q, want ready", got.Modules[3].State)
	}
	if got.Modules[4].State != "ready" {
		t.Errorf("credentials state = %q, want ready", got.Modules[4].State)
	}
	// docker's state depends on the machine: ready where a daemon is reachable, degraded
	// where it is not (the whole point of the degraded state — boot must not require Docker).
	if s := got.Modules[2].State; s != "ready" && s != "degraded" {
		t.Errorf("docker state = %q, want ready or degraded", s)
	}
}
