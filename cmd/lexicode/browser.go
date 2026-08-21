package main

import (
	"fmt"
	"os/exec"
	"runtime"
)

// openBrowser opens url in the user's default browser. Failing to open one is never fatal: the
// server prints the URL either way.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open %s: %w", url, err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
