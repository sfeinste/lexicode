package github

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/kernel/ports"
)

// These tests drive the read path through the same fixture harness as forge_test.go, with
// stateful handlers standing in for GitHub's failure modes.

func TestServerErrorsAreRetriedWithBackoff(t *testing.T) {
	h := newHarness(t)
	var attempts atomic.Int32
	h.mux.HandleFunc("GET /repos/acme/payments/issues", func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	})

	if _, err := h.forge.ListOpenIssues(ctx(), testCreds, testRepo); err != nil {
		t.Fatalf("ListOpenIssues after two 500s: %v", err)
	}
	if n := attempts.Load(); n != 3 {
		t.Errorf("server saw %d attempts; want 3 (two 500s, then success)", n)
	}
	h.mu.Lock()
	sleeps := append([]time.Duration(nil), h.sleeps...)
	h.mu.Unlock()
	if len(sleeps) != 2 {
		t.Fatalf("recorded %d backoff sleeps; want 2", len(sleeps))
	}
	// Exponential base with full jitter: attempt i sleeps in [base<<i, 2*(base<<i)).
	if sleeps[0] < time.Millisecond || sleeps[1] < 2*time.Millisecond {
		t.Errorf("backoffs %v are below the exponential base", sleeps)
	}
}

func TestServerErrorsGiveUpAfterMaxAttempts(t *testing.T) {
	h := newHarness(t)
	var attempts atomic.Int32
	h.mux.HandleFunc("GET /repos/acme/payments/issues", func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(502)
	})

	_, err := h.forge.ListOpenIssues(ctx(), testCreds, testRepo)
	if err == nil {
		t.Fatal("ListOpenIssues succeeded against a permanently-502 server")
	}
	if n := attempts.Load(); n != defaultMaxAttempts {
		t.Errorf("server saw %d attempts; want %d", n, defaultMaxAttempts)
	}
}

func TestRateLimitExhaustionDegradesAndReturnsTypedError(t *testing.T) {
	h := newHarness(t)
	reset := time.Now().Add(2 * time.Hour)
	h.fixture("GET /repos/acme/payments/issues", 403,
		`{"message":"API rate limit exceeded"}`,
		map[string]string{
			"X-RateLimit-Remaining": "0",
			"X-RateLimit-Reset":     fmt.Sprintf("%d", reset.Unix()),
		})

	_, err := h.forge.ListOpenIssues(ctx(), testCreds, testRepo)
	if !errors.Is(err, ports.ErrRateLimited) {
		t.Fatalf("err = %v; want ErrRateLimited", err)
	}
	var rl *ports.RateLimitedError
	if !errors.As(err, &rl) || rl.Reset.Unix() != reset.Unix() {
		t.Fatalf("typed error does not carry the reset time: %v", err)
	}

	h.mu.Lock()
	health := append([]string(nil), h.health...)
	h.mu.Unlock()
	if len(health) != 1 || !strings.HasPrefix(health[0], "degraded|") ||
		!strings.Contains(health[0], "rate limit exhausted") {
		t.Fatalf("health transitions = %v; want one degraded report", health)
	}

	// The proactive gate: while the budget is known-exhausted, further calls fail typed
	// without touching the network at all.
	before := h.calls.Load()
	_, err = h.forge.ListOpenIssues(ctx(), testCreds, testRepo)
	if !errors.Is(err, ports.ErrRateLimited) {
		t.Fatalf("second call err = %v; want ErrRateLimited", err)
	}
	if after := h.calls.Load(); after != before {
		t.Errorf("a rate-limited client still made %d HTTP calls; want 0", after-before)
	}
}

func TestRateLimitNearResetIsSleptThrough(t *testing.T) {
	h := newHarness(t)
	var attempts atomic.Int32
	h.mux.HandleFunc("GET /repos/acme/payments/issues", func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(2*time.Second).Unix()))
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"message":"API rate limit exceeded"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		_, _ = io.WriteString(w, `[]`)
	})

	if _, err := h.forge.ListOpenIssues(ctx(), testCreds, testRepo); err != nil {
		t.Fatalf("ListOpenIssues with a near reset: %v", err)
	}
	if n := attempts.Load(); n != 2 {
		t.Errorf("server saw %d attempts; want 2 (rate-limited, then retried after the reset)", n)
	}
	h.mu.Lock()
	slept := len(h.sleeps) > 0
	degraded := len(h.health) > 0
	h.mu.Unlock()
	if !slept {
		t.Error("the reset wait was not slept through")
	}
	if degraded {
		t.Error("a sleep-through must not report the module degraded")
	}
}

func TestRateLimitRecoveryReportsHealthy(t *testing.T) {
	h := newHarness(t)
	reset := time.Now().Add(2 * time.Hour)
	var failing atomic.Bool
	failing.Store(true)
	h.mux.HandleFunc("GET /repos/acme/payments/issues", func(w http.ResponseWriter, _ *http.Request) {
		if failing.Load() {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", reset.Unix()))
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"message":"API rate limit exceeded"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		_, _ = io.WriteString(w, `[]`)
	})

	if _, err := h.forge.ListOpenIssues(ctx(), testCreds, testRepo); !errors.Is(err, ports.ErrRateLimited) {
		t.Fatalf("err = %v; want ErrRateLimited", err)
	}

	// The reset passes (simulated clock) and GitHub recovers.
	failing.Store(false)
	h.forge.transport.now = func() time.Time { return reset.Add(time.Minute) }
	if _, err := h.forge.ListOpenIssues(ctx(), testCreds, testRepo); err != nil {
		t.Fatalf("ListOpenIssues after the reset: %v", err)
	}

	h.mu.Lock()
	health := append([]string(nil), h.health...)
	h.mu.Unlock()
	if len(health) != 2 || !strings.HasPrefix(health[0], "degraded|") || !strings.HasPrefix(health[1], "ready|") {
		t.Fatalf("health transitions = %v; want degraded then ready", health)
	}
}
