// Package mentionparse is the one parser for the S12 mention wire format, shared by the
// tickets service (descriptions, comments) and the wiki service (page bodies). The Editor
// inserts an explicit token for every accepted autocomplete pick:
//
//	@[Display Name](user:01H…)  @[dev](agent:01H…)  @[API runbook](wiki:01H…)  @[PAY-14](ticket:01H…)
//
// The token is unambiguous (bare `@name` text never creates a linked mention — the wiki
// backlink pass owns *unlinked* mention detection), renders legibly as plain markdown, and
// survives copy/paste. Each service keeps its own target validation; parsing and paragraph
// extraction live here so the two can never drift.
package mentionparse

import (
	"regexp"
	"strings"
)

// Pattern matches one mention token. Mirrored client-side in
// web/src/components/Editor/mentionPattern.ts.
var Pattern = regexp.MustCompile(`@\[([^\]\n]+)\]\((user|agent|wiki|ticket):([A-Za-z0-9]+)\)`)

// Parsed is one token found in a body, with the paragraph that contains it.
type Parsed struct {
	Label   string
	Kind    string // user | agent | wiki | ticket
	ID      string
	Context string
}

// Parse extracts every mention token from a markdown body, in order. Context is the full
// containing paragraph (blank-line delimited) — the backlinks pane renders it (UI spec §5.6:
// a bare list of titles is useless).
func Parse(body string) []Parsed {
	matches := Pattern.FindAllStringSubmatchIndex(body, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]Parsed, 0, len(matches))
	for _, m := range matches {
		out = append(out, Parsed{
			Label:   body[m[2]:m[3]],
			Kind:    body[m[4]:m[5]],
			ID:      body[m[6]:m[7]],
			Context: ParagraphAround(body, m[0], m[1]),
		})
	}
	return out
}

// ParagraphAround returns the blank-line-delimited paragraph containing [start,end).
func ParagraphAround(body string, start, end int) string {
	pStart := 0
	if i := strings.LastIndex(body[:start], "\n\n"); i != -1 {
		pStart = i + 2
	}
	pEnd := len(body)
	if i := strings.Index(body[end:], "\n\n"); i != -1 {
		pEnd = end + i
	}
	return strings.TrimSpace(body[pStart:pEnd])
}

// RewriteWikiLabels replaces the label of every wiki token pointing at pageID with newLabel —
// the rename pass that keeps inbound links reading correctly. Everything else in the body is
// untouched. Returns the rewritten body and whether anything changed.
func RewriteWikiLabels(body, pageID, newLabel string) (string, bool) {
	changed := false
	out := Pattern.ReplaceAllStringFunc(body, func(tok string) string {
		m := Pattern.FindStringSubmatch(tok)
		if m == nil || m[2] != "wiki" || m[3] != pageID || m[1] == newLabel {
			return tok
		}
		changed = true
		return "@[" + newLabel + "](wiki:" + pageID + ")"
	})
	return out, changed
}

// MaskTokens blanks every mention token out of a body — same length, token bytes replaced
// with spaces — so a plain-text search cannot match text inside a token's label. Indices
// found in the masked string map 1:1 onto the original body.
func MaskTokens(body string) string {
	matches := Pattern.FindAllStringIndex(body, -1)
	if len(matches) == 0 {
		return body
	}
	b := []byte(body)
	for _, m := range matches {
		for i := m[0]; i < m[1]; i++ {
			if b[i] != '\n' {
				b[i] = ' '
			}
		}
	}
	return string(b)
}

// FindPlainOccurrence returns the byte range of the first case-insensitive occurrence of
// needle in body that is NOT inside a mention token, or (-1, -1). This is the "unlinked
// mention" detector: the page's title appearing as plain text, one click from becoming a
// real link.
func FindPlainOccurrence(body, needle string) (int, int) {
	if needle == "" {
		return -1, -1
	}
	masked := MaskTokens(body)
	// Sliding EqualFold window rather than ToLower+Index: lowering can change byte lengths
	// (Unicode dotted-İ and friends) and silently shift the offsets back into the original.
	for i := 0; i+len(needle) <= len(masked); i++ {
		if strings.EqualFold(masked[i:i+len(needle)], needle) {
			return i, i + len(needle)
		}
	}
	return -1, -1
}
