package store

import (
	"database/sql"
	"errors"
	"fmt"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// The store's error vocabulary. Callers match with errors.Is: every constraint violation matches
// ErrConstraint, and additionally the specific kind it was. The driver error stays in the chain
// for logging, but no caller should ever need to parse its message.
var (
	// ErrNotFound is returned when a lookup matches no row.
	ErrNotFound = errors.New("not found")
	// ErrConstraint matches every constraint violation, whatever the kind.
	ErrConstraint = errors.New("constraint violated")
	// ErrForeignKey is a foreign-key violation: the row references something that does not
	// exist, or something still references the row being deleted (deletes are RESTRICT, D-2).
	ErrForeignKey = errors.New("foreign key constraint violated")
	// ErrUnique is a uniqueness violation (duplicate email, duplicate dedupe_key, ...).
	ErrUnique = errors.New("unique constraint violated")
	// ErrCheck is a CHECK violation — a value outside the schema's enum or json_valid rules.
	ErrCheck = errors.New("check constraint violated")
	// ErrNotNull is a NOT NULL violation.
	ErrNotNull = errors.New("not null constraint violated")
)

// ConstraintError wraps a driver-level constraint failure with its kind.
type ConstraintError struct {
	// Kind is one of ErrForeignKey, ErrUnique, ErrCheck, ErrNotNull, or ErrConstraint when
	// SQLite reported a constraint class this store does not distinguish.
	Kind error
	err  error
}

// Error renders the kind and the driver's message.
func (e *ConstraintError) Error() string {
	return fmt.Sprintf("%v: %v", e.Kind, e.err)
}

// Unwrap exposes the driver error for logging.
func (e *ConstraintError) Unwrap() error { return e.err }

// Is matches ErrConstraint and the specific kind.
func (e *ConstraintError) Is(target error) bool {
	return target == ErrConstraint || target == e.Kind
}

// mapErr converts driver and database/sql errors into the store's vocabulary. Every repository
// return path runs through it.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_CONSTRAINT {
		kind := ErrConstraint
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY, sqlite3.SQLITE_CONSTRAINT_TRIGGER:
			kind = ErrForeignKey
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY, sqlite3.SQLITE_CONSTRAINT_ROWID:
			kind = ErrUnique
		case sqlite3.SQLITE_CONSTRAINT_CHECK:
			kind = ErrCheck
		case sqlite3.SQLITE_CONSTRAINT_NOTNULL:
			kind = ErrNotNull
		}
		return &ConstraintError{Kind: kind, err: err}
	}
	return err
}
