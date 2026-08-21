// Package store owns the SQLite database: connections, migrations, transactions and the
// repositories (story S03, D-2).
//
// One file, two *sql.DB handles: a single dedicated write connection (SQLite has one writer at a
// time; queueing writes in-process beats colliding on SQLITE_BUSY) and a pool of readers, which
// WAL mode lets run concurrently with the writer. Both run with foreign_keys=ON and
// busy_timeout=5000.
//
// The driver is modernc.org/sqlite — pure Go, no cgo, so cross-compilation stays free (D-2).
// Repositories are hand-written SQL over typed structs from internal/domain; JSON columns pass
// through typed Go types wherever plan/02-data-model.md defines a shape.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// busyTimeoutMS is how long a connection waits on a locked database before failing (D-2).
const busyTimeoutMS = 5000

// Store is the open database. It is safe for concurrent use; create one per process and share
// it through kernel.Kernel.Store().
type Store struct {
	path   string
	write  *sql.DB
	read   *sql.DB
	logger *slog.Logger
}

// Options configures Open. The zero value of everything but Path is usable.
type Options struct {
	// Path is the database file. Required; use a file in t.TempDir() in tests.
	Path string
	// Logger receives the migration log line. Nil means slog.Default().
	Logger *slog.Logger
	// ReadPoolSize caps the reader pool. Zero means a small default.
	ReadPoolSize int
}

// Open opens (creating if absent) the database at opts.Path, with WAL, foreign keys and the busy
// timeout applied to every connection. It does not migrate; call Migrate before first use.
func Open(opts Options) (*Store, error) {
	if opts.Path == "" {
		return nil, fmt.Errorf("store: no database path given")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	poolSize := opts.ReadPoolSize
	if poolSize <= 0 {
		poolSize = 4
	}

	// _txlock=immediate makes the write connection take the write lock at BEGIN rather than at
	// the first write, so two in-process transactions cannot deadlock upgrading read → write.
	write, err := sql.Open("sqlite", dsn(opts.Path, true))
	if err != nil {
		return nil, fmt.Errorf("store: open write connection: %w", err)
	}
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	write.SetConnMaxLifetime(0)

	read, err := sql.Open("sqlite", dsn(opts.Path, false))
	if err != nil {
		_ = write.Close()
		return nil, fmt.Errorf("store: open read pool: %w", err)
	}
	read.SetMaxOpenConns(poolSize)
	read.SetMaxIdleConns(poolSize)
	read.SetConnMaxLifetime(0)

	s := &Store{path: opts.Path, write: write, read: read, logger: logger}
	if err := write.Ping(); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("store: open %s: %w", opts.Path, mapErr(err))
	}
	return s, nil
}

// dsn builds the driver DSN. Pragmas ride along as _pragma query parameters, which the modernc
// driver applies to every new connection — a pool must not depend on "someone ran PRAGMA once".
func dsn(path string, writer bool) string {
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMS))
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	// NORMAL is the WAL-appropriate durability level: a crash loses at most the final commit's
	// fsync, never consistency. FULL would fsync per commit for no benefit a laptop can feel.
	q.Add("_pragma", "synchronous(NORMAL)")
	if writer {
		q.Set("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}

// Path is the database file this store opened.
func (s *Store) Path() string { return s.path }

// Close closes both handles. The store is unusable afterwards.
func (s *Store) Close() error {
	var first error
	if err := s.read.Close(); err != nil {
		first = err
	}
	if err := s.write.Close(); err != nil && first == nil {
		first = err
	}
	return first
}

// Writer is the write connection, for the rare caller (the migrator, the seeder) that needs raw
// access. Everything else goes through a repository or Tx.
func (s *Store) Writer() *sql.DB { return s.write }

// Reader is the read pool.
func (s *Store) Reader() *sql.DB { return s.read }

// dbtx is what a repository runs SQL against: the read pool, the write connection, or a
// transaction. database/sql's three types all satisfy it.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// handle is the pair every repository works over. Outside a transaction reads go to the pool and
// writes to the write connection; inside one, both are the transaction.
type handle struct {
	r dbtx
	w dbtx
}

func (s *Store) handle() handle { return handle{r: s.read, w: s.write} }
