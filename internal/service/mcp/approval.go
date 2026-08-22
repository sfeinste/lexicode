package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/spruce/lexicode/internal/domain"

	"github.com/spruce/lexicode/internal/kernel/store"
)

// suggestDenyMessage is the contracts §3.3 message, verbatim. Do not edit it: the acceptance
// criterion quotes it.
const suggestDenyMessage = "this agent is in Suggest mode; it plans, it does not act."

// approvalRequest is Claude Code's permission payload (--permission-prompt-tool input).
// Unknown extra fields (tool_use_id, suggestions) are tolerated and ignored.
type approvalRequest struct {
	ToolName string          `json:"tool_name"`
	Input    json.RawMessage `json:"input"`
}

// approvalDecision is what request_approval returns to Claude Code.
type approvalDecision struct {
	Behavior     string          `json:"behavior"`
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	Message      string          `json:"message,omitempty"`
}

// toolRequestApproval implements contracts §3.3's precedence exactly:
//
//  1. agent_permission_rules — first rule matching tool+pattern, in creation order, decides:
//     allow/deny immediately, ask forces a human even under permissive autonomy.
//  2. The autonomy short-circuit table, verbatim.
//  3. Park for a human: elicitation kind approval, run → awaiting_approval, block.
func (s *Server) toolRequestApproval(ctx context.Context, run domain.Run, raw json.RawMessage) (any, error) {
	var req approvalRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("invalid request_approval arguments: %w", err)
	}
	if req.ToolName == "" {
		return nil, errors.New("request_approval needs a tool_name")
	}
	if len(req.Input) == 0 {
		req.Input = json.RawMessage("{}")
	}
	specifier := toolSpecifier(req.ToolName, req.Input)

	// (1) Permission rules short-circuit before autonomy (contracts §3.3).
	rules, err := s.st.PermissionRules().ForAgent(ctx, run.AgentID)
	if err != nil {
		return nil, err
	}
	forceAsk := false
	var askRule *domain.AgentPermissionRule
	for i := range rules {
		rule := &rules[i]
		if !ruleMatches(rule, req.ToolName, specifier) {
			continue
		}
		switch rule.Decision {
		case domain.DecisionAllow:
			return s.decide(ctx, run, req, specifier, allowDecision(req.Input),
				fmt.Sprintf("permission rule %s(%s) allows it", rule.Tool, rule.Pattern)), nil
		case domain.DecisionDeny:
			return s.decide(ctx, run, req, specifier, denyDecision(
				fmt.Sprintf("a permission rule for this agent denies %s(%s)", rule.Tool, rule.Pattern)),
				fmt.Sprintf("permission rule %s(%s) denies it", rule.Tool, rule.Pattern)), nil
		case domain.DecisionAsk:
			forceAsk, askRule = true, rule
		}
		break // first matching rule wins
	}

	// (2) Autonomy short-circuits, unless a rule said ask.
	parkedReason := ""
	if forceAsk {
		parkedReason = fmt.Sprintf("a permission rule for this agent (%s: %s) says ask a human",
			askRule.Tool, askRule.Pattern)
	} else {
		switch run.Autonomy {
		case domain.AutonomySuggest:
			if isMutatingTool(req.ToolName) {
				return s.decide(ctx, run, req, specifier,
					denyDecision(suggestDenyMessage), "suggest mode denies every mutating tool"), nil
			}
			// Suggest auto-allows nothing: even a read-only request goes to a human.
			parkedReason = "suggest-mode agents act only with explicit approval"
		case domain.AutonomyApproveEach:
			parkedReason = "the approve_each autonomy parks every action for approval"
		case domain.AutonomyAutoGates:
			if hit := destructiveReason(req.ToolName, req.Input); hit != "" {
				parkedReason = hit
			} else {
				return s.decide(ctx, run, req, specifier, allowDecision(req.Input),
					"auto_gates auto-allows non-destructive tools"), nil
			}
		case domain.AutonomyAuto:
			if permissionsGrant(req.ToolName, s.agentPermissions(ctx, run)) {
				return s.decide(ctx, run, req, specifier, allowDecision(req.Input),
					"auto autonomy: the agent's permissions grant it"), nil
			}
			return s.decide(ctx, run, req, specifier, denyDecision(
				fmt.Sprintf("this agent's permissions do not grant %s; ask the project owner to widen them", req.ToolName)),
				"auto autonomy: the agent's permissions do not grant it"), nil
		default:
			parkedReason = fmt.Sprintf("unknown autonomy %q parks for a human, never guesses", run.Autonomy)
		}
	}

	// (3) Park for a human.
	return s.parkApproval(ctx, run, req, specifier, parkedReason)
}

// decide records a short-circuit decision (an action activity — no elicitation exists, no
// human was involved) and returns the wire result.
func (s *Server) decide(ctx context.Context, run domain.Run, req approvalRequest, specifier string, d approvalDecision, why string) approvalDecision {
	title := "Auto-allowed " + approvalAction(req.ToolName, specifier)
	ok := true
	if d.Behavior == "deny" {
		title = "Denied " + approvalAction(req.ToolName, specifier)
		ok = false
	}
	s.appendActivity(ctx, domain.Activity{
		RunID:    run.ID,
		Type:     domain.ActivityAction,
		Level:    1,
		ToolName: "mcp__lexicode__request_approval",
		GroupKey: "mcp__lexicode__request_approval",
		Title:    truncTitle(title),
		Payload: mustJSON(map[string]any{
			"tool_name": req.ToolName, "input": req.Input,
			"behavior": d.Behavior, "message": d.Message, "why": why,
		}),
		OK: &ok,
	})
	return d
}

// parkApproval opens (or reuses) the approval elicitation, parks the run in
// awaiting_approval, and blocks for the human's decision.
func (s *Server) parkApproval(ctx context.Context, run domain.Run, req approvalRequest, specifier, why string) (any, error) {
	// The stored request is the enriched card: Claude Code's payload plus the six fields
	// the approval card must show (contracts §3.3) — the UI cannot derive them.
	card := s.approvalCard(req, specifier, why)
	request := mustJSON(card)

	el, err := s.st.Elicitations().PendingByRequest(ctx, run.ID, domain.ElicitationApproval, request)
	if errors.Is(err, store.ErrNotFound) {
		el, err = s.openElicitation(ctx, run, domain.ElicitationApproval, request,
			"mcp__lexicode__request_approval", truncTitle("Approval: "+card.Action))
	}
	if err != nil {
		return nil, err
	}

	s.transition(ctx, run.ID, domain.RunAwaitingApproval, "waiting for approval: "+card.Action)
	resp, err := s.await(ctx, run, el.ID)
	if err != nil {
		return nil, err
	}
	if resp.Behavior == "deny" {
		msg := resp.Message
		if msg == "" {
			msg = "denied by the delegating human"
		}
		return denyDecision(msg), nil
	}
	updated := resp.UpdatedInput
	if len(updated) == 0 {
		updated = req.Input
	}
	return approvalDecision{Behavior: "allow", UpdatedInput: updated}, nil
}

// approvalCard is the enriched approval payload: the original permission request plus the
// six fields the card renders. Derivations are heuristic and honest — where nothing better
// is derivable from tool_name+input, the text is generic rather than invented.
type approvalCard struct {
	ToolName     string          `json:"tool_name"`
	Input        json.RawMessage `json:"input"`
	Action       string          `json:"action"`
	Scope        string          `json:"scope"`
	Impact       string          `json:"impact"`
	Reason       string          `json:"reason"`
	Alternatives string          `json:"alternatives"`
	Recovery     string          `json:"recovery"`
}

func (s *Server) approvalCard(req approvalRequest, specifier, why string) approvalCard {
	destructive := destructiveReason(req.ToolName, req.Input)

	scope := specifier
	if scope == "" {
		scope = "the run's workspace"
	}
	impact := "reads only; changes nothing"
	if isMutatingTool(req.ToolName) {
		impact = "modifies the run's workspace or environment"
	}
	if destructive != "" {
		impact = "potentially destructive: " + destructive
	}
	recovery := "The workspace is disposable: a bad change can be discarded with the run branch."
	if destructive != "" {
		recovery = "This action may not be undoable from inside the run; review it carefully before approving."
	}
	return approvalCard{
		ToolName:     req.ToolName,
		Input:        req.Input,
		Action:       approvalAction(req.ToolName, specifier),
		Scope:        scope,
		Impact:       impact,
		Reason:       why,
		Alternatives: "Deny with a message the agent will read, or approve with edited input.",
		Recovery:     recovery,
	}
}

// approvalAction is the one-line action name: `Run "npm test"`, `Edit src/api/charge.ts`.
func approvalAction(tool, specifier string) string {
	switch tool {
	case "Bash":
		if specifier != "" {
			return `Run "` + specifier + `"`
		}
		return "Run a command"
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		if specifier != "" {
			return tool + " " + strings.TrimPrefix(specifier, "/workspace/")
		}
		return tool + " a file"
	case "WebFetch":
		if specifier != "" {
			return "Fetch " + specifier
		}
		return "Fetch a URL"
	case "ExitPlanMode":
		return "Leave plan mode and start acting"
	}
	if specifier != "" {
		return tool + " " + specifier
	}
	return tool
}

// agentPermissions loads the agent's typed permissions; a lookup failure grants nothing.
func (s *Server) agentPermissions(ctx context.Context, run domain.Run) domain.AgentPermissions {
	agent, err := s.st.Agents().ByID(ctx, run.AgentID)
	if err != nil {
		s.logger.Warn("mcp: agent lookup failed; granting no permissions",
			"agent", run.AgentID, "error", err.Error())
		return domain.AgentPermissions{}
	}
	return agent.Permissions
}

func allowDecision(input json.RawMessage) approvalDecision {
	return approvalDecision{Behavior: "allow", UpdatedInput: input}
}

func denyDecision(msg string) approvalDecision {
	return approvalDecision{Behavior: "deny", Message: msg}
}

// ---- classification --------------------------------------------------------------------

// readOnlyTools never modify the workspace or the world. Everything not listed is treated as
// mutating — the honest default for a tool this server does not know.
var readOnlyTools = map[string]bool{
	"Read": true, "Glob": true, "Grep": true, "LS": true,
	"NotebookRead": true, "WebFetch": true, "WebSearch": true,
	"TodoRead": true, "Task": true,
}

func isMutatingTool(tool string) bool { return !readOnlyTools[tool] }

// permissionsGrant maps a requested tool onto the agent's typed permissions (data model
// §3.1) for the `auto` autonomy. The mapping is deliberately coarse and documented:
// read-only tools need read_files; editing tools need edit_files; Bash and anything
// execution-shaped needs run_commands; the lexicode wiki proposal needs create_wiki_pages;
// TodoWrite (the agent's own plan) is always granted; unknown tools are granted nothing.
func permissionsGrant(tool string, p domain.AgentPermissions) bool {
	switch tool {
	case "Read", "Glob", "Grep", "LS", "NotebookRead":
		return p.ReadFiles
	case "WebFetch", "WebSearch":
		// Network reach is governed by the run's network policy (enforced in the egress
		// proxy, not here); reading the result needs no file permission.
		return true
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return p.EditFiles
	case "Bash", "BashOutput", "KillShell":
		return p.RunCommands
	case "TodoWrite", "ExitPlanMode", "Task":
		return true
	case "mcp__lexicode__propose_wiki_page":
		return p.CreateWikiPages
	}
	return false
}

// destructive command patterns for the auto_gates heuristic, each with the reason rendered
// on the card. Matching is on the Bash command string, lowercased.
var destructivePatterns = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`\brm\s+(-[a-z]*\s+)*-[a-z]*r[a-z]*f|\brm\s+(-[a-z]*\s+)*-[a-z]*f[a-z]*r`), "recursive force-delete (rm -rf)"},
	{regexp.MustCompile(`\bgit\s+push\b[^|;&]*(\s--force\b|\s-f\b|\s\+\S)`), "force push"},
	{regexp.MustCompile(`\bsudo\b`), "privilege escalation (sudo)"},
	{regexp.MustCompile(`\bmkfs\b|\bdd\s+[^|;&]*of=/dev/`), "raw device write"},
	{regexp.MustCompile(`\b(shutdown|reboot|halt|poweroff)\b`), "host power control"},
	{regexp.MustCompile(`(^|[|;&]\s*)git\s+reset\s+--hard\s+\S*\s*($|[|;&])`), "hard reset discarding work"},
}

// pathOutsideWorkspace reports an absolute path that is not under /workspace or /tmp.
func pathOutsideWorkspace(p string) bool {
	if p == "" || !strings.HasPrefix(p, "/") {
		return false // relative paths resolve under the workspace working directory
	}
	if strings.Contains(p, "..") {
		return true
	}
	return !strings.HasPrefix(p, "/workspace/") && p != "/workspace" &&
		!strings.HasPrefix(p, "/tmp/") && p != "/tmp"
}

// redirectTarget finds `> /abs/path` shell redirections.
var redirectTarget = regexp.MustCompile(`>>?\s*(/[^\s;|&]+)`)

// destructiveReason implements the documented auto_gates heuristics. It returns the reason a
// human must see, or "" when nothing matched:
//
//   - file tools writing outside /workspace (and /tmp, the container's scratch space);
//   - Bash commands matching the destructivePatterns table, or redirecting output to an
//     absolute path outside /workspace//tmp;
//   - the plan gate (ExitPlanMode) — leaving plan mode is the gate auto_gates exists for;
//   - network egress beyond policy is NOT detected here: the S18 egress proxy enforces the
//     policy itself, per request, so no approval heuristic duplicates (or worse,
//     contradicts) the real enforcement.
func destructiveReason(tool string, input json.RawMessage) string {
	switch tool {
	case "ExitPlanMode":
		return "the plan gate: leaving plan mode starts real actions"
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		var in struct {
			FilePath     string `json:"file_path"`
			NotebookPath string `json:"notebook_path"`
		}
		_ = json.Unmarshal(input, &in)
		p := in.FilePath
		if p == "" {
			p = in.NotebookPath
		}
		if pathOutsideWorkspace(p) {
			return "writes outside the workspace: " + p
		}
	case "Bash":
		var in struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(input, &in)
		cmd := strings.ToLower(in.Command)
		for _, p := range destructivePatterns {
			if p.re.MatchString(cmd) {
				return p.reason
			}
		}
		for _, m := range redirectTarget.FindAllStringSubmatch(in.Command, -1) {
			if pathOutsideWorkspace(m[1]) && !strings.HasPrefix(m[1], "/dev/null") {
				return "redirects output outside the workspace: " + m[1]
			}
		}
	}
	return ""
}

// toolSpecifier derives the per-tool "what exactly" string rules match against and cards
// display: the Bash command, the file path, the URL. Unknown tools get the compacted input.
func toolSpecifier(tool string, input json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	switch tool {
	case "Bash":
		return pick("command")
	case "Edit", "Write", "MultiEdit", "Read":
		return pick("file_path", "path")
	case "NotebookEdit", "NotebookRead":
		return pick("notebook_path")
	case "WebFetch":
		return pick("url")
	case "WebSearch":
		return pick("query")
	case "Glob", "Grep":
		return pick("pattern")
	}
	return pick("command", "file_path", "path", "url", "pattern")
}

// ruleMatches evaluates one agent_permission_rules row against tool + specifier. The
// pattern grammar is Claude Code's: "" or "*" match anything; a trailing ":*" or "*" is a
// prefix match on the specifier ("npm test:*" matches "npm test -- --grep x"); anything
// else must equal the specifier exactly.
func ruleMatches(rule *domain.AgentPermissionRule, tool, specifier string) bool {
	if rule.Tool != tool {
		return false
	}
	pat := rule.Pattern
	switch {
	case pat == "" || pat == "*":
		return true
	case strings.HasSuffix(pat, ":*"):
		prefix := strings.TrimSuffix(pat, ":*")
		return specifier == prefix || strings.HasPrefix(specifier, prefix)
	case strings.HasSuffix(pat, "*"):
		return strings.HasPrefix(specifier, strings.TrimSuffix(pat, "*"))
	default:
		return specifier == pat
	}
}

// rememberedPattern is what an "always allow" remember writes when the responder supplies no
// pattern: the exact specifier for path-shaped tools, the first two command words + ":*" for
// Bash (the Claude Code convention), "*" when nothing is derivable.
func rememberedPattern(tool, specifier string) string {
	if specifier == "" {
		return "*"
	}
	if tool == "Bash" {
		fields := strings.Fields(specifier)
		if len(fields) >= 2 {
			return fields[0] + " " + fields[1] + ":*"
		}
		return fields[0] + ":*"
	}
	return specifier
}

// requestedApproval re-parses a stored approval card back into the original permission
// request, for the remember flow.
func requestedApproval(el domain.Elicitation) approvalRequest {
	var req approvalRequest
	_ = json.Unmarshal(el.Request, &req)
	return req
}
