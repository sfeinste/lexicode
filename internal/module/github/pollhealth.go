// Per-resource poll health (LEXI-9).
//
// A tick polls five independent resources. Before this, one of them erroring aborted the tick
// and the whole worker backed off exponentially — so a token missing GitHub's "Checks: read"
// permission, which 403s forever and can never succeed, dragged the effective poll interval
// from 30 seconds to the 15-minute cap for every other resource too. Pull requests, reviews
// and comments were all still perfectly readable; they just arrived a quarter of an hour late,
// and nothing anywhere said why.
//
// So each resource carries its own health here:
//
//   - A transient failure (5xx, a network drop, a rate limit) backs that resource off on its
//     own timer. The worker's cadence does not change, and the other four keep their interval.
//   - A permanent refusal — the forge saying the credential may not see this resource at all,
//     typed as *ports.ForbiddenError by the adapter's one error boundary — stops the resource
//     being polled, degrades the module with a reason naming the resource AND the permission
//     to grant, and logs once instead of once per tick. It is re-probed on a long interval,
//     and immediately when the repository is reconnected, so fixing the token recovers without
//     restarting the process.
package github

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// deniedRecheck is how long a disabled resource waits before spending one request to find out
// whether the token has been fixed. Long, because the answer is almost always "no" and the
// fast path to recovery is reconnecting the repository, which clears the state outright.
const deniedRecheck = 30 * time.Minute

// deniedRecheckWords is deniedRecheck in English, derived so the two cannot drift: a reason
// string a user is meant to act on should not contain "30m0s".
var deniedRecheckWords = fmt.Sprintf("%d minutes", int(deniedRecheck/time.Minute))

// pollResource is one pass's identity in words a user can act on. `key` doubles as the
// poll_cursors.resource name for the four passes that have a cursor row; reviews ride per-PR
// cursors in poll_pr_state and use their key for health only.
type pollResource struct {
	key    string
	what   string // "check suites" — reads after "Polling …"
	events string // "check_suite events (CI results)" — reads inside "No … will fire"
	grant  string // the fine-grained permission to grant, in GitHub's own words
}

const grantPulls = `the "Pull requests" repository permission (read access)`

var (
	resourcePulls = pollResource{
		key: resPulls, what: "pull requests",
		events: "pull_request events (opened, pushed, ready for review, closed)",
		grant:  grantPulls,
	}
	resourceReviews = pollResource{
		key: resReviews, what: "pull request reviews",
		events: "review submitted events", grant: grantPulls,
	}
	resourceReviewComments = pollResource{
		key: resReviewComments, what: "review comments",
		events: "review_comment events", grant: grantPulls,
	}
	resourceIssueComments = pollResource{
		key: resIssueComments, what: "issue comments",
		events: "issue_comment events",
		grant:  `the "Issues" repository permission (read access)`,
	}
	resourceCheckSuites = pollResource{
		key: resCheckSuites, what: "check suites",
		events: "check_suite events (CI results)",
		grant:  `the "Checks" repository permission (read access)`,
	}
)

// resourceState is one project-and-resource's poll health.
type resourceState struct {
	failures    int       // consecutive transient failures
	nextAttempt time.Time // transient backoff: the pass is skipped until this passes
	disabled    bool      // the forge refuses this resource to this credential
	recheckAt   time.Time // when a disabled resource spends one request re-probing
}

// healthKey is the moduleHealth key one project's one resource degrades under. It is
// project-scoped, so two projects denied the same resource each get their own sentence and
// clear independently.
func healthKey(projectID, resource string) string { return "poll|" + projectID + "|" + resource }

// resourceDue reports whether a pass should run this tick: always, unless it is inside a
// transient backoff window or disabled and not yet due for a re-probe.
func (p *Poller) resourceDue(projectID, resource string, now time.Time) bool {
	p.rmu.Lock()
	defer p.rmu.Unlock()
	st := p.resources[projectID][resource]
	if st == nil {
		return true
	}
	if st.disabled {
		return !now.Before(st.recheckAt)
	}
	return !now.Before(st.nextAttempt)
}

// stateFor returns the mutable state for one project-and-resource, creating it on demand.
// Callers hold p.rmu.
func (p *Poller) stateFor(projectID, resource string) *resourceState {
	byRes := p.resources[projectID]
	if byRes == nil {
		byRes = map[string]*resourceState{}
		p.resources[projectID] = byRes
	}
	st := byRes[resource]
	if st == nil {
		st = &resourceState{}
		byRes[resource] = st
	}
	return st
}

// resourceOK records a pass that worked: the backoff clears, and a resource that had been
// disabled comes back — which is how a re-granted token recovers without a restart.
func (p *Poller) resourceOK(projectID string, res pollResource) {
	p.rmu.Lock()
	st := p.stateFor(projectID, res.key)
	wasDisabled := st.disabled
	*st = resourceState{}
	p.rmu.Unlock()
	if !wasDisabled {
		return
	}
	if p.mh.recover(healthKey(projectID, res.key)) {
		p.logger.Info("github.poll: resource readable again; polling resumed",
			slog.String("project", projectID), slog.String("resource", res.key))
	}
}

// resourceDenied records a permanent refusal: stop polling the resource, say so once, and
// leave the module degraded with a reason that names what to grant.
func (p *Poller) resourceDenied(projectID string, res pollResource, repo domain.RepoRef,
	fe *ports.ForbiddenError, now time.Time,
) {
	p.rmu.Lock()
	st := p.stateFor(projectID, res.key)
	st.disabled = true
	st.failures = 0
	st.nextAttempt = time.Time{}
	st.recheckAt = now.Add(deniedRecheck)
	p.rmu.Unlock()

	reason := deniedReason(res, repo, fe)
	// degrade reports true only on a change, so the WARN below is written once per refusal,
	// not once per tick — the noise this bug produced for hours.
	if p.mh.degrade(healthKey(projectID, res.key), reason) {
		p.logger.Warn("github.poll: resource not readable by this token; polling disabled",
			slog.String("project", projectID),
			slog.String("resource", res.key),
			slog.Int("status", fe.Status),
			slog.String("reason", reason))
	}
}

// resourceTransient records a failure that may well clear on its own: back this resource off,
// and only this one. The worker keeps its cadence for everything else.
func (p *Poller) resourceTransient(projectID string, res pollResource, err error,
	now time.Time, base time.Duration,
) {
	p.rmu.Lock()
	st := p.stateFor(projectID, res.key)
	st.failures++
	failures := st.failures
	wait := backoffFor(base, failures, now, err)
	st.nextAttempt = now.Add(wait)
	p.rmu.Unlock()
	p.logger.Warn("github.poll: resource pass failed; backing that resource off",
		slog.String("project", projectID), slog.String("resource", res.key),
		slog.Int("consecutive", failures), slog.Duration("retry_in", wait),
		slog.String("error", err.Error()))
}

// forgetResources drops a project's per-resource health and clears anything it had the module
// degraded for. Called when the repository is disconnected, and when it is (re)connected —
// the latter is the user's fast path back after fixing a token, because connect re-runs with
// the new credential rather than waiting out deniedRecheck.
func (p *Poller) forgetResources(projectID string) {
	p.rmu.Lock()
	byRes := p.resources[projectID]
	delete(p.resources, projectID)
	p.rmu.Unlock()
	for key := range byRes {
		if p.mh.recover(healthKey(projectID, key)) {
			p.logger.Info("github.poll: resource re-enabled by repository (re)connect",
				slog.String("project", projectID), slog.String("resource", key))
		}
	}
}

// backoffFor is the shared exponential backoff: the base interval doubled per consecutive
// failure, capped at maxErrorBackoff, and never shorter than a known rate-limit reset —
// sleeping past the reset beats hammering a dead budget.
func backoffFor(base time.Duration, failures int, now time.Time, err error) time.Duration {
	wait := base << min(failures, 10)
	if wait <= 0 || wait > maxErrorBackoff {
		wait = maxErrorBackoff
	}
	var rl *ports.RateLimitedError
	if errors.As(err, &rl) {
		if until := rl.Reset.Sub(now); until > wait && until < maxErrorBackoff {
			wait = until
		}
	}
	return wait
}

// deniedReason is the sentence the user reads on the module card. It is the entire fix from
// their side of the screen: the symptom they noticed was "triggers are weirdly slow", so it
// has to say which resource stopped, which triggers therefore will not fire, that the rest of
// the polling is unaffected, exactly which permission to grant, and how to make the fix take
// effect. Anything vaguer sends them back to reading log files.
func deniedReason(res pollResource, repo domain.RepoRef, fe *ports.ForbiddenError) string {
	switch fe.Status {
	case 401:
		return fmt.Sprintf("Polling %s on %s is disabled: GitHub no longer accepts this token (HTTP 401). "+
			"No %s will fire. Replace the token and reconnect the repository.",
			res.what, repo, res.events)
	case 404:
		return fmt.Sprintf("Polling %s on %s is disabled: GitHub answered 404, which is what a token without "+
			"access is told instead of 403 — the repository may also have been renamed or deleted. "+
			"No %s will fire; every other GitHub event is still polled at the normal interval. "+
			"Check the repository still exists and grant the token %s, then reconnect the repository "+
			"— or wait up to %s for the automatic re-check.",
			res.what, repo, res.events, res.grant, deniedRecheckWords)
	default:
		return fmt.Sprintf("Polling %s on %s is disabled: this token is not permitted to read them (HTTP %d). "+
			"No %s will fire; every other GitHub event is still polled at the normal interval. "+
			"Grant the token %s — a classic PAT needs the \"repo\" scope — then reconnect the repository, "+
			"or wait up to %s for the automatic re-check.",
			res.what, repo, fe.Status, res.events, res.grant, deniedRecheckWords)
	}
}
