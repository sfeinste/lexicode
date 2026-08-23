// preserve.go is the orchestrator-owned push (§10.5, and the D-9 amendment in
// plan/00-decisions.md). It is the only place in the product that pushes a run's work.
//
// Why the orchestrator and not the agent: the container never holds the repository
// credential. The clone step needs it to read a private repository, and points `origin` at a
// tokenless URL the moment the fetch is done, so from the agent's first instruction onward
// there is nothing in its environment, in `.git/config` or in `git remote -v` that authorizes
// a write. The token comes back here, after the agent process has exited, for exactly one
// command, in exactly one exec's environment.
//
// Two things follow from owning the push, and both are done here:
//
//   - The report is honest. A push either happened or it did not, and the run's terminal
//     message and its `partial_work` output say which. The previous version ran
//     `git push … || true` and reported success unconditionally.
//   - The commits can be checked before they leave. The orchestrator's own wip commit gets
//     the D-9 trailer and the agent's git identity set authoritatively (no hook, no
//     repository config — both live inside the container, where a root agent can edit them).
//     Agent-authored commits are verified and recorded, never rewritten.
package sched

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// baseSHAFile mirrors the docker adapter's constant of the same name: the file its clone step
// writes the run's branch point into, inside `.git/` so it is never committed. kernel → module
// is a forbidden import edge (architecture §2.1), so — as with the OAuth variable name and the
// scaffolding paths elsewhere in this codebase — the string IS the protocol.
const baseSHAFile = ".git/lexicode-base"

// runTrailerKey is the D-9 commit trailer's key.
const runTrailerKey = "Lexicode-Run"

// preserveOutcome is what the teardown push actually did. Every field is a fact, and the
// terminal message and the run_outputs row are rendered from it — never from an assumption.
type preserveOutcome struct {
	// attempted is false when there was no push path at all: no container, no branch, or an
	// agent without the push_branches grant.
	attempted bool
	// branch is the branch that was (or would have been) pushed — the branch the agent was
	// actually on when it exited, which is not always the run's own minted branch: a run
	// whose job is "address the review" checks out the pull request's branch.
	branch string
	// committed is true when the orchestrator made a wip commit of an uncommitted tree.
	committed bool
	// pushed is true only when git push reported success.
	pushed bool
	// nothing is true when there was nothing to commit and nothing to push: the workspace is
	// exactly as it was cloned.
	nothing bool
	// failure is why nothing reached the remote, in a sentence fit for a user. Empty when
	// nothing went wrong.
	failure string
}

// preserveAndPush commits whatever is uncommitted, pushes the branch, and verifies the D-9
// attribution of what it is pushing. It runs before teardown, while the container is alive.
func (s *Scheduler) preserveAndPush(ctx context.Context, run domain.Run, agent domain.Agent, inst ports.Instance, branch string) preserveOutcome {
	if inst == nil || branch == "" {
		return preserveOutcome{}
	}
	// push_branches is a grant, and it gates the orchestrator's push exactly as it used to
	// gate the agent's: a reviewer agent's container produces no branch on the remote.
	if !agent.Permissions.PushBranches {
		return preserveOutcome{}
	}
	out := preserveOutcome{attempted: true, branch: branch}

	var auth PushAuth
	if s.opts.Pushes != nil {
		var err error
		auth, err = s.opts.Pushes.ForRun(ctx, run)
		if err != nil {
			// Push anyway: the remote may need no credential (a `file://` fixture), and if
			// it does the push fails and says so, which is better than guessing here.
			s.logger.Error("sched: could not resolve the run's push credential",
				slog.String("run", run.ID), slog.String("error", err.Error()))
		}
	}

	red := &redactor{}
	red.add(auth.Secrets...)

	env := map[string]string{
		"LEXICODE_BRANCH":      branch,
		"LEXICODE_BASE_BRANCH": auth.BaseBranch,
		"LEXICODE_WIP_MESSAGE": wipMessage(run),
		"LEXICODE_TRAILER":     "^" + runTrailerKey + ": " + run.ID + "$",
	}
	// The orchestrator's own commit is attributed authoritatively, here, in the environment
	// git resolves identity from first. The repository-local config and the commit-msg hook
	// that carry D-9 for the agent both live inside the container, where a root agent can
	// edit them; neither is trusted for the commit the orchestrator makes.
	env["GIT_AUTHOR_NAME"] = agent.GitAuthorName
	env["GIT_AUTHOR_EMAIL"] = agent.GitAuthorEmail
	env["GIT_COMMITTER_NAME"] = agent.GitAuthorName
	env["GIT_COMMITTER_EMAIL"] = agent.GitAuthorEmail
	for k, v := range gitConfigEnv(auth.Env) {
		env[k] = v
	}

	code, stdout, err := s.execCollect(ctx, inst, []string{"/bin/sh", "-c", preserveScript}, env)
	if err != nil {
		// This is the path that used to vanish: the exec never started, the function
		// returned false, and the run's message simply had no branch clause. The reason is
		// now on the run.
		out.failure = "the run's container could not be reached to preserve its work: " + red.clean(err.Error())
		s.logger.Error("sched: artifact push exec failed",
			slog.String("run", run.ID), slog.String("error", err.Error()))
		s.appendWarningActivity(ctx, run.ID, "Partial work could not be preserved", map[string]any{
			"warning": "artifact_push_unreachable",
			"branch":  branch,
			"error":   red.clean(err.Error()),
		})
		return out
	}

	report := parsePreserveReport(red.clean(stdout))
	if report.branch != "" {
		out.branch = report.branch
	}
	out.committed = report.committed

	switch {
	case report.refusedBaseBranch:
		out.failure = fmt.Sprintf("the agent's work is on `%s`, the repository's default branch; "+
			"Lexicode does not push there", out.branch)
		s.reportPreserveFailure(ctx, run, out.branch,
			"Refused to push to the default branch", "push_refused_default_branch", out.failure)
	case report.commitFailed:
		out.failure = "the uncommitted work in the workspace could not be committed"
		s.reportPreserveFailure(ctx, run, out.branch,
			"Partial work could not be committed", "artifact_commit_failed", out.failure)
	case report.nothing:
		out.nothing = true
	case report.pushed:
		out.pushed = true
	case report.pushFailed:
		out.failure = "git push failed: " + firstLine(report.pushError)
		s.reportPreserveFailure(ctx, run, out.branch,
			"The run's branch could not be pushed", "artifact_push_failed", out.failure)
	default:
		// The script ran but said nothing recognizable — a broken shell, a killed exec.
		out.failure = fmt.Sprintf("the preserve step exited %d without reporting an outcome", code)
		s.reportPreserveFailure(ctx, run, out.branch,
			"The run's branch could not be pushed", "artifact_push_unreported", out.failure)
	}

	s.verifyAttribution(ctx, run, agent, out.branch, report)
	return out
}

// reportPreserveFailure makes an unsuccessful teardown push LOUD, in both of the places
// somebody looks: the process log and the run's own activity feed.
//
// It exists because of a specific afternoon. A push that did not land left a clause in the
// run's terminal message and absolutely nothing else — no log line, no activity — so a CI
// run whose only visible symptom was "the branch is not in the remote" could be grepped for
// push, preserve, denied and warning and come back empty. A push that fails is the
// difference between a run whose work exists and a run whose work is gone; it does not get
// to be quiet about it. The one outcome deliberately left silent is `nothing`, which is not
// a failure: there was no work to push.
//
// detail is already redacted — it is built from the redactor-cleaned script output.
func (s *Scheduler) reportPreserveFailure(ctx context.Context, run domain.Run, branch, title, warning, detail string) {
	s.logger.Error("sched: "+title,
		slog.String("run", run.ID),
		slog.String("branch", branch),
		slog.String("warning", warning),
		slog.String("detail", detail))
	s.appendWarningActivity(ctx, run.ID, title, map[string]any{
		"warning": warning,
		"branch":  branch,
		"error":   detail,
	})
}

// preserveScript is the whole teardown push, in one exec. It reports through `lexicode:`
// lines rather than through its exit code, so a failure is a fact the orchestrator records
// rather than an exit status it has to guess the meaning of.
//
// It never runs a hook (`--no-verify`) and, through gitConfigEnv, never inherits the
// container's `core.hooksPath`: both hook paths are inside the workspace, which the agent
// could have written to.
const preserveScript = `set -u
say() { printf 'lexicode: %s\n' "$*"; }

branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo HEAD)
if [ "$branch" = HEAD ] || [ -z "$branch" ]; then branch="$LEXICODE_BRANCH"; fi
say "branch $branch"

if [ -n "$LEXICODE_BASE_BRANCH" ] && [ "$branch" = "$LEXICODE_BASE_BRANCH" ]; then
  say "refused-base-branch"
  exit 0
fi

if [ -n "$(git status --porcelain 2>/dev/null)" ]; then
  if git add -A && git commit --no-verify -q -m "$LEXICODE_WIP_MESSAGE"; then
    say "committed"
  else
    say "commit-failed"
    exit 0
  fi
fi

base=$(cat ` + baseSHAFile + ` 2>/dev/null || true)
head=$(git rev-parse HEAD 2>/dev/null || true)
if [ -n "$base" ]; then range="$base..HEAD"; else range="HEAD"; fi
# What this push actually adds: everything since the branch point that the remote does not
# already have. The clone step fetched remote-tracking refs while it still had the
# credential, so "--not --remotes=origin" excludes commits an earlier run already pushed to
# a shared branch — a run that addresses review feedback must not be told off for the
# commits it inherited.
git log --format='lexicode: commit %H %ae' "$range" --not --remotes=origin 2>/dev/null || true
git log --format='lexicode: trailed %H' --grep="$LEXICODE_TRAILER" "$range" --not --remotes=origin 2>/dev/null || true

if [ -n "$base" ] && [ "$base" = "$head" ]; then
  say "nothing"
  exit 0
fi

if out=$(git push origin "HEAD:refs/heads/$branch" 2>&1); then
  say "pushed"
else
  say "push-failed"
  printf 'lexicode: error %s\n' "$(printf '%s' "$out" | tr '\n' ' ' | cut -c1-500)"
fi
exit 0
`

// preserveReport is preserveScript's output, parsed.
type preserveReport struct {
	branch            string
	committed         bool
	commitFailed      bool
	refusedBaseBranch bool
	nothing           bool
	pushed            bool
	pushFailed        bool
	pushError         string
	// commits is every commit the push would carry, oldest last (git log order), as
	// "<sha> <author email>".
	commits []pushedCommit
	// trailed is the subset of those whose message carries this run's D-9 trailer.
	trailed map[string]bool
}

type pushedCommit struct {
	SHA         string `json:"sha"`
	AuthorEmail string `json:"author_email"`
}

func parsePreserveReport(stdout string) preserveReport {
	r := preserveReport{trailed: map[string]bool{}}
	sc := bufio.NewScanner(strings.NewReader(stdout))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		rest, ok := strings.CutPrefix(line, "lexicode: ")
		if !ok {
			continue
		}
		verb, arg, _ := strings.Cut(rest, " ")
		switch verb {
		case "branch":
			r.branch = arg
		case "committed":
			r.committed = true
		case "commit-failed":
			r.commitFailed = true
		case "refused-base-branch":
			r.refusedBaseBranch = true
		case "nothing":
			r.nothing = true
		case "pushed":
			r.pushed = true
		case "push-failed":
			r.pushFailed = true
		case "error":
			r.pushError = arg
		case "commit":
			sha, email, _ := strings.Cut(arg, " ")
			r.commits = append(r.commits, pushedCommit{SHA: sha, AuthorEmail: email})
		case "trailed":
			r.trailed[arg] = true
		}
	}
	return r
}

// verifyAttribution is the D-9 check the orchestrator can make now that it owns the push: it
// reads the commits it is about to send and records the ones that do not carry this run's
// trailer, or that were authored under an identity other than the agent's.
//
// It records; it does not rewrite. Rewriting an agent's history at teardown would change what
// the human reviews, and a wrong guess would be unrecoverable. The trailer is what the poller
// reads for depth attribution (loop protection), so a missing one is a level-1 warning on the
// run rather than a line in a log file nobody reads.
func (s *Scheduler) verifyAttribution(ctx context.Context, run domain.Run, agent domain.Agent, branch string, r preserveReport) {
	var untrailed []pushedCommit
	var misattributed []pushedCommit
	for _, c := range r.commits {
		if !r.trailed[c.SHA] {
			untrailed = append(untrailed, c)
		}
		if agent.GitAuthorEmail != "" && c.AuthorEmail != "" && c.AuthorEmail != agent.GitAuthorEmail {
			misattributed = append(misattributed, c)
		}
	}
	if len(untrailed) == 0 && len(misattributed) == 0 {
		return
	}
	title := fmt.Sprintf("%d commit(s) on `%s` are missing the %s trailer",
		len(untrailed), branch, runTrailerKey)
	if len(untrailed) == 0 {
		title = fmt.Sprintf("%d commit(s) on `%s` were authored under another identity",
			len(misattributed), branch)
	}
	s.appendWarningActivity(ctx, run.ID, title, map[string]any{
		"warning":               "commit_attribution",
		"branch":                branch,
		"expected_trailer":      runTrailerKey + ": " + run.ID,
		"expected_author_email": agent.GitAuthorEmail,
		"missing_trailer":       untrailed,
		"unexpected_author":     misattributed,
		"detail": "The orchestrator does not rewrite an agent's commits. Loop protection " +
			"attributes a push by this trailer, so commits without it are attributed by " +
			"author email or not at all.",
	})
}

// wipMessage is the orchestrator's own commit message, D-9 trailer included. The trailer is
// written into the message rather than left to the container's commit-msg hook: the hook
// lives in the workspace, and the commit runs with hooks disabled for that reason.
func wipMessage(run domain.Run) string {
	summary := run.CurrentStep
	if summary == "" {
		summary = fmt.Sprintf("run #%d", run.Seq)
	}
	return fmt.Sprintf("wip: %s [lexicode run %s]\n\n%s: %s\n",
		summary, run.ID, runTrailerKey, run.ID)
}

// gitConfigEnv renders the credential as git's config-via-environment, replacing whatever the
// container's own GIT_CONFIG_* variables say.
//
// Replacing rather than extending is the point. The container carries GIT_CONFIG_COUNT=2 for
// `commit.template` and `core.hooksPath`, both pointing into the workspace — files a root
// agent can rewrite. The orchestrator's one command must not execute or interpolate anything
// the agent could have edited, so it declares its own complete set: the credential when there
// is one, and an explicit count of zero when there is not.
func gitConfigEnv(auth map[string]string) map[string]string {
	out := map[string]string{"GIT_CONFIG_COUNT": "0"}
	// Keys arrive already numbered from the credential source; count them and pass them
	// through so the source, not this function, decides what config the push carries.
	n := 0
	for k, v := range auth {
		out[k] = v
		if strings.HasPrefix(k, "GIT_CONFIG_KEY_") {
			n++
		}
	}
	if n > 0 {
		out["GIT_CONFIG_COUNT"] = fmt.Sprint(n)
	}
	return out
}

// BasicAuthHeader renders a token as the `http.extraheader` value git sends: the form GitHub
// itself documents for tokenized HTTPS access, and the only one that keeps the credential out
// of both the command line and the repository config. Exported because the credential source
// that builds the PushAuth lives above the kernel and this is the shape both sides agree on.
func BasicAuthHeader(user, token string) string {
	return "AUTHORIZATION: basic " +
		base64.StdEncoding.EncodeToString([]byte(user+":"+token))
}

// execCollect runs argv to completion and returns its exit code and combined output. Stdout
// and stderr are drained CONCURRENTLY: the docker exec demultiplexer feeds both through
// unbuffered pipes, so reading one to EOF before touching the other deadlocks the moment the
// ignored stream produces a frame (git push reports on stderr).
func (s *Scheduler) execCollect(ctx context.Context, inst ports.Instance, argv []string, env map[string]string) (int, string, error) {
	streams, err := inst.Exec(ctx, argv, ports.ExecOpts{Env: env})
	if err != nil {
		return -1, "", err
	}
	if streams.Stdin != nil {
		_ = streams.Stdin.Close()
	}
	var (
		mu     sync.Mutex
		buf    strings.Builder
		drains sync.WaitGroup
	)
	for _, r := range []io.Reader{streams.Stdout, streams.Stderr} {
		if r == nil {
			continue
		}
		drains.Add(1)
		go func(r io.Reader) {
			defer drains.Done()
			b, _ := io.ReadAll(io.LimitReader(r, 256*1024))
			_, _ = io.Copy(io.Discard, r)
			mu.Lock()
			buf.Write(b)
			mu.Unlock()
		}(r)
	}
	drains.Wait()
	code := 0
	if streams.Wait != nil {
		var werr error
		code, werr = streams.Wait()
		if werr != nil {
			return code, buf.String(), werr
		}
	}
	return code, buf.String(), nil
}

// appendWarningActivity records a level-1 warning on a run — the tier the transcript surfaces
// by default, for the things a human has to know about the run's result.
func (s *Scheduler) appendWarningActivity(ctx context.Context, runID, title string, payload map[string]any) {
	a := domain.Activity{
		RunID: runID, Type: domain.ActivitySystem, Level: 1, GroupKey: "system",
		Title: truncate(title, 200), Payload: mustJSON(payload),
		Attempt: 1, CreatedAt: s.now(),
	}
	if err := s.st.Activities().AppendNext(ctx, &a); err != nil {
		s.logger.Error("sched: warning activity append failed",
			slog.String("run", runID), slog.String("error", err.Error()))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncate(strings.TrimSpace(s), 300)
}
