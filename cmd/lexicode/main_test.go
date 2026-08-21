package main

import (
	"bytes"
	"context"
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
	if !strings.Contains(out.String(), "no migrations") {
		t.Errorf("output = %q, want the S03 placeholder line", out.String())
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
// unknown /api path is a problem+json 404, not the app shell.
func TestAPINamespaceNeverReturnsHTML(t *testing.T) {
	srv := newTestServer(t)

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
	go func() { done <- serve(ctx, cfg, logger, io.Discard) }()

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
	go func() { done <- serve(ctx, cfg, logger, io.Discard) }()
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
