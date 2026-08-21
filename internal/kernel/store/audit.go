package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/spruce/lexicode/internal/domain"
)

// AuditRepo appends to and reads the audit_log. The log is append-only: there is no update and
// no delete, and none should ever be added.
type AuditRepo struct{ h handle }

// Audit returns the audit repository.
func (s *Store) Audit() *AuditRepo { return &AuditRepo{h: s.handle()} }

// Audit returns the audit repository bound to this transaction.
func (t *Tx) Audit() *AuditRepo { return &AuditRepo{h: t.handle()} }

const auditCols = `id, project_id, actor_kind, actor_id, action, target_kind, target_id,
	before, after, note, created_at`

// Append inserts one audit entry.
func (r *AuditRepo) Append(ctx context.Context, e *domain.AuditEntry) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO audit_log (`+auditCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, nullStr(e.ProjectID), string(e.ActorKind), nullStr(e.ActorID), e.Action,
		e.TargetKind, e.TargetID, nullRawText(e.Before), nullRawText(e.After),
		e.Note, e.CreatedAt)
	return mapErr(err)
}

// Recent returns the newest entries, optionally scoped to a project (empty projectID = all).
func (r *AuditRepo) Recent(ctx context.Context, projectID string, limit int) ([]domain.AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	var (
		rows *sql.Rows
		err  error
	)
	if projectID == "" {
		rows, err = r.h.r.QueryContext(ctx,
			`SELECT `+auditCols+` FROM audit_log ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	} else {
		rows, err = r.h.r.QueryContext(ctx, `
			SELECT `+auditCols+` FROM audit_log
			WHERE project_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, projectID, limit)
	}
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.AuditEntry
	for rows.Next() {
		var (
			e                 domain.AuditEntry
			actorKind         string
			projID, actorID   sql.NullString
			beforeJS, afterJS sql.NullString
		)
		err := rows.Scan(&e.ID, &projID, &actorKind, &actorID, &e.Action, &e.TargetKind,
			&e.TargetID, &beforeJS, &afterJS, &e.Note, &e.CreatedAt)
		if err != nil {
			return nil, mapErr(err)
		}
		e.ProjectID = strPtr(projID)
		e.ActorKind = domain.ActorKind(actorKind)
		e.ActorID = strPtr(actorID)
		if beforeJS.Valid {
			e.Before = json.RawMessage(beforeJS.String)
		}
		if afterJS.Valid {
			e.After = json.RawMessage(afterJS.String)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
