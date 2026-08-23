// The GitHub poller (story S25, architecture §7, D-3): one goroutine per connected project,
// diffing API state into normalized events on the kernel bus.
//
// Each tick, per the §7 table: the pull listing (state=all, sort=updated) is diffed against
// poll_pr_state to *derive* activity types — opened vs synchronize vs ready_for_review vs
// closed — because GitHub's list endpoints do not hand them out and the opened/synchronize
// distinction is where the runaway loop lives. Review comments and issue comments ride
// updated_at cursors; reviews are listed only for PRs touched this tick (per-PR cursor in
// poll_pr_state.review_cursor); check suites are listed for open PR heads behind a completed-at
// cursor. Listing requests offer If-None-Match from poll_cursors.etag — a 304 costs no rate
// limit and skips the resource. Every event carries a deterministic dedupe key
// (sha256(project|resource|id|discriminator)), so replaying a tick emits nothing twice: the
// bus's insert-idempotency is the second line of defence behind the cursors.
//
// Cold start: the first tick for a project records every listed PR into poll_pr_state, seeds
// all cursors to "now", emits NOTHING, and logs "baseline — no events emitted" (plus an audit
// row, which is what the trigger history surface can later show). A repo with 40 open PRs
// must not fire 40 triggers on connect.
//
// Assumption, documented: review submissions and comments bump the PR listing's content (the
// PR's updated_at), so a 304 on the pull listing means no reviews/comments happened either for
// touched-PR purposes. Comment listings have their own cursors and are polled each tick
// regardless.
package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Poll cursor resource names (poll_cursors.resource).
const (
	resPulls          = "pulls"
	resReviewComments = "review_comments"
	resIssueComments  = "issue_comments"
	resCheckSuites    = "check_suites"
	// resReviews has no poll_cursors row — reviews ride per-PR cursors in poll_pr_state
	// (§7 table row 3). It exists so the reviews pass has a health identity of its own
	// (pollhealth.go), the same as the four cursored resources.
	resReviews = "reviews"
)

// Interval policy: workspace_settings.poll_interval_seconds, default 30, floor 10
// (architecture §7). The floor is enforcement — a 1-second setting must not melt the rate
// limit budget.
const (
	defaultPollSeconds = 30
	floorPollSeconds   = 10
)

// maxErrorBackoff bounds the exponential backoff applied on top of the configured interval
// after a transient failure (rate limits and API outages; the transport additionally marks the
// module degraded, S14). It applies per resource, and to a whole tick that could not even
// begin — never to a worker whose resources are merely partly unreadable (LEXI-9).
const maxErrorBackoff = 15 * time.Minute

// errNoRepo tells a worker its project no longer has a connected repo; the worker exits.
var errNoRepo = errors.New("github.poll: project has no connected repo")

// Poller is the github.poll EventSource (contracts §2.1): the module's second port, sharing
// the Forge adapter's transport (and thereby its retry and rate-limit policy).
type Poller struct {
	forge  *Forge
	logger *slog.Logger

	store *store.Store
	creds func(ctx context.Context, rp domain.Repo) (ports.Creds, error)
	now   func() time.Time

	// after is the worker's sleep, seamed so a test can assert the cadence a resource
	// failure leaves the worker on without waiting out real minutes.
	after func(time.Duration) <-chan time.Time

	mu      sync.Mutex
	emit    ports.Emit
	baseCtx context.Context
	cancel  context.CancelFunc
	workers map[string]context.CancelFunc
	started bool
	wg      sync.WaitGroup

	// mh composes this module's kernel state; rmu guards per-project resource health
	// (pollhealth.go). Both are separate from mu, which guards the worker registry.
	mh        *moduleHealth
	rmu       sync.Mutex
	resources map[string]map[string]*resourceState
}

// newPoller builds the poller around the module's forge. Store, creds and logger are wired in
// Module.Init (or directly by tests).
func newPoller(f *Forge) *Poller {
	mh := newModuleHealth(nil)
	if f != nil && f.mh != nil {
		mh = f.mh
	}
	return &Poller{
		forge:     f,
		logger:    slog.Default(),
		now:       time.Now,
		after:     time.After,
		workers:   make(map[string]context.CancelFunc),
		mh:        mh,
		resources: make(map[string]map[string]*resourceState),
	}
}

// ID implements ports.EventSource.
func (p *Poller) ID() string { return pollSourceID }

// Catalog implements ports.EventSource.
func (p *Poller) Catalog() ports.EventCatalog { return catalog() }

// Start implements ports.EventSource: one worker per project with a connected repo. It
// returns promptly; polling happens on the workers' goroutines under ctx.
func (p *Poller) Start(ctx context.Context, emit ports.Emit) error {
	if p.store == nil {
		return errors.New("github.poll: no store wired")
	}
	if emit == nil {
		return errors.New("github.poll: no emit wired")
	}
	wctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		cancel()
		return errors.New("github.poll: Start called twice")
	}
	p.started = true
	p.emit = emit
	p.baseCtx = wctx
	p.cancel = cancel
	p.mu.Unlock()

	repos, err := p.store.Repos().List(ctx)
	if err != nil {
		return fmt.Errorf("github.poll: list connected repos: %w", err)
	}
	for _, rp := range repos {
		p.EnsureWorker(rp.ProjectID)
	}
	return nil
}

// Stop implements ports.EventSource: cancels every worker and waits for them (bounded by ctx).
func (p *Poller) Stop(ctx context.Context) error {
	p.mu.Lock()
	if !p.started {
		p.mu.Unlock()
		return nil
	}
	p.started = false
	cancel := p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("github.poll: workers did not drain: %w", ctx.Err())
	}
}

// EnsureWorker starts the project's poll goroutine if the poller is started and none runs yet
// (module boot, and the repo.connected bus event).
func (p *Poller) EnsureWorker(projectID string) {
	p.mu.Lock()
	if !p.started {
		p.mu.Unlock()
		return // Start's repo listing will pick the project up
	}
	if _, ok := p.workers[projectID]; ok {
		p.mu.Unlock()
		// A running worker plus a repo.connected event is a RECONNECT — the fast path back
		// for a user who has just fixed the token that got a resource disabled. Re-probe on
		// the next tick instead of waiting out deniedRecheck.
		p.forgetResources(projectID)
		return
	}
	wctx, cancel := context.WithCancel(p.baseCtx)
	p.workers[projectID] = cancel
	p.wg.Add(1)
	go p.runWorker(wctx, projectID)
	p.mu.Unlock()
	p.logger.Info("github.poll: worker started", slog.String("project", projectID))
}

// RemoveWorker stops the project's poll goroutine and clears its poll state, so a later
// reconnect starts from a fresh baseline (the repo.disconnected bus event).
func (p *Poller) RemoveWorker(ctx context.Context, projectID string) {
	p.mu.Lock()
	cancel, ok := p.workers[projectID]
	delete(p.workers, projectID)
	p.mu.Unlock()
	if ok {
		cancel()
		p.logger.Info("github.poll: worker stopped", slog.String("project", projectID))
	}
	p.forgetResources(projectID)
	if p.store == nil {
		return
	}
	if err := p.store.PollCursors().DeleteForProject(ctx, projectID); err != nil {
		p.logger.Warn("github.poll: clear cursors failed", slog.String("project", projectID),
			slog.String("error", err.Error()))
	}
	if err := p.store.PollPRState().DeleteForProject(ctx, projectID); err != nil {
		p.logger.Warn("github.poll: clear PR state failed", slog.String("project", projectID),
			slog.String("error", err.Error()))
	}
}

// dropWorker forgets a worker that is exiting on its own (repo row gone).
func (p *Poller) dropWorker(projectID string) {
	p.mu.Lock()
	if cancel, ok := p.workers[projectID]; ok {
		delete(p.workers, projectID)
		defer cancel()
	}
	p.mu.Unlock()
}

// interval reads workspace_settings.poll_interval_seconds with the default and the floor
// applied.
func (p *Poller) interval(ctx context.Context) time.Duration {
	sec := int64(defaultPollSeconds)
	if ws, err := p.store.Workspace().Get(ctx); err == nil && ws.PollIntervalSeconds > 0 {
		sec = ws.PollIntervalSeconds
	}
	if sec < floorPollSeconds {
		sec = floorPollSeconds
	}
	return time.Duration(sec) * time.Second
}

// runWorker is one project's poll loop: tick, sleep the configured interval (re-read every
// loop so a settings change applies without restart), exit when the repo is disconnected or
// the context ends.
//
// The backoff here is for a tick that could not START — no repo row readable, credentials
// unresolvable, the baseline pass failing. It is deliberately NOT reached by a resource that
// merely cannot be polled: those back off individually inside tick (pollhealth.go), because a
// worker that slows every resource down to the pace of its least readable one is the LEXI-9
// bug — a token missing "Checks: read" put a 30-second poll on a 15-minute cadence and left
// every trigger in the workspace quarter-hours late.
func (p *Poller) runWorker(ctx context.Context, projectID string) {
	defer p.wg.Done()
	failures := 0
	for {
		err := p.tick(ctx, projectID)
		switch {
		case errors.Is(err, errNoRepo):
			p.logger.Info("github.poll: repo disconnected; worker exits",
				slog.String("project", projectID))
			p.dropWorker(projectID)
			return
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return
		case err != nil:
			failures++
			p.logger.Warn("github.poll: tick could not run", slog.String("project", projectID),
				slog.Int("consecutive", failures), slog.String("error", err.Error()))
		default:
			failures = 0
		}

		wait := p.interval(ctx)
		if failures > 0 {
			wait = backoffFor(wait, failures, p.now(), err)
		}
		select {
		case <-ctx.Done():
			return
		case <-p.after(wait):
		}
	}
}

// ------------------------------------------------------------------------------ tick -----

// tickState carries one tick's shared context.
type tickState struct {
	projectID string
	repo      domain.Repo
	creds     ports.Creds
	agents    []domain.Agent
	template  string // effective branch template (repo row, else workspace default)
	now       time.Time

	prState map[int64]domain.PollPRState // post-pulls-pass snapshot
	touched []domain.PullRequest         // PRs the pull listing returned this tick, enriched
}

// tick runs one full poll pass for a project.
func (p *Poller) tick(ctx context.Context, projectID string) error {
	rp, err := p.store.Repos().ByProject(ctx, projectID)
	if errors.Is(err, store.ErrNotFound) {
		return errNoRepo
	}
	if err != nil {
		return err
	}
	creds, err := p.creds(ctx, rp)
	if err != nil {
		return fmt.Errorf("github.poll: resolve credentials: %w", err)
	}
	agents, err := p.store.Agents().ForProject(ctx, projectID)
	if err != nil {
		return err
	}
	template := ""
	if rp.BranchTemplate != nil {
		template = *rp.BranchTemplate
	}
	if template == "" {
		if ws, werr := p.store.Workspace().Get(ctx); werr == nil {
			template = ws.DefaultBranchTemplate
		}
	}

	t := &tickState{
		projectID: projectID, repo: rp, creds: creds, agents: agents,
		template: template, now: p.now().UTC(),
	}

	pulls, err := p.store.PollCursors().Get(ctx, projectID, resPulls)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if !pulls.BaselineDone {
		return p.baseline(ctx, t)
	}

	return p.runPasses(ctx, t, pulls)
}

// runPasses is the five resource passes of one tick, run independently (LEXI-9).
//
// They were a chain of early returns, which made the tick as readable as its least readable
// resource: one 403 and the other four never ran, their cursors never advanced, and the whole
// worker backed off. Now each pass owns its own outcome. A pass that succeeds advances its
// cursor and emits its events whatever the others did; a pass that fails is classified —
// permanently refused, or merely transient — and marked accordingly, and only that pass pays.
//
// Order still matters and is unchanged: pulls fills tickState.touched and .prState, which
// reviews and checks read. When pulls is the pass that failed those two see an empty touched
// set and simply have nothing to do this tick, which is the correct degradation and not an
// error of their own.
//
// The tick itself returns an error only for something that ends the WORKER: a cancelled
// context. Everything else has been recorded against the resource it belongs to, so a tick in
// which some passes worked is not a failed tick.
func (p *Poller) runPasses(ctx context.Context, t *tickState, pulls domain.PollCursor) error {
	base := p.interval(ctx)
	passes := []struct {
		res pollResource
		run func() error
	}{
		{resourcePulls, func() error { return p.pollPulls(ctx, t, pulls) }},
		{resourceReviews, func() error { return p.pollReviews(ctx, t) }},
		{resourceReviewComments, func() error { return p.pollComments(ctx, t, resReviewComments) }},
		{resourceIssueComments, func() error { return p.pollComments(ctx, t, resIssueComments) }},
		{resourceCheckSuites, func() error { return p.pollChecks(ctx, t) }},
	}
	for _, pass := range passes {
		if !p.resourceDue(t.projectID, pass.res.key, t.now) {
			continue
		}
		err := pass.run()
		switch {
		case err == nil:
			p.resourceOK(t.projectID, pass.res)
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return err
		default:
			var fe *ports.ForbiddenError
			if errors.As(err, &fe) {
				p.resourceDenied(t.projectID, pass.res, t.repo.Ref(), fe, t.now)
			} else {
				p.resourceTransient(t.projectID, pass.res, err, t.now, base)
			}
		}
	}
	return nil
}

// baseline is the cold-start pass (architecture §7): record every listed PR's state, seed all
// cursors to now, emit nothing. Logged, and written to the audit trail so the trigger history
// surface can show "baseline — no events emitted".
func (p *Poller) baseline(ctx context.Context, t *tickState) error {
	prs, err := p.forge.ListPullRequests(ctx, t.creds, t.repo.Ref(), time.Time{})
	if err != nil {
		return err
	}
	nowStr := domain.FormatTime(t.now)
	for _, pr := range prs {
		st := domain.PollPRState{
			ProjectID: t.projectID, Number: int64(pr.Number),
			HeadSHA: pr.HeadSHA, State: pr.State, Draft: pr.Draft,
			UpdatedAt: domain.FormatTime(pr.UpdatedAt), ReviewCursor: nowStr,
		}
		if err := p.store.PollPRState().Upsert(ctx, &st); err != nil {
			return err
		}
	}
	cursor := nowStr
	if mx := maxPRUpdated(prs); !mx.IsZero() {
		cursor = domain.FormatTime(mx)
	}
	for _, res := range []string{resPulls, resReviewComments, resIssueComments, resCheckSuites} {
		c := domain.PollCursor{
			ProjectID: t.projectID, Resource: res, Cursor: nowStr,
			BaselineDone: true, LastPolledAt: &nowStr,
		}
		if res == resPulls {
			c.Cursor = cursor
		}
		if err := p.store.PollCursors().Upsert(ctx, &c); err != nil {
			return err
		}
	}
	p.logger.Info("github.poll: baseline — no events emitted",
		slog.String("project", t.projectID),
		slog.String("repo", t.repo.Ref().String()),
		slog.Int("prs_recorded", len(prs)))
	if p.forge.auditRec != nil {
		_ = p.forge.auditRec(ctx, "github.poll.baseline",
			audit.Target{Kind: "repo", ID: t.projectID, ProjectID: t.projectID}, nil,
			map[string]any{"prs_recorded": len(prs), "note": "baseline — no events emitted"})
	}
	return nil
}

// pollPulls lists PRs updated since the cursor and derives activity types by diffing
// poll_pr_state (the §7 table's first row).
func (p *Poller) pollPulls(ctx context.Context, t *tickState, cur domain.PollCursor) error {
	since := parseCursor(cur.Cursor)
	es := &etagState{send: cur.Etag}
	prs, err := p.forge.ListPullRequests(withEtag(ctx, es), t.creds, t.repo.Ref(), since)
	etag, notModified := es.Result()
	if notModified {
		return p.touchCursor(ctx, t, cur, cur.Cursor, etag)
	}
	if err != nil {
		return err
	}

	prState, err := p.store.PollPRState().ForProject(ctx, t.projectID)
	if err != nil {
		return err
	}
	t.prState = prState

	// Oldest-first, so a burst of changes lands in event order.
	sort.Slice(prs, func(i, j int) bool { return prs[i].UpdatedAt.Before(prs[j].UpdatedAt) })

	maxUpdated := parseCursor(cur.Cursor)
	for _, pr := range prs {
		if pr.UpdatedAt.After(maxUpdated) {
			maxUpdated = pr.UpdatedAt
		}
		prev, seen := prState[int64(pr.Number)]
		acts := deriveActivities(prev, seen, pr)

		detailed := false
		if len(acts) > 0 {
			// The detail read fills additions/deletions/files_changed, which the list
			// endpoint does not carry (contracts §4 exposes them as pr.* fields).
			if full, derr := p.forge.GetPullRequest(ctx, t.creds, t.repo.Ref(), pr.Number); derr == nil {
				pr = full
				detailed = true
			} else {
				p.logger.Warn("github.poll: PR detail read failed; using list data",
					slog.Int("pr", pr.Number), slog.String("error", derr.Error()))
			}
		}
		t.touched = append(t.touched, pr)

		for _, act := range acts {
			if err := p.emitPREvent(ctx, t, pr, act); err != nil {
				return err
			}
		}

		st := domain.PollPRState{
			ProjectID: t.projectID, Number: int64(pr.Number),
			HeadSHA: pr.HeadSHA, State: pr.State, Draft: pr.Draft,
			UpdatedAt:    domain.FormatTime(pr.UpdatedAt),
			ReviewCursor: prev.ReviewCursor, // zero value ("") for unseen: a new PR's reviews are all new
			// Size counters (S37 diff-size warning) only from the detail read — the list
			// payload reports zeros, which would overwrite real numbers. An unchanged PR
			// keeps whatever the last detail read learned.
			Additions: prev.Additions,
			Deletions: prev.Deletions,
		}
		if detailed {
			adds, dels := int64(pr.Additions), int64(pr.Deletions)
			st.Additions, st.Deletions = &adds, &dels
		}
		if err := p.store.PollPRState().Upsert(ctx, &st); err != nil {
			return err
		}
		prState[st.Number] = st
	}

	cursor := cur.Cursor
	if !maxUpdated.IsZero() {
		cursor = domain.FormatTime(maxUpdated)
	}
	return p.touchCursor(ctx, t, cur, cursor, etag)
}

// deriveActivities is the §7 diff: what happened to one PR since the recorded state. Order
// within a tick: synchronize, ready_for_review, opened (reopen), closed.
func deriveActivities(prev domain.PollPRState, seen bool, pr domain.PullRequest) []string {
	var acts []string
	if !seen {
		// Unseen → opened; a PR born and closed inside one poll window emits only closed
		// (an opened trigger must not fire for a PR that is already gone).
		if pr.State == "open" {
			return []string{"opened"}
		}
		return []string{"closed"}
	}
	if pr.HeadSHA != prev.HeadSHA && pr.State == "open" {
		acts = append(acts, "synchronize")
	}
	if prev.State == "open" && pr.State == "open" && prev.Draft && !pr.Draft {
		acts = append(acts, "ready_for_review")
	}
	if prev.State == "closed" && pr.State == "open" {
		acts = append(acts, "opened") // reopen: polling cannot tell it from open
	}
	if prev.State == "open" && pr.State == "closed" {
		acts = append(acts, "closed")
	}
	return acts
}

// emitPREvent publishes one pull_request event with the contracts §4 payload and D-9 actor
// attribution.
func (p *Poller) emitPREvent(ctx context.Context, t *tickState, pr domain.PullRequest, act string) error {
	var (
		att         attribution
		ok          bool
		headMessage string
	)
	if act == "synchronize" {
		// A push changes no body — the only body in sight is the pull request's, and it
		// carries the OPENING run's marker forever. Commit identity wins here, or every
		// later push on a PR attributes to run #1 and the depth chain never accumulates
		// (see attribution.go's header). The same read carries the head commit message the
		// loop guard scans for skip tokens (S27) — it rides the synchronize payload as
		// pr.head_commit_message.
		author, committer, message, cerr := p.forge.CommitEmails(ctx, t.creds, t.repo.Ref(), pr.HeadSHA)
		if cerr != nil {
			p.logger.Warn("github.poll: commit read for attribution failed",
				slog.Int("pr", pr.Number), slog.String("error", cerr.Error()))
		} else {
			headMessage = message
			// The trailer names the pushing run exactly; the emails name only the agent,
			// and the run is resolved from the branch below (newEvent → causeRun).
			att, ok = p.attributeTrailer(ctx, t.projectID, message, t.agents)
			if !ok {
				att, ok = attributeEmail(t.agents, author, committer)
			}
		}
	} else {
		// Comments, reviews and PR-open carry a marker written for that very activity.
		att, ok = attributeBody(pr.Body, t.agents)
	}
	if !ok {
		att, ok = attributeBranch(pr.HeadRef, t.template, t.agents)
	}
	if !ok && act == "synchronize" {
		// Last resort for a push whose commit could not be read and whose branch does not
		// match: the PR body names *an* agent, which is better than nothing for actor
		// suppression. Its run id is deliberately dropped — it is the opening run's.
		if att, ok = attributeBody(pr.Body, t.agents); ok {
			att.runID = ""
		}
	}

	occurred := pr.UpdatedAt
	// Polling only knows who *authored* the PR. For a push, a ready-for-review or a close —
	// activities DERIVED from state diffing — it cannot name the acting login, and it has no
	// user type to read either: the zero forgeUser leaves both empty, which keeps the event
	// `external` (see actorKindFor) rather than guessing the author. That is what stops an
	// unattributed agent push from resetting the depth counter.
	var who forgeUser
	if act == "opened" {
		who = forgeUser{login: pr.AuthorLogin, userType: pr.AuthorType}
		if !pr.CreatedAt.IsZero() {
			occurred = pr.CreatedAt
		}
	}
	e := p.newEvent(ctx, t, kindPullRequest, act, pr,
		dedupe(t.projectID, kindPullRequest, strconv.Itoa(pr.Number), act+":"+pr.HeadSHA),
		occurred, att, ok, who)
	prMap := p.prBody(pr, t)
	if headMessage != "" {
		prMap["head_commit_message"] = headMessage
	}
	e.Payload = mustPayload(map[string]any{
		"pr":    prMap,
		"repo":  repoBody(t.repo),
		"actor": actorBody(att, ok, who),
	})
	return p.publish(ctx, e)
}

// pollReviews lists reviews for every PR the pull listing touched this tick, behind the
// per-PR review cursor (§7 table row 3).
func (p *Poller) pollReviews(ctx context.Context, t *tickState) error {
	for _, pr := range t.touched {
		st := t.prState[int64(pr.Number)]
		revs, err := p.forge.ListReviews(ctx, t.creds, t.repo.Ref(), pr.Number)
		if err != nil {
			return err
		}
		sort.Slice(revs, func(i, j int) bool { return revs[i].SubmittedAt.Before(revs[j].SubmittedAt) })
		cursor := parseCursor(st.ReviewCursor)
		maxSeen := cursor
		for _, rev := range revs {
			state := strings.ToLower(rev.State)
			if state == "pending" || state == "dismissed" {
				continue // not submissions: pending is unfinished, dismissal is a retraction
			}
			if !cursor.IsZero() && rev.SubmittedAt.Before(cursor) {
				continue
			}
			if rev.SubmittedAt.After(maxSeen) {
				maxSeen = rev.SubmittedAt
			}
			att, ok := attributeBody(rev.Body, t.agents)
			who := forgeUser{login: rev.AuthorLogin, userType: rev.AuthorType}
			e := p.newEvent(ctx, t, kindReview, "submitted", pr,
				dedupe(t.projectID, kindReview, strconv.FormatInt(rev.ID, 10), ""),
				rev.SubmittedAt, att, ok, who)
			e.Payload = mustPayload(map[string]any{
				"review": map[string]any{
					"id":     strconv.FormatInt(rev.ID, 10),
					"author": rev.AuthorLogin,
					"state":  state,
					"body":   rev.Body,
				},
				"pr":    p.prBody(pr, t),
				"repo":  repoBody(t.repo),
				"actor": actorBody(att, ok, who),
			})
			if err := p.publish(ctx, e); err != nil {
				return err
			}
		}
		if maxSeen != cursor && !maxSeen.IsZero() {
			st.ReviewCursor = domain.FormatTime(maxSeen)
			if err := p.store.PollPRState().Upsert(ctx, &st); err != nil {
				return err
			}
			t.prState[st.Number] = st
		}
	}
	return nil
}

// pollComments handles both comment listings (§7 table rows 2 and 4): updated_at cursor with
// etag; only comments *created* inside the window emit — an edit reappears in the listing but
// its created_at is old, and `created` is the only activity type polling can honestly derive.
func (p *Poller) pollComments(ctx context.Context, t *tickState, resource string) error {
	cur, err := p.store.PollCursors().Get(ctx, t.projectID, resource)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	since := parseCursor(cur.Cursor)
	es := &etagState{send: cur.Etag}
	ectx := withEtag(ctx, es)

	var (
		comments []domain.Comment
		kind     string
	)
	if resource == resReviewComments {
		kind = kindReviewComment
		comments, err = p.forge.ListReviewComments(ectx, t.creds, t.repo.Ref(), since)
	} else {
		kind = kindIssueComment
		comments, err = p.forge.ListIssueComments(ectx, t.creds, t.repo.Ref(), since)
	}
	etag, notModified := es.Result()
	if notModified {
		return p.touchCursor(ctx, t, cur, cur.Cursor, etag)
	}
	if err != nil {
		return err
	}

	maxUpdated := since
	for _, cm := range comments {
		if cm.UpdatedAt.After(maxUpdated) {
			maxUpdated = cm.UpdatedAt
		}
		if cm.CreatedAt.Before(since) {
			continue // an edit of an old comment
		}
		body := map[string]any{
			"id":     strconv.FormatInt(cm.ID, 10),
			"author": cm.AuthorLogin,
			"body":   cm.Body,
		}
		if kind == kindReviewComment {
			body["path"] = cm.Path
			body["line"] = cm.Line
		}
		att, ok := attributeBody(cm.Body, t.agents)
		who := forgeUser{login: cm.AuthorLogin, userType: cm.AuthorType}
		pr, hasPR := t.findPR(int64(cm.SubjectNumber))
		e := p.newEvent(ctx, t, kind, "created", pr,
			dedupe(t.projectID, kind, strconv.FormatInt(cm.ID, 10), ""),
			cm.CreatedAt, att, ok, who)
		payload := map[string]any{
			"comment": body,
			"repo":    repoBody(t.repo),
			"actor":   actorBody(att, ok, who),
		}
		if hasPR {
			payload["pr"] = p.prBody(pr, t)
		} else {
			n := int64(cm.SubjectNumber)
			e.SubjectNumber = &n
			e.SubjectBranch = nil
			if kind == kindIssueComment {
				// A review comment always sits on a PR; an issue comment whose subject the
				// pull listing did not touch this tick may be a plain issue — polling
				// cannot tell, so the subject stays "issue" and no pr sub-object is faked.
				e.SubjectKind = "issue"
			}
		}
		e.Payload = mustPayload(payload)
		if err := p.publish(ctx, e); err != nil {
			return err
		}
	}
	cursor := cur.Cursor
	if !maxUpdated.IsZero() {
		cursor = domain.FormatTime(maxUpdated)
	}
	return p.touchCursor(ctx, t, cur, cursor, etag)
}

// pollChecks lists check suites for every open PR head and emits `completed` for suites that
// finished inside the cursor window (§7 table row 5). Attribution rides the head branch: CI
// finishing on an agent's branch is causally the agent's push.
func (p *Poller) pollChecks(ctx context.Context, t *tickState) error {
	if t.prState == nil {
		st, err := p.store.PollPRState().ForProject(ctx, t.projectID)
		if err != nil {
			return err
		}
		t.prState = st
	}
	cur, err := p.store.PollCursors().Get(ctx, t.projectID, resCheckSuites)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	since := parseCursor(cur.Cursor)
	maxSeen := since

	numbers := make([]int64, 0, len(t.prState))
	for n, st := range t.prState {
		if st.State == "open" {
			numbers = append(numbers, n)
		}
	}
	sort.Slice(numbers, func(i, j int) bool { return numbers[i] < numbers[j] })

	for _, n := range numbers {
		st := t.prState[n]
		suites, err := p.forge.ListCheckSuites(ctx, t.creds, t.repo.Ref(), st.HeadSHA)
		if err != nil {
			return err
		}
		pr, hasPR := t.findPR(n)
		for _, s := range suites {
			if s.Status != "completed" {
				continue
			}
			if !s.UpdatedAt.IsZero() && s.UpdatedAt.Before(since) {
				continue
			}
			if s.UpdatedAt.After(maxSeen) {
				maxSeen = s.UpdatedAt
			}
			branch := s.HeadBranch
			if branch == "" && hasPR {
				branch = pr.HeadRef
			}
			att, ok := attributeBranch(branch, t.template, t.agents)
			// A check suite's "login" is the CI app's name, not a forge user: no user type,
			// so an unattributed suite stays `external`. CI is a machine, and a machine's
			// verdict must not reset a chain's depth counter.
			who := forgeUser{login: s.App}
			occurred := s.UpdatedAt
			if occurred.IsZero() {
				occurred = t.now
			}
			e := p.newEvent(ctx, t, kindCheckSuite, "completed", pr,
				dedupe(t.projectID, kindCheckSuite, strconv.FormatInt(s.ID, 10),
					s.Status+":"+s.Conclusion),
				occurred, att, ok, who)
			if !hasPR {
				e.SubjectNumber = &n
				b := branch
				if b != "" {
					e.SubjectBranch = &b
				}
			}
			payload := map[string]any{
				"check": map[string]any{
					"suite_id":   strconv.FormatInt(s.ID, 10),
					"name":       s.App,
					"conclusion": s.Conclusion,
					"url":        s.URL,
				},
				"repo":  repoBody(t.repo),
				"actor": actorBody(att, ok, who),
			}
			if hasPR {
				payload["pr"] = p.prBody(pr, t)
			}
			e.Payload = mustPayload(payload)
			if err := p.publish(ctx, e); err != nil {
				return err
			}
		}
	}
	cursor := cur.Cursor
	if !maxSeen.IsZero() {
		cursor = domain.FormatTime(maxSeen)
	}
	return p.touchCursor(ctx, t, cur, cursor, "")
}

// --------------------------------------------------------------------------- helpers -----

// forgeUser is the API-reported acting identity behind one event: the login, and the forge's
// own answer to "person or machine" (GitHub's `user.type`). Both are empty for an event the
// poller DERIVES from state diffing — a push seen only as a head-sha change, a close, a
// ready-for-review — where no endpoint names who acted.
type forgeUser struct {
	login    string
	userType string
}

// actorKindFor is the actor_kind of an event whose actor did NOT attribute to one of our
// agents (D-9 marker, commit identity, branch template).
//
// GitHub reports `user.type` as "User" or "Bot" on reviews, comments, issues and pull
// requests, and that is the honest reading of it: a non-agent "User" is a person, so the
// event is `human` and the loop guard's depth reset (architecture §9) fires for it. Anything
// else — "Bot", an unrecognized type, or no reported type at all (a derived push) — stays
// `external`.
//
// The default is deliberately not human. An unattributed agent push relabelled human would
// reset the depth counter on the agent's own chain, which weakens the very guard the reset
// exists to serve. `external` says what we actually know: someone who is not one of our
// agents, and not demonstrably a person.
//
// What this does NOT claim is identity: a GitHub login still does not map to a workspace user
// (D-9, S25), so a human forge event carries actor_login and no actor_id.
func actorKindFor(who forgeUser) domain.ActorKind {
	if who.userType == domain.UserTypeUser {
		return domain.ActorHuman
	}
	return domain.ActorExternal
}

// newEvent fills the envelope every poll event shares. The payload is the caller's.
func (p *Poller) newEvent(ctx context.Context, t *tickState, kind, act string, pr domain.PullRequest,
	dedupeKey string, occurred time.Time, att attribution, attributed bool, who forgeUser,
) domain.Event {
	pid := t.projectID
	e := domain.Event{
		ProjectID:    &pid,
		Source:       pollSourceID,
		Kind:         kind,
		ActivityType: act,
		ActorKind:    actorKindFor(who),
		SubjectKind:  "pr",
		DedupeKey:    dedupeKey,
		OccurredAt:   domain.FormatTime(occurred),
	}
	if pr.Number != 0 {
		n := int64(pr.Number)
		e.SubjectNumber = &n
		if pr.HeadRef != "" {
			b := pr.HeadRef
			e.SubjectBranch = &b
		}
	}
	if who.login != "" {
		l := who.login
		e.ActorLogin = &l
	}
	if attributed && att.agent != nil {
		e.ActorKind = domain.ActorAgent
		id := att.agent.ID
		e.ActorID = &id
		if att.runID != "" {
			r := att.runID
			e.CauseRunID = &r
		} else {
			e.CauseRunID = p.causeRun(ctx, t.projectID, att.agent.ID, pr.HeadRef, occurred)
		}
	}
	return e
}

// publish emits one event. The Emit implementation is idempotent on DedupeKey (contracts
// §2.1); a duplicate is the cursors' overlap window doing its job, not an error.
func (p *Poller) publish(ctx context.Context, e domain.Event) error {
	p.mu.Lock()
	emit := p.emit
	p.mu.Unlock()
	if emit == nil {
		return errors.New("github.poll: emit not wired")
	}
	return emit(ctx, e)
}

// touchCursor persists a cursor row's new position (keeping baseline_done) and stamps
// last_polled_at.
func (p *Poller) touchCursor(ctx context.Context, t *tickState, cur domain.PollCursor,
	cursor, etag string,
) error {
	nowStr := domain.FormatTime(t.now)
	if etag == "" {
		etag = cur.Etag
	}
	c := domain.PollCursor{
		ProjectID: t.projectID, Resource: cur.Resource, Cursor: cursor, Etag: etag,
		BaselineDone: true, LastPolledAt: &nowStr,
	}
	if cur.Resource == "" {
		return errors.New("github.poll: cursor row without a resource")
	}
	return p.store.PollCursors().Upsert(ctx, &c)
}

// prBody renders the contracts §4 `pr` sub-object. author_kind is derived from the PR's own
// D-9 signals (body marker, head branch) — the shared-PAT login never identifies the agent.
func (p *Poller) prBody(pr domain.PullRequest, t *tickState) map[string]any {
	authorKind := "human"
	if _, ok := attributeBody(pr.Body, t.agents); ok {
		authorKind = "agent"
	} else if _, ok := attributeBranch(pr.HeadRef, t.template, t.agents); ok {
		authorKind = "agent"
	}
	labels := pr.Labels
	if labels == nil {
		labels = []string{}
	}
	return map[string]any{
		"number":        pr.Number,
		"title":         pr.Title,
		"author":        pr.AuthorLogin,
		"author_kind":   authorKind,
		"branch":        pr.HeadRef,
		"base":          pr.BaseRef,
		"draft":         pr.Draft,
		"merged":        pr.Merged,
		"state":         pr.State,
		"additions":     pr.Additions,
		"deletions":     pr.Deletions,
		"files_changed": pr.ChangedFiles,
		"labels":        labels,
		"body":          pr.Body,
		"url":           pr.URL,
	}
}

// repoBody renders the contracts §4 `repo` sub-object.
func repoBody(rp domain.Repo) map[string]any {
	branch := ""
	if rp.DefaultBranch != nil {
		branch = *rp.DefaultBranch
	}
	return map[string]any{"owner": rp.Owner, "name": rp.Name, "default_branch": branch}
}

// actorBody renders the contracts §4 `actor` sub-object. Its kind agrees with the event's
// actor_kind column by construction — the `actor.is_human` / `actor.is_agent` operators read
// THIS object, not the column (internal/service/triggers/conditions.go), so the two must not
// be allowed to drift.
func actorBody(att attribution, attributed bool, who forgeUser) map[string]any {
	out := map[string]any{
		"kind":  string(actorKindFor(who)),
		"login": who.login,
		"agent": "",
	}
	if attributed && att.agent != nil {
		out["kind"] = string(domain.ActorAgent)
		out["agent"] = att.agent.Name
	}
	return out
}

// findPR returns this tick's enriched PR by number, when the pull listing touched it.
func (t *tickState) findPR(number int64) (domain.PullRequest, bool) {
	for _, pr := range t.touched {
		if int64(pr.Number) == number {
			return pr, true
		}
	}
	return domain.PullRequest{}, false
}

// dedupe is the §7 deterministic key: sha256(project|resource|id|discriminator).
func dedupe(projectID, resource, id, discriminator string) string {
	sum := sha256.Sum256([]byte(projectID + "|" + resource + "|" + id + "|" + discriminator))
	return hex.EncodeToString(sum[:])
}

// parseCursor reads a stored RFC3339 cursor; empty or malformed yields the zero time.
func parseCursor(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	ts, err := domain.ParseTime(s)
	if err != nil {
		return time.Time{}
	}
	return ts
}

func maxPRUpdated(prs []domain.PullRequest) time.Time {
	var mx time.Time
	for _, pr := range prs {
		if pr.UpdatedAt.After(mx) {
			mx = pr.UpdatedAt
		}
	}
	return mx
}

func mustPayload(v map[string]any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}
