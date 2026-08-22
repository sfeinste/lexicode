package runs

import (
	"context"
	"fmt"
	"strings"
)

// defaultBranchTemplate mirrors the workspace_settings schema default; it is the last-resort
// fallback when both the repo and the workspace row carry empty templates (which the schema
// prevents, but the builder never renders an empty branch over it).
const defaultBranchTemplate = "{agent}/{ticket-key}-{slug}"

// maxSlugLen bounds the {slug} placeholder. Branch names appear in the UI, in PR headers and
// in `git branch` output; a 200-char ticket title must not become a 200-char ref.
const maxSlugLen = 40

// branchName renders the branch template deterministically: same agent, ticket and run seq →
// same name. Placeholders: {agent} (agent name, slugified), {ticket-key} (e.g. PAY-14; a
// ticketless run substitutes run-<seq>), {slug} (ticket title, slugified and bounded).
func branchName(template string, agentName, ticketKey, title string, runSeq int64) string {
	if strings.TrimSpace(template) == "" {
		template = defaultBranchTemplate
	}
	key := ticketKey
	if key == "" {
		key = fmt.Sprintf("run-%d", runSeq)
	}
	rendered := strings.NewReplacer(
		"{agent}", slugify(agentName, maxSlugLen),
		"{ticket-key}", key,
		"{slug}", slugify(title, maxSlugLen),
	).Replace(template)
	return sanitizeRef(rendered)
}

// uniqueBranch appends -2, -3, … until taken says no (or the counter proves something is
// systematically wrong). taken may be nil: no collision source is wired, first render wins.
func uniqueBranch(ctx context.Context, base string, taken func(context.Context, string) (bool, error)) (string, error) {
	if taken == nil {
		return base, nil
	}
	name := base
	for i := 2; ; i++ {
		inUse, err := taken(ctx, name)
		if err != nil {
			return "", fmt.Errorf("checking branch %q for collisions: %w", name, err)
		}
		if !inUse {
			return name, nil
		}
		if i > 100 {
			return "", fmt.Errorf("no free branch name after 100 tries from %q", base)
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

// slugify folds a human title into a bounded lowercase kebab slug: ASCII letters and digits
// survive, common accented Latin letters fold to their base letter, and every other rune —
// spaces, punctuation, emoji, CJK — becomes a separator. An empty result is legitimate (the
// template's surrounding separators are trimmed by sanitizeRef).
func slugify(s string, maxLen int) string {
	var b strings.Builder
	pendingSep := false
	for _, r := range strings.ToLower(s) {
		var out string
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			out = string(r)
		default:
			if folded, ok := latinFold[r]; ok {
				out = folded
			}
		}
		if out == "" {
			if b.Len() > 0 {
				pendingSep = true
			}
			continue
		}
		if pendingSep {
			if b.Len()+1+len(out) > maxLen {
				break
			}
			b.WriteByte('-')
			pendingSep = false
		}
		if b.Len()+len(out) > maxLen {
			// Cut at the last full word: drop the partial word built since the last '-'.
			slug := b.String()
			if i := strings.LastIndexByte(slug, '-'); i > 0 {
				return slug[:i]
			}
			return slug
		}
		b.WriteString(out)
	}
	return b.String()
}

// latinFold maps the accented Latin letters that actually show up in ticket titles to their
// ASCII base. Deliberately small: anything absent becomes a separator, which is always a
// valid slug — never a mangled one.
var latinFold = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'æ': "ae",
	'ç': "c", 'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ì': "i", 'í': "i",
	'î': "i", 'ï': "i", 'ñ': "n", 'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o",
	'ö': "o", 'ø': "o", 'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ý': "y",
	'ÿ': "y", 'ß': "ss", 'œ': "oe", 'đ': "d", 'ł': "l", 'š': "s", 'ž': "z",
	'č': "c", 'ć': "c", 'ř': "r", 'ě': "e", 'ů': "u", 'ő': "o", 'ű': "u",
	'ą': "a", 'ę': "e", 'ś': "s", 'ń': "n", 'ź': "z", 'ż': "z",
}

// sanitizeRef makes the rendered template a valid git branch name (git-check-ref-format):
// whitespace and git's forbidden characters become '-', forbidden sequences ("..", "@{",
// "//") collapse, no component starts or ends with '.', nothing ends in ".lock", and leading
// or trailing separators are trimmed.
func sanitizeRef(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r <= ' ' || r == 0x7f: // control characters and whitespace
			b.WriteByte('-')
		case r == '~' || r == '^' || r == ':' || r == '?' || r == '*' || r == '[' || r == '\\':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	for _, bad := range []string{"..", "@{"} {
		for strings.Contains(out, bad) {
			out = strings.ReplaceAll(out, bad, "-")
		}
	}
	// Collapse runs of separators, then fix each path component's edges.
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	for strings.Contains(out, "//") {
		out = strings.ReplaceAll(out, "//", "/")
	}
	parts := strings.Split(out, "/")
	kept := parts[:0]
	for _, p := range parts {
		p = strings.Trim(p, "-")
		p = strings.TrimLeft(p, ".")
		p = strings.TrimRight(p, ".")
		p = strings.TrimSuffix(p, ".lock")
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "/")
}
