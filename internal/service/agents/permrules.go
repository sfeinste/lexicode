// Permission-rule routes (contracts §5, story S21): the rows "always allow" writes
// (interaction rule 8) are visible and deletable in agent settings — never a hidden mute.
package agents

import (
	"errors"
	"net/http"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
)

type permissionRuleBody struct {
	ID               string  `json:"id"`
	AgentID          string  `json:"agent_id"`
	Tool             string  `json:"tool"`
	Pattern          string  `json:"pattern"`
	Decision         string  `json:"decision"`
	CreatedFromRunID *string `json:"created_from_run_id,omitempty"`
	CreatedBy        *string `json:"created_by,omitempty"`
	CreatedAt        string  `json:"created_at"`
}

func toPermissionRuleBody(r domain.AgentPermissionRule) permissionRuleBody {
	return permissionRuleBody{
		ID: r.ID, AgentID: r.AgentID, Tool: r.Tool, Pattern: r.Pattern,
		Decision: string(r.Decision), CreatedFromRunID: r.CreatedFromRunID,
		CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt,
	}
}

func (s *Service) handleListRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.st.PermissionRules().ForAgent(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	body := struct {
		Rules []permissionRuleBody `json:"rules"`
	}{Rules: make([]permissionRuleBody, 0, len(rules))}
	for _, rule := range rules {
		body.Rules = append(body.Rules, toPermissionRuleBody(rule))
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (s *Service) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	agentID, ruleID := r.PathValue("id"), r.PathValue("rule_id")
	rule, err := s.st.PermissionRules().ByID(r.Context(), ruleID)
	if err == nil && rule.AgentID != agentID {
		err = store.ErrNotFound // a rule is addressable only under its own agent
	}
	if err == nil {
		err = s.st.PermissionRules().Delete(r.Context(), ruleID)
	}
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"Not found", "No such permission rule on this agent.")
		return
	}
	if err != nil {
		s.writeError(w, err)
		return
	}
	ag, aerr := s.st.Agents().ByID(r.Context(), agentID)
	projectID := ""
	if aerr == nil {
		projectID = ag.ProjectID
	}
	if err := s.audit.Write(r.Context(), "agent.permission_rule.delete",
		audit.Target{Kind: "permission_rule", ID: ruleID, ProjectID: projectID},
		rule, nil); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
