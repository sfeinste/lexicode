// wiki.go is the `wiki` ContextProvider (architecture §11: priority 20): the knowledge base
// steering runs. Scope decides how a live page reaches a run —
//
//   - `always`  → injected into every run, reason exactly "always";
//   - `paths`   → injected when one of the page's globs matches one of the run's changed
//     paths, reason "matched path <path>" naming an actual matched changed path;
//   - `auto`    → injected when a title/tag keyword appears in the run's task summary,
//     reason `retrieved for "<keyword>"` naming the matched keyword;
//   - `manual` and `never` → never auto-injected (a human pastes manual pages by hand).
//
// A dry resolve (the agent detail preview) carries no ticket, no changed paths and no task
// summary, so only `always` pages appear — which is exactly the "what EVERY run of this
// agent sees" promise. Token counts are the page's stored token_estimate, the same number
// the wiki tree and the budget endpoint render, so all three surfaces agree.
package contextmod

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// WikiProvider yields wiki pages per their agent_scope.
type WikiProvider struct {
	st *store.Store
}

// NewWikiProvider builds the provider over st.
func NewWikiProvider(st *store.Store) *WikiProvider { return &WikiProvider{st: st} }

// ID implements ports.ContextProvider.
func (p *WikiProvider) ID() string { return "wiki" }

// Priority implements ports.ContextProvider.
func (p *WikiProvider) Priority() int { return 20 }

// Resolve implements ports.ContextProvider. Output order is deterministic: `always` pages
// first, then `paths` matches, then `auto` retrievals — each group in tree order (the order
// ForProject returns).
func (p *WikiProvider) Resolve(ctx context.Context, req ports.ContextRequest) ([]ports.ContextItem, error) {
	pages, err := p.st.Wiki().ForProject(ctx, req.ProjectID)
	if err != nil {
		return nil, err
	}

	var always, byPath, auto []ports.ContextItem
	for _, pg := range pages {
		if pg.State != domain.WikiLive || pg.ArchivedAt != nil {
			continue
		}
		switch pg.AgentScope {
		case domain.ScopeAlways:
			always = append(always, wikiItem(pg, "always"))
		case domain.ScopePaths:
			if matched, ok := firstGlobMatch(pg.ScopePaths, req.ChangedPaths); ok {
				byPath = append(byPath, wikiItem(pg, "matched path "+matched))
			}
		case domain.ScopeAuto:
			if kw, ok := keywordMatch(pg, req.TaskSummary); ok {
				auto = append(auto, wikiItem(pg, fmt.Sprintf("retrieved for %q", kw)))
			}
		case domain.ScopeManual, domain.ScopeNever:
			// Never auto-injected: `manual` is for humans to paste deliberately, `never`
			// is an explicit opt-out.
		}
	}
	out := make([]ports.ContextItem, 0, len(always)+len(byPath)+len(auto))
	out = append(out, always...)
	out = append(out, byPath...)
	out = append(out, auto...)
	return out, nil
}

// wikiItem shapes one page as a context item. Tokens is the page's stored token_estimate —
// the number every other wiki surface shows — not a re-estimate.
func wikiItem(pg domain.WikiPage, reason string) ports.ContextItem {
	return ports.ContextItem{
		SourceKind: "wiki",
		SourceRef:  pg.Slug,
		Title:      pg.Title,
		Reason:     reason,
		Body:       pg.Body,
		Tokens:     int(pg.TokenEstimate),
		Injected:   true,
	}
}

// firstGlobMatch returns the first changed path that any of the page's globs matches. The
// changed path (not the glob) is what the reason names — "matched path infra/deploy.ts" —
// because the panel answers "why is this page here" with the concrete file that pulled it in.
func firstGlobMatch(globs, changed []string) (string, bool) {
	for _, c := range changed {
		for _, g := range globs {
			if pathGlobMatch(g, c) {
				return c, true
			}
		}
	}
	return "", false
}

// pathGlobMatch matches one path glob against one repo-relative path. The dialect is the
// trigger engine's stdlib path.Match per segment (`*` never crosses `/`), extended with
// `**` as a full segment matching any number of segments — the form the bootstrap doc
// detection already proposes for .cursor/rules globs ("web/**"). A malformed pattern
// matches nothing.
func pathGlobMatch(pattern, p string) bool {
	return segsMatch(strings.Split(pattern, "/"), strings.Split(p, "/"))
}

func segsMatch(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		// `**` matches zero or more leading segments.
		for skip := 0; skip <= len(segs); skip++ {
			if segsMatch(pat[1:], segs[skip:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], segs[0])
	if err != nil || !ok {
		return false
	}
	return segsMatch(pat[1:], segs[1:])
}

// keywordMatch decides whether an `auto` page is relevant to the task summary: some keyword
// from the page's title or tags appears as a whole word in the summary. The matched keyword
// (lowercased) is returned for the reason string. Title words shorter than 4 characters and
// common stopwords are not keywords — "the" retrieving every page would make `auto` noise.
// Tags always count, whatever their length: a tag is a deliberate label.
func keywordMatch(pg domain.WikiPage, summary string) (string, bool) {
	if strings.TrimSpace(summary) == "" {
		return "", false
	}
	words := tokenSet(summary)
	for _, tag := range pg.Tags {
		t := strings.ToLower(strings.TrimSpace(tag))
		if t == "" {
			continue
		}
		if strings.ContainsAny(t, " -_") {
			// Multi-word tag: match as a substring of the lowercased summary.
			if strings.Contains(strings.ToLower(summary), t) {
				return t, true
			}
			continue
		}
		if words[t] {
			return t, true
		}
	}
	for _, w := range tokenize(pg.Title) {
		if len(w) < 4 || titleStopwords[w] {
			continue
		}
		if words[w] {
			return w, true
		}
	}
	return "", false
}

// titleStopwords are common title words that carry no retrieval signal.
var titleStopwords = map[string]bool{
	"about": true, "from": true, "guide": true, "howto": true, "into": true,
	"notes": true, "over": true, "page": true, "that": true, "this": true,
	"what": true, "when": true, "with": true, "your": true,
}

// tokenize splits text into lowercased alphanumeric runs.
func tokenize(text string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, strings.ToLower(b.String()))
			b.Reset()
		}
	}
	for _, r := range text {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func tokenSet(text string) map[string]bool {
	set := map[string]bool{}
	for _, w := range tokenize(text) {
		set[w] = true
	}
	return set
}
