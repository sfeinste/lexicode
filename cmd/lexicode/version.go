package main

import (
	"flag"
	"fmt"
	"io"
	"runtime"
)

func cmdVersion(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("lexicode version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	long := fs.Bool("long", false, "also print the Go version and target platform")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if !*long {
		fmt.Fprintln(stdout, versionString())
		return nil
	}
	fmt.Fprintf(stdout, "lexicode %s\n", versionString())
	fmt.Fprintf(stdout, "go       %s\n", runtime.Version())
	fmt.Fprintf(stdout, "platform %s/%s\n", runtime.GOOS, runtime.GOARCH)
	return nil
}
