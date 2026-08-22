package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// PermissionRulesRepo reads and writes agent_permission_rules: what "always allow" writes
// (interaction rule 8). Rules are evaluated by the S21 MCP server before autonomy.
type PermissionRulesRepo struct{ h handle }

// PermissionRules returns the permission-rules repository.
func (s *Store) PermissionRules() *PermissionRulesRepo { return &PermissionRulesRepo{h: s.handle()} }

// PermissionRules returns the permission-rules repository bound to this transaction.
func (t *Tx) PermissionRules() *PermissionRulesRepo { return &PermissionRulesRepo{h: t.handle()} }

const permRuleCols = `id, agent_id, tool, pattern, decision, created_from_run_id, created_by,
	created_at`

// Create inserts a rule.
func (r *PermissionRulesRepo) Create(ctx context.Context, rule *domain.AgentPermissionRule) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO agent_permission_rules (`+permRuleCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rule.ID, rule.AgentID, rule.Tool, rule.Pattern, string(rule.Decision),
		nullStr(rule.CreatedFromRunID), nullStr(rule.CreatedBy), rule.CreatedAt)
	return mapErr(err)
}

// ByID returns the rule with this ID, or ErrNotFound.
func (r *PermissionRulesRepo) ByID(ctx context.Context, id string) (domain.AgentPermissionRule, error) {
	return scanPermRule(r.h.r.QueryRowContext(ctx,
		`SELECT `+permRuleCols+` FROM agent_permission_rules WHERE id = ?`, id))
}

// ForAgent returns an agent's rules in creation order — the order the MCP server evaluates
// them in (first match wins; documented in internal/service/mcp).
func (r *PermissionRulesRepo) ForAgent(ctx context.Context, agentID string) ([]domain.AgentPermissionRule, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+permRuleCols+` FROM agent_permission_rules
		WHERE agent_id = ? ORDER BY created_at, id`, agentID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.AgentPermissionRule
	for rows.Next() {
		rule, err := scanPermRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

// Delete removes a rule. ErrNotFound when no such rule exists.
func (r *PermissionRulesRepo) Delete(ctx context.Context, id string) error {
	res, err := r.h.w.ExecContext(ctx,
		`DELETE FROM agent_permission_rules WHERE id = ?`, id)
	if err != nil {
		return mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return mapErr(err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanPermRule(row rowScanner) (domain.AgentPermissionRule, error) {
	var (
		rule                  domain.AgentPermissionRule
		decision              string
		createdFrom, createdB sql.NullString
	)
	err := row.Scan(&rule.ID, &rule.AgentID, &rule.Tool, &rule.Pattern, &decision,
		&createdFrom, &createdB, &rule.CreatedAt)
	if err != nil {
		return domain.AgentPermissionRule{}, mapErr(err)
	}
	rule.Decision = domain.PermissionDecision(decision)
	rule.CreatedFromRunID = strPtr(createdFrom)
	rule.CreatedBy = strPtr(createdB)
	return rule, nil
}
