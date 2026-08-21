package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Tx is one write transaction. Repositories obtained from it run inside the transaction —
// reads included, so a transaction observes its own writes.
type Tx struct {
	tx *sql.Tx
}

// Exec runs a statement inside the transaction, for the odd write no repository covers yet.
func (t *Tx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := t.tx.ExecContext(ctx, query, args...)
	return res, mapErr(err)
}

// Query runs a query inside the transaction.
func (t *Tx) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := t.tx.QueryContext(ctx, query, args...)
	return rows, mapErr(err)
}

// QueryRow runs a single-row query inside the transaction.
func (t *Tx) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, query, args...)
}

func (t *Tx) handle() handle { return handle{r: t.tx, w: t.tx} }

// Tx runs fn inside one transaction on the write connection. fn returning nil commits; an error
// or a panic rolls back — the panic is re-raised after the rollback, never swallowed. Nested
// calls are not supported: a repository must never open its own transaction.
func (s *Store) Tx(ctx context.Context, fn func(*Tx) error) (err error) {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", mapErr(err))
	}

	done := false
	defer func() {
		if !done {
			// Panic path (or an early return this function does not have): roll back and let
			// the panic continue. Rollback's own error is unreachable data at this point.
			_ = tx.Rollback()
		}
	}()

	if err := fn(&Tx{tx: tx}); err != nil {
		done = true
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("%w (and rollback failed: %v)", err, rbErr)
		}
		return err
	}

	done = true
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", mapErr(err))
	}
	return nil
}
