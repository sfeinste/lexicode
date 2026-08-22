package wiki

import (
	"context"
	"strings"

	"github.com/spruce/lexicode/internal/kernel/store"
)

// searchLimit caps one search response; search is navigation, not export.
const searchLimit = 25

// Search runs the FTS5 query behind `/` — the wiki's primary navigation (UI spec §5.6).
// User input is quoted term-by-term into FTS5 syntax (never passed raw: a stray `"` or `-`
// must not 500), with the final term matched as a prefix so results appear while typing.
func (s *Service) Search(ctx context.Context, projectKey, query string) ([]store.WikiSearchHit, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return nil, err
	}
	match := ftsQuery(query)
	if match == "" {
		return []store.WikiSearchHit{}, nil
	}
	return s.st.Wiki().Search(ctx, p.ID, match, searchLimit)
}

// ftsQuery turns free text into safe FTS5 syntax: each whitespace-separated term becomes a
// quoted token, the last one a prefix ("runbo"* finds runbook mid-word-typing). Empty input
// returns "".
func ftsQuery(q string) string {
	terms := strings.Fields(q)
	if len(terms) == 0 {
		return ""
	}
	parts := make([]string, len(terms))
	for i, t := range terms {
		parts[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
		if i == len(terms)-1 {
			parts[i] += "*"
		}
	}
	return strings.Join(parts, " ")
}
