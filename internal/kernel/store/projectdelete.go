package store

import (
	"context"
)

// ProjectDeleteCounts is what a project deletion removed — the numbers the danger-zone
// confirmation names (UI spec §5.11) and the workspace-level audit entry records.
type ProjectDeleteCounts struct {
	Tickets   int64
	Runs      int64
	WikiPages int64
}

// CountProjectRows returns the ProjectDeleteCounts a deletion of this project would remove,
// archived rows included — the confirm dialog names what will actually go, not what is
// visible.
func (r *ProjectsRepo) CountProjectRows(ctx context.Context, projectID string) (ProjectDeleteCounts, error) {
	var c ProjectDeleteCounts
	for _, q := range []struct {
		dst   *int64
		query string
	}{
		{&c.Tickets, `SELECT COUNT(*) FROM tickets WHERE project_id = ?`},
		{&c.Runs, `SELECT COUNT(*) FROM runs WHERE project_id = ?`},
		{&c.WikiPages, `SELECT COUNT(*) FROM wiki_pages WHERE project_id = ?`},
	} {
		if err := r.h.r.QueryRowContext(ctx, q.query, projectID).Scan(q.dst); err != nil {
			return ProjectDeleteCounts{}, mapErr(err)
		}
	}
	return c, nil
}

// DeleteProjectCascade hard-deletes one project and every row that hangs off it. The schema
// declares no ON DELETE actions and the pool runs with foreign_keys=ON, so the deletes go in
// dependency order — children before parents — inside the caller's transaction. Two circular
// edges need care: agents ↔ agent_directives (directive_version_id) is broken by nulling the
// pointer first, and the self-references (tickets.parent_id, runs.parent_run_id,
// wiki_pages.parent_id/proposal_target_id) are safe because SQLite checks immediate FKs at
// statement end, so one DELETE that removes parent and child together passes.
//
// audit_log rows scoped to the project are NOT deleted: their project_id is set to NULL so the
// project's history survives at workspace level — the audit log is append-only (S06) and a
// deletion must not be able to erase its own trail.
//
// The returned counts are what was actually removed. Callers are responsible for refusing the
// deletion while runs are active — by the time this runs, every container should be gone.
func (t *Tx) DeleteProjectCascade(ctx context.Context, projectID string) (ProjectDeleteCounts, error) {
	counts, err := t.Projects().CountProjectRows(ctx, projectID)
	if err != nil {
		return ProjectDeleteCounts{}, err
	}

	// Break the agents ↔ agent_directives cycle before either side is deleted.
	if _, err := t.Exec(ctx,
		`UPDATE agents SET directive_version_id = NULL WHERE project_id = ?`, projectID); err != nil {
		return ProjectDeleteCounts{}, err
	}

	byProject := func(table string) string {
		return `DELETE FROM ` + table + ` WHERE project_id = ?`
	}
	viaRuns := func(table string) string {
		return `DELETE FROM ` + table + ` WHERE run_id IN (SELECT id FROM runs WHERE project_id = ?)`
	}
	for _, q := range []string{
		// Leaves that reference runs, tickets, agents or triggers — before their parents.
		viaRuns("activities"),
		viaRuns("elicitations"),
		viaRuns("run_outputs"),
		viaRuns("run_context_items"),
		viaRuns("run_messages"),
		`DELETE FROM agent_permission_rules WHERE agent_id IN (SELECT id FROM agents WHERE project_id = ?)`,
		`DELETE FROM acceptance_criteria WHERE ticket_id IN (SELECT id FROM tickets WHERE project_id = ?)`,
		`DELETE FROM triage_items WHERE ticket_id IN (SELECT id FROM tickets WHERE project_id = ?)`,
		`DELETE FROM ticket_stream WHERE ticket_id IN (SELECT id FROM tickets WHERE project_id = ?)`,
		`DELETE FROM ticket_labels WHERE label_id IN (SELECT id FROM labels WHERE project_id = ?)`,
		byProject("labels"),
		`DELETE FROM trigger_firings WHERE trigger_id IN (SELECT id FROM triggers WHERE project_id = ?)`,
		`DELETE FROM wiki_versions WHERE page_id IN (SELECT id FROM wiki_pages WHERE project_id = ?)`,
		byProject("wiki_pages"), // the FTS delete trigger (migration 0002) cleans the index
		byProject("mentions"),
		byProject("notifications"),
		byProject("budget_ledger"),
		// Core rows: runs before tickets (runs.ticket_id) and events (runs.cause_event_id);
		// tickets before columns (tickets.column_id) and agents (delegate_agent_id).
		byProject("runs"),
		byProject("tickets"),
		byProject("columns"),
		`DELETE FROM agent_directives WHERE agent_id IN (SELECT id FROM agents WHERE project_id = ?)`,
		byProject("agents"),
		byProject("triggers"),
		byProject("events"),
		byProject("poll_cursors"),
		byProject("poll_pr_state"),
		byProject("repos"), // before secrets: repos.token_secret_id
		byProject("secrets"),
		byProject("saved_views"),
		byProject("project_members"),
		`UPDATE audit_log SET project_id = NULL WHERE project_id = ?`,
		`DELETE FROM projects WHERE id = ?`,
	} {
		if _, err := t.Exec(ctx, q, projectID); err != nil {
			return ProjectDeleteCounts{}, err
		}
	}
	return counts, nil
}
