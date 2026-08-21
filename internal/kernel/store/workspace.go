package store

import (
	"context"

	"github.com/spruce/lexicode/internal/domain"
)

// WorkspaceRepo reads and writes the single workspace_settings row, which migration 0001 inserts
// with the schema's defaults — "inherited from workspace" always has something to point at.
type WorkspaceRepo struct{ h handle }

// Workspace returns the workspace-settings repository.
func (s *Store) Workspace() *WorkspaceRepo { return &WorkspaceRepo{h: s.handle()} }

// Workspace returns the workspace-settings repository bound to this transaction.
func (t *Tx) Workspace() *WorkspaceRepo { return &WorkspaceRepo{h: t.handle()} }

// Get returns the workspace settings.
func (r *WorkspaceRepo) Get(ctx context.Context) (domain.WorkspaceSettings, error) {
	var ws domain.WorkspaceSettings
	err := r.h.r.QueryRowContext(ctx, `
		SELECT default_branch, default_branch_template, default_network_policy,
			default_daily_budget_cents, default_context_threshold_tokens,
			default_verification_days, max_concurrent_containers, poll_interval_seconds,
			updated_at
		FROM workspace_settings WHERE id = 1`).
		Scan(&ws.DefaultBranch, &ws.DefaultBranchTemplate, &ws.DefaultNetworkPolicy,
			&ws.DefaultDailyBudgetCents, &ws.DefaultContextThresholdTokens,
			&ws.DefaultVerificationDays, &ws.MaxConcurrentContainers, &ws.PollIntervalSeconds,
			&ws.UpdatedAt)
	if err != nil {
		return domain.WorkspaceSettings{}, mapErr(err)
	}
	return ws, nil
}

// Update rewrites the workspace settings.
func (r *WorkspaceRepo) Update(ctx context.Context, ws *domain.WorkspaceSettings) error {
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE workspace_settings SET default_branch = ?, default_branch_template = ?,
			default_network_policy = ?, default_daily_budget_cents = ?,
			default_context_threshold_tokens = ?, default_verification_days = ?,
			max_concurrent_containers = ?, poll_interval_seconds = ?, updated_at = ?
		WHERE id = 1`,
		ws.DefaultBranch, ws.DefaultBranchTemplate, ws.DefaultNetworkPolicy,
		ws.DefaultDailyBudgetCents, ws.DefaultContextThresholdTokens, ws.DefaultVerificationDays,
		ws.MaxConcurrentContainers, ws.PollIntervalSeconds, ws.UpdatedAt)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}
