package mcp

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Routes registers the human side of elicitation (contracts §5):
//
//	POST /api/v1/elicitations/{id}/respond    project members, resolved via the run
//
// The MCP endpoint itself is mounted separately (Handler), on the main mux and on the
// egress-proxy listener, because it authenticates by run token, not by session.
func (s *Server) Routes(mux httpx.Registrar, a *auth.Service) {
	mux.Handle("POST /api/v1/elicitations/{id}/respond",
		a.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.handleRespond(w, r, a)
		})))
}

// respondBody is the respond request: one action, plus the fields that action needs.
// `remember` implements interaction rule 8: "always allow" writes exactly one
// agent_permission_rules row, then answers allow.
type respondBody struct {
	Action       string              `json:"action"` // answer | approve | deny | approve_with_edits | remember
	Answers      map[string][]string `json:"answers,omitempty"`
	Text         string              `json:"text,omitempty"`
	UpdatedInput json.RawMessage     `json:"updated_input,omitempty"`
	Message      string              `json:"message,omitempty"`
	Pattern      string              `json:"pattern,omitempty"` // remember: rule pattern override
}

func (b *respondBody) Validate() []httpx.FieldError {
	switch b.Action {
	case "answer", "approve", "deny", "approve_with_edits", "remember":
		return nil
	}
	return []httpx.FieldError{{Field: "action",
		Message: "action must be one of: answer, approve, deny, approve_with_edits, remember"}}
}

// elicitationBody is the wire shape of one elicitation.
type elicitationBody struct {
	ID          string          `json:"id"`
	RunID       string          `json:"run_id"`
	Kind        string          `json:"kind"`
	State       string          `json:"state"`
	Request     json.RawMessage `json:"request"`
	Response    json.RawMessage `json:"response,omitempty"`
	RespondedBy *string         `json:"responded_by,omitempty"`
	RespondedAt *string         `json:"responded_at,omitempty"`
	CreatedAt   string          `json:"created_at"`
}

func toElicitationBody(el domain.Elicitation) elicitationBody {
	return elicitationBody{
		ID: el.ID, RunID: el.RunID, Kind: string(el.Kind), State: string(el.State),
		Request: el.Request, Response: el.Response,
		RespondedBy: el.RespondedBy, RespondedAt: el.RespondedAt, CreatedAt: el.CreatedAt,
	}
}

func (s *Server) handleRespond(w http.ResponseWriter, r *http.Request, a *auth.Service) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated",
			"Not authenticated", "Sign in to use this endpoint.")
		return
	}
	el, err := s.st.Elicitations().ByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"Not found", "No elicitation matches this id.")
		return
	}
	if err != nil {
		s.writeError(w, err)
		return
	}

	run, project, err := s.runProject(r, el.RunID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	member, err := a.IsProjectMember(r.Context(), u, project)
	if err != nil {
		s.writeError(w, err)
		return
	}
	if !member {
		httpx.WriteProblem(w, http.StatusForbidden, "forbidden",
			"Not a project member", "You are not a member of this project.")
		return
	}

	body, ok := httpx.DecodeJSON[respondBody](w, r)
	if !ok {
		return
	}
	if el.State != domain.ElicitationPending {
		httpx.WriteProblem(w, http.StatusConflict, "elicitation_not_pending",
			"Already resolved", "This elicitation is "+string(el.State)+"; it cannot be answered again.")
		return
	}

	resp, fieldErrs := buildResponse(el, body)
	if len(fieldErrs) > 0 {
		httpx.WriteValidation(w, fieldErrs)
		return
	}

	var rule *domain.AgentPermissionRule
	if body.Action == "remember" {
		rule, err = s.rememberRule(r, run, el, body, u.ID)
		if err != nil {
			s.writeError(w, err)
			return
		}
	}

	userID := u.ID
	resolved, err := s.Resolve(r.Context(), el.ID, resp, &userID)
	if errors.Is(err, ErrNotPending) {
		httpx.WriteProblem(w, http.StatusConflict, "elicitation_not_pending",
			"Already resolved", "Someone answered this elicitation first.")
		return
	}
	if err != nil {
		s.writeError(w, err)
		return
	}

	if s.audit != nil {
		_ = s.audit.Write(r.Context(), "elicitation.respond",
			audit.Target{Kind: "elicitation", ID: el.ID, ProjectID: run.ProjectID},
			map[string]any{"state": string(domain.ElicitationPending)},
			map[string]any{"state": string(resolved.State), "action": body.Action})
	}

	out := struct {
		Elicitation elicitationBody `json:"elicitation"`
		RuleID      string          `json:"rule_id,omitempty"`
	}{Elicitation: toElicitationBody(resolved)}
	if rule != nil {
		out.RuleID = rule.ID
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// buildResponse maps one respond action onto the ports.Response the blocked tool call gets.
func buildResponse(el domain.Elicitation, body respondBody) (ports.Response, []httpx.FieldError) {
	isQuestion := el.Kind == domain.ElicitationQuestion
	switch body.Action {
	case "answer":
		if !isQuestion {
			return ports.Response{}, []httpx.FieldError{{Field: "action",
				Message: "answer applies to questions; use approve/deny for approvals"}}
		}
		if len(body.Answers) == 0 && body.Text == "" {
			return ports.Response{}, []httpx.FieldError{{Field: "answers",
				Message: "an answer needs answers or text"}}
		}
		return ports.Response{Answers: body.Answers, Text: body.Text}, nil
	case "approve", "approve_with_edits", "remember":
		if isQuestion {
			return ports.Response{}, []httpx.FieldError{{Field: "action",
				Message: body.Action + " applies to approvals; use answer for questions"}}
		}
		resp := ports.Response{Behavior: "allow"}
		if body.Action == "approve_with_edits" {
			if len(body.UpdatedInput) == 0 {
				return ports.Response{}, []httpx.FieldError{{Field: "updated_input",
					Message: "approve_with_edits needs updated_input"}}
			}
			resp.UpdatedInput = body.UpdatedInput
		}
		return resp, nil
	case "deny":
		if isQuestion {
			// Denying a question is answering it with a refusal in words.
			text := body.Message
			if text == "" {
				text = "The delegating human declined to answer."
			}
			return ports.Response{Text: text}, nil
		}
		return ports.Response{Behavior: "deny", Message: body.Message}, nil
	}
	return ports.Response{}, []httpx.FieldError{{Field: "action", Message: "unknown action"}}
}

// rememberRule writes the ONE agent_permission_rules row an "always allow" produces
// (interaction rule 8) — never a global mute. The rule's tool and pattern come from the
// parked request; a pattern override in the body wins.
func (s *Server) rememberRule(r *http.Request, run domain.Run, el domain.Elicitation, body respondBody, userID string) (*domain.AgentPermissionRule, error) {
	req := requestedApproval(el)
	if req.ToolName == "" {
		return nil, errors.New("mcp: this approval carries no tool_name to remember")
	}
	pattern := body.Pattern
	if pattern == "" {
		pattern = rememberedPattern(req.ToolName, toolSpecifier(req.ToolName, req.Input))
	}
	rule := domain.AgentPermissionRule{
		ID:               domain.NewID(),
		AgentID:          run.AgentID,
		Tool:             req.ToolName,
		Pattern:          pattern,
		Decision:         domain.DecisionAllow,
		CreatedFromRunID: &run.ID,
		CreatedBy:        &userID,
		CreatedAt:        domain.FormatTime(s.now()),
	}
	if err := s.st.PermissionRules().Create(r.Context(), &rule); err != nil {
		return nil, err
	}
	if s.audit != nil {
		_ = s.audit.Write(r.Context(), "agent.permission_rule.create",
			audit.Target{Kind: "permission_rule", ID: rule.ID, ProjectID: run.ProjectID},
			nil, rule)
	}
	return &rule, nil
}

func (s *Server) writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"Not found", "Nothing matches this path.")
		return
	}
	s.logger.Error("mcp: request failed", "error", err.Error())
	httpx.WriteProblem(w, http.StatusInternalServerError, httpx.TypeInternal,
		"Something went wrong", "The server could not complete this request.")
}
