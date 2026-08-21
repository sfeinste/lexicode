package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

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

// AuditFilter narrows a List. Every field is optional; the zero value lists everything, newest
// first. Timestamps are RFC3339 UTC strings, compared lexically (D-2: fixed-width timestamps
// make string order time order).
type AuditFilter struct {
	ProjectID  string
	ActorKind  string
	ActorID    string
	Action     string
	TargetKind string
	Since      string // created_at >= Since
	Until      string // created_at <= Until
	// BeforeCreatedAt/BeforeID are the keyset-pagination cursor: rows strictly older than this
	// (created_at, id) pair. Both must be set together; they come from the last row of the
	// previous page.
	BeforeCreatedAt string
	BeforeID        string
	Limit           int // ≤0 means 100
}

// List returns entries newest first, narrowed by the filter.
func (r *AuditRepo) List(ctx context.Context, f AuditFilter) ([]domain.AuditEntry, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	var (
		where []string
		args  []any
	)
	add := func(cond string, vals ...any) {
		where = append(where, cond)
		args = append(args, vals...)
	}
	if f.ProjectID != "" {
		add("project_id = ?", f.ProjectID)
	}
	if f.ActorKind != "" {
		add("actor_kind = ?", f.ActorKind)
	}
	if f.ActorID != "" {
		add("actor_id = ?", f.ActorID)
	}
	if f.Action != "" {
		add("action = ?", f.Action)
	}
	if f.TargetKind != "" {
		add("target_kind = ?", f.TargetKind)
	}
	if f.Since != "" {
		add("created_at >= ?", f.Since)
	}
	if f.Until != "" {
		add("created_at <= ?", f.Until)
	}
	if f.BeforeCreatedAt != "" && f.BeforeID != "" {
		add("(created_at < ? OR (created_at = ? AND id < ?))",
			f.BeforeCreatedAt, f.BeforeCreatedAt, f.BeforeID)
	}

	q := `SELECT ` + auditCols + ` FROM audit_log`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := r.h.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.AuditEntry
	for rows.Next() {
		e, err := scanAudit(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Recent returns the newest entries, optionally scoped to a project (empty projectID = all).
func (r *AuditRepo) Recent(ctx context.Context, projectID string, limit int) ([]domain.AuditEntry, error) {
	return r.List(ctx, AuditFilter{ProjectID: projectID, Limit: limit})
}

func scanAudit(rows *sql.Rows) (domain.AuditEntry, error) {
	var (
		e                 domain.AuditEntry
		actorKind         string
		projID, actorID   sql.NullString
		beforeJS, afterJS sql.NullString
	)
	err := rows.Scan(&e.ID, &projID, &actorKind, &actorID, &e.Action, &e.TargetKind,
		&e.TargetID, &beforeJS, &afterJS, &e.Note, &e.CreatedAt)
	if err != nil {
		return e, err
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
	return e, nil
}
