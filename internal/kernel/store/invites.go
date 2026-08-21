package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// InvitesRepo reads and writes the invites table.
type InvitesRepo struct{ h handle }

// Invites returns the invites repository.
func (s *Store) Invites() *InvitesRepo { return &InvitesRepo{h: s.handle()} }

// Invites returns the invites repository bound to this transaction.
func (t *Tx) Invites() *InvitesRepo { return &InvitesRepo{h: t.handle()} }

const inviteCols = `id, token_hash, role, created_by, expires_at, redeemed_by, created_at`

// Create inserts an invite.
func (r *InvitesRepo) Create(ctx context.Context, inv *domain.Invite) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO invites (`+inviteCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		inv.ID, inv.TokenHash, string(inv.Role), inv.CreatedBy, inv.ExpiresAt,
		nullStr(inv.RedeemedBy), inv.CreatedAt)
	return mapErr(err)
}

// ByTokenHash returns the invite whose token hashes to this value, or ErrNotFound.
func (r *InvitesRepo) ByTokenHash(ctx context.Context, tokenHash string) (domain.Invite, error) {
	return scanInvite(r.h.r.QueryRowContext(ctx,
		`SELECT `+inviteCols+` FROM invites WHERE token_hash = ?`, tokenHash))
}

// Redeem marks an unredeemed invite as redeemed by this user. Redeeming an already-redeemed
// invite returns ErrNotFound — one link, one member.
func (r *InvitesRepo) Redeem(ctx context.Context, id, userID string) error {
	res, err := r.h.w.ExecContext(ctx,
		`UPDATE invites SET redeemed_by = ? WHERE id = ? AND redeemed_by IS NULL`, userID, id)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

// List returns every invite, newest first.
func (r *InvitesRepo) List(ctx context.Context) ([]domain.Invite, error) {
	rows, err := r.h.r.QueryContext(ctx,
		`SELECT `+inviteCols+` FROM invites ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Invite
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func scanInvite(row rowScanner) (domain.Invite, error) {
	var (
		inv      domain.Invite
		role     string
		redeemed sql.NullString
	)
	err := row.Scan(&inv.ID, &inv.TokenHash, &role, &inv.CreatedBy, &inv.ExpiresAt,
		&redeemed, &inv.CreatedAt)
	if err != nil {
		return domain.Invite{}, mapErr(err)
	}
	inv.Role = domain.UserRole(role)
	inv.RedeemedBy = strPtr(redeemed)
	return inv, nil
}
