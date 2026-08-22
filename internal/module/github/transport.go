package github

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/spruce/lexicode/internal/kernel/ports"
)

// Transport tuning defaults. Options overrides them; tests set them very small.
const (
	defaultMaxAttempts       = 3                      // total tries per request (5xx retry cap)
	defaultRetryBase         = 500 * time.Millisecond // first 5xx backoff; doubles per attempt
	defaultMaxRateLimitSleep = 5 * time.Second        // longest reset wait worth sleeping through
)

// transport is the module's custom http.RoundTripper (story S14):
//
//   - 5xx responses are retried with jittered exponential backoff, at most maxAttempts tries.
//   - X-RateLimit-Remaining/-Reset are respected with a sleep-or-degrade policy: a reset that
//     is at most maxSleep away is slept through and the request retried once; a farther reset
//     marks the module degraded and returns a typed *ports.RateLimitedError — including on
//     later requests, proactively, without touching the network until the reset passes.
//
// go-github's own pre-flight rate-limit check is disabled (WithDisableRateLimitCheck) so this
// policy is the only one in play and is what the tests exercise.
type transport struct {
	base        http.RoundTripper
	maxAttempts int
	retryBase   time.Duration
	maxSleep    time.Duration

	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error

	// onExhausted and onHealthy report health transitions to the module (which forwards them
	// to kernel.SetModuleState). Never nil.
	onExhausted func(reset time.Time)
	onHealthy   func()

	mu           sync.Mutex
	blockedUntil time.Time // non-zero while the rate limit is known to be exhausted
	degraded     bool
}

func newTransport(base http.RoundTripper, maxAttempts int, retryBase, maxSleep time.Duration) *transport {
	if base == nil {
		base = http.DefaultTransport
	}
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	if retryBase <= 0 {
		retryBase = defaultRetryBase
	}
	if maxSleep <= 0 {
		maxSleep = defaultMaxRateLimitSleep
	}
	return &transport{
		base:        base,
		maxAttempts: maxAttempts,
		retryBase:   retryBase,
		maxSleep:    maxSleep,
		now:         time.Now,
		sleep:       sleepCtx,
		onExhausted: func(time.Time) {},
		onHealthy:   func() {},
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Proactive gate: a previous response told us the budget is exhausted until blockedUntil.
	// Respect it without spending a network call.
	if err := t.waitIfBlocked(req.Context()); err != nil {
		return nil, err
	}

	resp, err := t.attemptWithRetries(req)
	if err != nil {
		return nil, err
	}

	if rateLimited(resp) {
		reset := resetTime(resp)
		wait := reset.Sub(t.now())
		if wait > 0 && wait <= t.maxSleep {
			// Sleep-through: the reset is close; wait it out and retry once.
			drain(resp)
			if err := t.sleep(req.Context(), wait); err != nil {
				return nil, err
			}
			resp, err = t.attemptWithRetries(req)
			if err != nil {
				return nil, err
			}
			if !rateLimited(resp) {
				t.recordBudget(resp)
				return resp, nil
			}
			reset = resetTime(resp)
		}
		drain(resp)
		t.exhaust(reset)
		return nil, &ports.RateLimitedError{Reset: reset}
	}

	t.recordBudget(resp)
	return resp, nil
}

// attemptWithRetries performs the request, retrying 5xx responses with jittered exponential
// backoff up to maxAttempts total tries. The last response is returned as-is (go-github turns
// it into an *ErrorResponse); network errors are never retried.
func (t *transport) attemptWithRetries(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := t.base.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 500 || attempt+1 >= t.maxAttempts {
			return resp, nil
		}
		fresh, ok := rewind(req)
		if !ok {
			return resp, nil // a body we cannot replay is a request we cannot retry
		}
		drain(resp)
		backoff := t.retryBase << attempt
		backoff += time.Duration(rand.Int64N(int64(backoff))) // full jitter on top of the base
		if err := t.sleep(req.Context(), backoff); err != nil {
			return nil, err
		}
		req = fresh
	}
}

// waitIfBlocked applies the sleep-or-degrade policy to a known-exhausted budget before any
// network I/O: sleep when the reset is close, fail typed (and stay degraded) when it is not.
func (t *transport) waitIfBlocked(ctx context.Context) error {
	t.mu.Lock()
	until := t.blockedUntil
	t.mu.Unlock()
	if until.IsZero() {
		return nil
	}
	wait := until.Sub(t.now())
	if wait <= 0 {
		return nil
	}
	if wait > t.maxSleep {
		return &ports.RateLimitedError{Reset: until}
	}
	return t.sleep(ctx, wait)
}

// recordBudget notes the remaining budget from a successful (non-rate-limited) response: it
// clears the degraded state, and if this very response spent the last unit it re-arms the
// proactive gate so the next call does not have to burn a 403 to find out.
func (t *transport) recordBudget(resp *http.Response) {
	t.mu.Lock()
	wasDegraded := t.degraded
	t.degraded = false
	t.blockedUntil = time.Time{}
	remaining, ok := headerInt(resp, "X-RateLimit-Remaining")
	var reset time.Time
	if ok && remaining <= 0 {
		reset = resetTime(resp)
		if reset.After(t.now()) {
			t.blockedUntil = reset
		}
	}
	t.mu.Unlock()
	if wasDegraded {
		t.onHealthy()
	}
}

// exhaust marks the budget exhausted until reset and reports the module degraded.
func (t *transport) exhaust(reset time.Time) {
	t.mu.Lock()
	t.blockedUntil = reset
	first := !t.degraded
	t.degraded = true
	t.mu.Unlock()
	if first {
		t.onExhausted(reset)
	}
}

// rateLimited reports whether resp is GitHub saying "primary rate limit exhausted": a 403 or
// 429 with X-RateLimit-Remaining: 0.
func rateLimited(resp *http.Response) bool {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return false
	}
	remaining, ok := headerInt(resp, "X-RateLimit-Remaining")
	return ok && remaining <= 0
}

// resetTime parses X-RateLimit-Reset (unix seconds). A missing or malformed header yields the
// zero time, which every caller treats as "already past".
func resetTime(resp *http.Response) time.Time {
	unix, ok := headerInt(resp, "X-RateLimit-Reset")
	if !ok {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

func headerInt(resp *http.Response, key string) (int64, bool) {
	v := resp.Header.Get(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// rewind clones req with a fresh body for a retry. Requests without a body always rewind;
// requests with one need GetBody (which net/http sets for the buffered bodies go-github sends).
func rewind(req *http.Request) (*http.Request, bool) {
	if req.Body == nil || req.Body == http.NoBody {
		return req, true
	}
	if req.GetBody == nil {
		return nil, false
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, false
	}
	fresh := req.Clone(req.Context())
	fresh.Body = body
	return fresh, true
}

// drain discards a response we are about to replace, so its connection can be reused.
func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}

// degradedReason renders the human-readable reason shown on the module card.
func degradedReason(reset time.Time) string {
	return fmt.Sprintf("GitHub API rate limit exhausted; resets at %s", reset.UTC().Format(time.RFC3339))
}
