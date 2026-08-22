// Command lexicode is the Lexicode server: one binary that serves the dashboard, orchestrates
// agent runs and owns the local database.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// version is injected at build time with -ldflags "-X main.version=...". "dev" means someone ran
// go build without the Makefile.
var version = "dev"

// commit is injected the same way and may be empty.
var commit = ""

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// errUsage asks the caller to print usage and exit 2.
var errUsage = errors.New("usage")

func run(args []string, stdout, stderr io.Writer) int {
	root := flag.NewFlagSet("lexicode", flag.ContinueOnError)
	root.SetOutput(stderr)
	showVersion := root.Bool("version", false, "print the version and exit")
	root.Usage = func() { usage(stderr) }

	// Stop parsing at the first non-flag argument so that "lexicode serve --port 8080" gives the
	// port to the subcommand, not to the root flag set.
	if err := root.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(stdout)
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, versionString())
		return 0
	}

	rest := root.Args()
	if len(rest) == 0 {
		usage(stderr)
		return 2
	}

	name, cmdArgs := rest[0], rest[1:]
	var err error
	switch name {
	case "serve":
		err = cmdServe(cmdArgs, stdout, stderr)
	case "migrate":
		err = cmdMigrate(cmdArgs, stdout, stderr)
	case "doctor":
		err = cmdDoctor(cmdArgs, stdout, stderr)
	case "version":
		err = cmdVersion(cmdArgs, stdout, stderr)
	case "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "lexicode: unknown command %q\n\n", name)
		usage(stderr)
		return 2
	}

	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		return 2
	default:
		fmt.Fprintln(stderr, "lexicode: "+err.Error())
		return 1
	}
}

func versionString() string {
	if commit == "" {
		return version
	}
	return version + " (" + commit + ")"
}

func usage(w io.Writer) {
	fmt.Fprint(w, strings.TrimLeft(`
lexicode — run coding agents against your repositories.

Usage:
  lexicode <command> [flags]

Commands:
  serve      run the HTTP server and the orchestrator
  doctor     check Docker, credentials, ports and disk, and print the fix for each failure
  migrate    apply pending database migrations and exit
  version    print the version and exit

Flags:
  --version  print the version and exit
  --help     print this message

Run "lexicode <command> --help" for the flags of a command.
`, "\n"))
}
