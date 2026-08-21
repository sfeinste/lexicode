package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// UsersRepo reads and writes the users table.
type UsersRepo struct{ h handle }

// Users returns the users repository.
func (s *Store) Users() *UsersRepo { return &UsersRepo{h: s.handle()} }

// Users returns the users repository bound to this transaction.
func (t *Tx) Users() *UsersRepo { return &UsersRepo{h: t.handle()} }

const userCols = `id, email, display_name, password_hash, role, avatar_color, prefs, archived_at, created_at`

// Create inserts a user. A duplicate email surfaces as ErrUnique.
func (r *UsersRepo) Create(ctx context.Context, u *domain.User) error {
	prefs, err := jsonText(u.Prefs)
	if err != nil {
		return err
	}
	_, err = r.h.w.ExecContext(ctx, `
		INSERT INTO users (`+userCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Email, u.DisplayName, u.PasswordHash, string(u.Role), u.AvatarColor,
		prefs, nullStr(u.ArchivedAt), u.CreatedAt)
	return mapErr(err)
}

// ByID returns the user with this ID, or ErrNotFound.
func (r *UsersRepo) ByID(ctx context.Context, id string) (domain.User, error) {
	return scanUser(r.h.r.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// ByEmail returns the user with this email, or ErrNotFound.
func (r *UsersRepo) ByEmail(ctx context.Context, email string) (domain.User, error) {
	return scanUser(r.h.r.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM users WHERE email = ?`, email))
}

// List returns every user, archived included, oldest first.
func (r *UsersRepo) List(ctx context.Context) ([]domain.User, error) {
	rows, err := r.h.r.QueryContext(ctx,
		`SELECT `+userCols+` FROM users ORDER BY created_at, id`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Count returns how many users exist. Zero is what routes the SPA to first-run setup (S05).
func (r *UsersRepo) Count(ctx context.Context) (int64, error) {
	var n int64
	err := r.h.r.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, mapErr(err)
}

// Update rewrites a user's mutable fields (everything but id, email and created_at).
func (r *UsersRepo) Update(ctx context.Context, u *domain.User) error {
	prefs, err := jsonText(u.Prefs)
	if err != nil {
		return err
	}
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE users SET display_name = ?, password_hash = ?, role = ?, avatar_color = ?,
			prefs = ?, archived_at = ?
		WHERE id = ?`,
		u.DisplayName, u.PasswordHash, string(u.Role), u.AvatarColor,
		prefs, nullStr(u.ArchivedAt), u.ID)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

// errIfNone maps "zero rows changed" on a targeted update to ErrNotFound.
func errIfNone(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

func scanUser(row rowScanner) (domain.User, error) {
	var (
		u        domain.User
		role     string
		prefs    string
		archived sql.NullString
	)
	err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.PasswordHash, &role, &u.AvatarColor,
		&prefs, &archived, &u.CreatedAt)
	if err != nil {
		return domain.User{}, mapErr(err)
	}
	u.Role = domain.UserRole(role)
	u.ArchivedAt = strPtr(archived)
	if err := jsonScan(prefs, &u.Prefs); err != nil {
		return domain.User{}, err
	}
	return u, nil
}
