package github

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Actor attribution (D-9, architecture §6.3): the poller resolves which agent caused an
// external event, in this order —
//
//  1. the marker comment `<!-- lexicode:actor=agent:<id> run=<run_id> -->` in a body
//     (covers comments, reviews and PR bodies; also yields the causing run directly),
//  2. the commit author/committer email against agents.git_author_email (covers pushes),
//  3. the head branch against the branch naming template (covers PR-open events).
//
// A hit sets Actor{kind: agent}; when the marker did not give the run, the causing run falls
// back to the agent's most recent run on the subject's branch — non-terminal preferred, else
// the most recent one if it ended within recentRunWindow (a terminal run that ended long ago
// is not "recent": the event was probably caused by something else touching the old branch).

// recentRunWindow bounds the terminal-run attribution fallback.
const recentRunWindow = 7 * 24 * time.Hour

// markerRe matches domain.Actor.Marker exactly. The format is load-bearing (D-9): change it
// and past events stop being attributable.
var markerRe = regexp.MustCompile(`<!-- lexicode:actor=agent:(\S+) run=(\S+) -->`)

// attribution is one resolved actor: the agent, and the causing run when known.
type attribution struct {
	agent *domain.Agent
	runID string // empty when only the agent resolved
}

// parseMarker extracts the D-9 marker from a body.
func parseMarker(body string) (agentID, runID string, ok bool) {
	m := markerRe.FindStringSubmatch(body)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// attributeBody resolves signal 1: the marker in a PR/review/comment body.
func attributeBody(body string, agents []domain.Agent) (attribution, bool) {
	agentID, runID, ok := parseMarker(body)
	if !ok {
		return attribution{}, false
	}
	for i := range agents {
		if agents[i].ID == agentID {
			return attribution{agent: &agents[i], runID: runID}, true
		}
	}
	// A marker naming an unknown agent is not an attribution — it may be stale or forged.
	return attribution{}, false
}

// attributeEmail resolves signal 2: a commit author/committer email against
// agents.git_author_email (case-insensitive).
func attributeEmail(agents []domain.Agent, emails ...string) (attribution, bool) {
	for i := range agents {
		want := strings.TrimSpace(agents[i].GitAuthorEmail)
		if want == "" {
			continue
		}
		for _, e := range emails {
			if e != "" && strings.EqualFold(e, want) {
				return attribution{agent: &agents[i]}, true
			}
		}
	}
	return attribution{}, false
}

// attributeBranch resolves signal 3: the head branch against the branch naming template.
func attributeBranch(branch, template string, agents []domain.Agent) (attribution, bool) {
	if branch == "" {
		return attribution{}, false
	}
	for i := range agents {
		if branchMatchesAgent(branch, template, agents[i].Name) {
			return attribution{agent: &agents[i]}, true
		}
	}
	return attribution{}, false
}

// defaultBranchTemplateFallback mirrors the workspace_settings schema default — the last
// resort when both the repo row and the workspace carry empty templates.
const defaultBranchTemplateFallback = "{agent}/{ticket-key}-{slug}"

// branchMatchesAgent reports whether branch was rendered from template for this agent: the
// template's literal text around {agent} becomes a prefix with the agent's slug substituted.
// For the default template "{agent}/{ticket-key}-{slug}" and agent "Dev" the prefix is
// "dev/". A template without {agent}, or where {agent} abuts the next placeholder with no
// literal separator, cannot distinguish agents and yields no match.
func branchMatchesAgent(branch, template, agentName string) bool {
	if strings.TrimSpace(template) == "" {
		template = defaultBranchTemplateFallback
	}
	i := strings.Index(template, "{agent}")
	if i < 0 {
		return false
	}
	pre := template[:i]
	if strings.ContainsRune(pre, '{') {
		return false // another placeholder before {agent}: the prefix is not literal
	}
	slug := agentSlug(agentName)
	if slug == "" {
		return false
	}
	rest := template[i+len("{agent}"):]
	j := strings.IndexByte(rest, '{')
	if j < 0 {
		// No further placeholder: the whole template is literal around the agent.
		return branch == pre+slug+rest
	}
	sep := rest[:j]
	if sep == "" {
		return false // {agent}{ticket-key}: the slug melts into the next value
	}
	return strings.HasPrefix(branch, pre+slug+sep)
}

// agentSlug folds an agent name the way the branch builder's slugify does for its common
// path: lowercase, ASCII letters and digits survive, everything else separates. (The builder
// additionally folds accented Latin letters; an agent named with such characters may not
// branch-match — the marker and email signals still cover it.)
func agentSlug(name string) string {
	var b strings.Builder
	pending := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pending {
				b.WriteByte('-')
				pending = false
			}
			b.WriteRune(r)
		} else if b.Len() > 0 {
			pending = true
		}
	}
	return b.String()
}

// causeRun resolves the run behind an agent-attributed event when the marker did not carry
// one: the agent's most recent run on the subject's branch, non-terminal preferred; a
// terminal run only counts while it ended within recentRunWindow of the event.
func (p *Poller) causeRun(ctx context.Context, projectID, agentID, branch string, occurredAt time.Time) *string {
	if branch == "" || p.store == nil {
		return nil
	}
	run, err := p.store.Runs().LatestForAgentOnBranch(ctx, projectID, agentID, branch)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			p.logger.Warn("github.poll: attribution run lookup failed", "error", err.Error())
		}
		return nil
	}
	if run.State.Terminal() {
		if run.EndedAt == nil {
			return nil
		}
		ended, err := domain.ParseTime(*run.EndedAt)
		if err != nil || occurredAt.Sub(ended) > recentRunWindow {
			return nil
		}
	}
	id := run.ID
	return &id
}
