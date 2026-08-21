package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spruce/lexicode/internal/config"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// cmdMigrate opens (creating if needed) the database and applies pending migrations, printing
// what it applied. Serve migrates on boot too; this subcommand exists for scripts and for
// checking a database without starting the server.
func cmdMigrate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("lexicode migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	config.Bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(config.Options{Flags: fs})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory %s: %w", cfg.DataDir, err)
	}

	// The subcommand's report is its stdout; the store's own log line goes to stderr at warn+
	// so that scripts parsing stdout see only the report.
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	st, err := store.Open(store.Options{Path: cfg.DBFile(), Logger: logger})
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	applied, err := st.Migrate(context.Background())
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "database: %s\n", st.Path())
	if len(applied) == 0 {
		fmt.Fprintln(stdout, "up to date; nothing to apply")
		return nil
	}
	for _, v := range applied {
		fmt.Fprintf(stdout, "applied %s\n", v)
	}
	return nil
}
