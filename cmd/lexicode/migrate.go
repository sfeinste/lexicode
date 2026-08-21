package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/spruce/lexicode/internal/config"
)

// cmdMigrate applies pending database migrations. The store and the migration set arrive in story
// S03; until then this exists so that the subcommand, its flags and its output contract are real.
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

	fmt.Fprintf(stdout, "data dir: %s\n", cfg.DataDir)
	fmt.Fprintln(stdout, "no migrations")
	return nil
}
