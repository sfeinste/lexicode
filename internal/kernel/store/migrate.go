package store

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/migrations"
)

// Migrate applies every embedded migration that has not been applied yet, each inside its own
// transaction together with its schema_migrations row — so a half-applied migration cannot be
// recorded and a recorded one cannot be half-applied. It returns the versions applied by this
// call (empty on a no-op restart) and logs one line naming them either way.
//
// Migrations are forward-only (D-2): there is no down path, and a version that exists in the
// database but not in the binary is an error — it means the binary is older than its data.
func (s *Store) Migrate(ctx context.Context) ([]string, error) {
	if _, err := s.write.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return nil, fmt.Errorf("store: create schema_migrations: %w", mapErr(err))
	}

	all, err := loadMigrations()
	if err != nil {
		return nil, err
	}

	done, err := s.appliedVersions(ctx)
	if err != nil {
		return nil, err
	}

	known := make(map[string]bool, len(all))
	for _, m := range all {
		known[m.version] = true
	}
	for v := range done {
		if !known[v] {
			return nil, fmt.Errorf(
				"store: database has migration %q this binary does not know; the binary is older than its database", v)
		}
	}

	var applied []string
	for _, m := range all {
		if done[m.version] {
			continue
		}
		if err := s.applyOne(ctx, m); err != nil {
			return applied, err
		}
		applied = append(applied, m.version)
	}

	versions := make([]string, 0, len(all))
	for _, m := range all {
		versions = append(versions, m.version)
	}
	s.logger.Info("database migrated",
		slog.String("path", s.path),
		slog.Any("applied", applied),
		slog.Any("versions", versions),
	)
	return applied, nil
}

type migration struct {
	version string // "0001_init"
	sql     string
}

// loadMigrations reads the embedded *.up.sql files in lexical order, which the NNNN_ prefix
// makes chronological order.
func loadMigrations() ([]migration, error) {
	entries, err := fs.Glob(migrations.FS, "*.up.sql")
	if err != nil {
		return nil, fmt.Errorf("store: list migrations: %w", err)
	}
	sort.Strings(entries)

	out := make([]migration, 0, len(entries))
	for _, name := range entries {
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return nil, fmt.Errorf("store: read migration %s: %w", name, err)
		}
		out = append(out, migration{
			version: strings.TrimSuffix(name, ".up.sql"),
			sql:     string(body),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("store: no embedded migrations; the migrations package is broken")
	}
	return out, nil
}

func (s *Store) appliedVersions(ctx context.Context) (map[string]bool, error) {
	rows, err := s.write.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", mapErr(err))
	}
	defer func() { _ = rows.Close() }()

	done := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("store: scan schema_migrations: %w", err)
		}
		done[v] = true
	}
	return done, rows.Err()
}

func (s *Store) applyOne(ctx context.Context, m migration) error {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migration %s: %w", m.version, mapErr(err))
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("store: apply migration %s: %w", m.version, mapErr(err))
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		m.version, domain.Now()); err != nil {
		return fmt.Errorf("store: record migration %s: %w", m.version, mapErr(err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration %s: %w", m.version, mapErr(err))
	}
	return nil
}
