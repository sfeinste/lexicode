package claudecode

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Per-tool title formatters and payload shapes (contracts §3.2). The title is the one-line
// render the UI always shows; the payload is typed per tool and drives tool-aware rendering:
// diff hunks for Edit/Write, argv+exit+output for Bash, and an honest {raw} fallback for
// tools this adapter does not know.

const (
	// titleCap bounds every activity title to one readable line.
	titleCap = 96
	// outputCap bounds each captured output field (Bash stdout/stderr, tool results) in the
	// payload. The full output still exists in the container; the activity keeps the head
	// and marks itself truncated.
	outputCap = 8 * 1024
	// diffCapLines bounds the number of diff lines kept per Edit/Write payload.
	diffCapLines = 200
	// contextLines is how many unchanged lines surround a diff hunk.
	contextLines = 3
)

// workspacePrefix is stripped from paths in titles: the UI renders workspace-relative paths.
const workspacePrefix = "/workspace/"

func relPath(p string) string {
	if p == "" {
		return p
	}
	return strings.TrimPrefix(p, workspacePrefix)
}

// truncateLine returns the first line of s, capped at titleCap runes.
func truncateLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > titleCap {
		return string(r[:titleCap-1]) + "…"
	}
	return s
}

// capOutput returns s capped at outputCap bytes (on a rune boundary) and whether it was cut.
func capOutput(s string) (string, bool) {
	if len(s) <= outputCap {
		return s, false
	}
	cut := s[:outputCap]
	for len(cut) > 0 && !isRuneStart(cut[len(cut)-1]) {
		cut = cut[:len(cut)-1]
	}
	return cut, true
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// toolInput is the union of the input fields the known tools use.
type toolInput struct {
	FilePath   string          `json:"file_path"`
	OldString  string          `json:"old_string"`
	NewString  string          `json:"new_string"`
	ReplaceAll bool            `json:"replace_all"`
	Content    string          `json:"content"`
	Command    string          `json:"command"`
	Pattern    string          `json:"pattern"`
	Path       string          `json:"path"`
	Glob       string          `json:"glob"`
	Todos      json.RawMessage `json:"todos"`
}

// formatAction builds the title and initial payload for a tool_use. The payload is completed
// later by mergeResult when the tool_result arrives.
func formatAction(tool string, input json.RawMessage) (string, map[string]any) {
	var in toolInput
	_ = json.Unmarshal(input, &in) // best effort; zero fields fall through to the fallback

	switch tool {
	case "Read":
		return "Read " + relPath(in.FilePath), map[string]any{"path": relPath(in.FilePath)}
	case "Edit":
		return "Edit " + relPath(in.FilePath), map[string]any{
			"path":  relPath(in.FilePath),
			"hunks": diffHunks(in.OldString, in.NewString),
		}
	case "Write":
		return "Write " + relPath(in.FilePath), map[string]any{
			"path":  relPath(in.FilePath),
			"hunks": diffHunks("", in.Content),
		}
	case "Bash":
		return "$ " + truncateLine(in.Command), map[string]any{
			// The CLI runs Bash commands through a shell; this is the honest argv shape for
			// the payload contract {argv, exit, stdout, stderr, truncated}.
			"argv": []string{"/bin/sh", "-c", in.Command},
		}
	case "Grep":
		scope := in.Glob
		if scope == "" {
			scope = relPath(in.Path)
		}
		if scope == "" {
			scope = "**"
		}
		return fmt.Sprintf("Search %q in %s", in.Pattern, scope),
			map[string]any{"pattern": in.Pattern}
	case "Glob":
		scope := relPath(in.Path)
		if scope == "" {
			scope = "**"
		}
		return fmt.Sprintf("Search %q in %s", in.Pattern, scope),
			map[string]any{"pattern": in.Pattern}
	case "TodoWrite":
		var items []json.RawMessage
		_ = json.Unmarshal(in.Todos, &items)
		return fmt.Sprintf("Plan updated (%d items)", len(items)),
			map[string]any{"items": json.RawMessage(orEmptyArray(in.Todos))}
	}

	// MCP tools render as "server: tool" (§3.3 gives the lexicode ones richer cards in S21;
	// until then this compact honest form applies to all of them).
	if server, name, ok := splitMCPTool(tool); ok {
		return truncateLine(server + ": " + name + " " + compactParams(input)),
			map[string]any{"raw": json.RawMessage(orEmptyObject(input))}
	}

	// Unknown tool: "<Tool> " + compact params. Never raw JSON as the default rendering, but
	// the payload keeps the honest {raw} for the verbose view.
	return truncateLine(tool + " " + compactParams(input)),
		map[string]any{"raw": json.RawMessage(orEmptyObject(input))}
}

// mergeResult completes an action's payload from its tool_result. ok reports whether the
// result was a success.
func mergeResult(tool string, payload map[string]any, res contentBlock) {
	text := resultText(res.Content)
	switch tool {
	case "Read":
		payload["lines"] = countLines(text)
	case "Edit", "Write":
		// The diff was computed at emission from the input; a failed edit keeps it plus the
		// error text so the card can explain itself.
		if res.IsError {
			capped, _ := capOutput(text)
			payload["error"] = capped
		}
	case "Bash":
		out, cut := capOutput(text)
		if res.IsError {
			payload["stdout"] = ""
			payload["stderr"] = out
			payload["exit"] = bashExitCode(text)
		} else {
			payload["stdout"] = out
			payload["stderr"] = ""
			payload["exit"] = 0
		}
		payload["truncated"] = cut
	case "Grep", "Glob":
		payload["matches"] = countMatches(text)
	case "TodoWrite":
		// Nothing useful in the result; the items are the payload.
	default:
		out, cut := capOutput(text)
		payload["result"] = out
		if cut {
			payload["truncated"] = true
		}
	}
}

// exitCodeRe recognises the CLI's "Exit code N" phrasing in failed Bash results.
var exitCodeRe = regexp.MustCompile(`(?i)exit code:? (\d+)`)

// bashExitCode extracts the exit code from a failed Bash result; when the text does not name
// one, 1 stands in — the payload contract wants a number and the step did fail.
func bashExitCode(text string) int {
	if m := exitCodeRe.FindStringSubmatch(text); m != nil {
		var code int
		_, _ = fmt.Sscanf(m[1], "%d", &code)
		return code
	}
	return 1
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// countMatches counts result lines, treating the CLI's "No matches found"-style responses
// as zero.
func countMatches(s string) int {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(strings.ToLower(s), "no matches") || strings.HasPrefix(strings.ToLower(s), "no files") {
		return 0
	}
	return countLines(s)
}

// splitMCPTool splits "mcp__server__tool" into its server and tool parts.
func splitMCPTool(name string) (server, tool string, ok bool) {
	rest, found := strings.CutPrefix(name, "mcp__")
	if !found {
		return "", "", false
	}
	server, tool, found = strings.Cut(rest, "__")
	if !found || server == "" || tool == "" {
		return "", "", false
	}
	return server, tool, true
}

// compactParams renders a tool input as "key=value key=value", values truncated, keys sorted —
// the compact honest fallback title for tools without a formatter.
func compactParams(input json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil || len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := fmt.Sprintf("%v", m[k])
		if r := []rune(v); len(r) > 40 {
			v = string(r[:39]) + "…"
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, " ")
}

func orEmptyObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}

func orEmptyArray(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("[]")
	}
	return raw
}

// diffHunk is one hunk of an Edit/Write payload: a header plus prefixed lines (" " context,
// "-" removed, "+" added), the shape the UI renders as a diff.
type diffHunk struct {
	Header string   `json:"header"`
	Lines  []string `json:"lines"`
}

// diffHunks computes a small unified diff between old and new text. It is deliberately
// simple — common prefix/suffix line trimming with the changed middle as -/+ runs — because
// Edit inputs are short by construction (old_string must be unique in the file) and the
// payload only needs to *render* the change, not re-apply it. Output is capped at
// diffCapLines lines with a trailing "…" marker.
func diffHunks(oldText, newText string) []diffHunk {
	if oldText == newText {
		return []diffHunk{}
	}
	oldLines := splitLines(oldText)
	newLines := splitLines(newText)

	// Trim common prefix and suffix.
	pre := 0
	for pre < len(oldLines) && pre < len(newLines) && oldLines[pre] == newLines[pre] {
		pre++
	}
	suf := 0
	for suf < len(oldLines)-pre && suf < len(newLines)-pre &&
		oldLines[len(oldLines)-1-suf] == newLines[len(newLines)-1-suf] {
		suf++
	}

	var lines []string
	ctxStart := max(0, pre-contextLines)
	for _, l := range oldLines[ctxStart:pre] {
		lines = append(lines, " "+l)
	}
	for _, l := range oldLines[pre : len(oldLines)-suf] {
		lines = append(lines, "-"+l)
	}
	for _, l := range newLines[pre : len(newLines)-suf] {
		lines = append(lines, "+"+l)
	}
	ctxEnd := min(suf, contextLines)
	for _, l := range oldLines[len(oldLines)-suf : len(oldLines)-suf+ctxEnd] {
		lines = append(lines, " "+l)
	}

	truncated := false
	if len(lines) > diffCapLines {
		lines = append(lines[:diffCapLines], "…")
		truncated = true
	}
	header := fmt.Sprintf("@@ -%d +%d @@", len(oldLines)-pre-suf, len(newLines)-pre-suf)
	if truncated {
		header += " (truncated)"
	}
	return []diffHunk{{Header: header, Lines: lines}}
}

// splitLines splits on newlines without a phantom trailing empty line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}
