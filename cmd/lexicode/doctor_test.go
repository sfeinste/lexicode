package main

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"testing"
)

// A doctor run against an unreachable daemon fails, exits non-zero, and prints the fix — the
// whole point of the command is that "no" always comes with "here is what to do".
func TestDoctorFailsOnUnreachableDockerAndPrintsFixes(t *testing.T) {
	home := t.TempDir()
	var out, errOut bytes.Buffer
	code := run([]string{"doctor",
		"--data-dir", home,
		"--config", home + "/absent.yaml",
		"--docker-host", "tcp://127.0.0.1:1",
		"--port", fmt.Sprint(freePort(t)),
		"--proxy-port", fmt.Sprint(freePort(t)),
	}, &out, &errOut)
	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero when a check fails:\n%s", out.String())
	}
	body := out.String()
	for _, want := range []string{"FAIL  Docker", "fix:"} {
		if !strings.Contains(body, want) {
			t.Errorf("output lacks %q:\n%s", want, body)
		}
	}
	if !strings.Contains(errOut.String(), "checks failed") {
		t.Errorf("stderr = %q, want the failure summary", errOut.String())
	}
}

// A port held by an unrelated process is a failure that names the flag to move it.
func TestDoctorReportsAnOccupiedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	home := t.TempDir()
	var out, errOut bytes.Buffer
	code := run([]string{"doctor",
		"--data-dir", home,
		"--config", home + "/absent.yaml",
		"--docker-host", "tcp://127.0.0.1:1",
		"--port", fmt.Sprint(port),
		"--proxy-port", fmt.Sprint(freePort(t)),
	}, &out, &errOut)
	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero:\n%s", out.String())
	}
	want := fmt.Sprintf("FAIL  Port %d", port)
	if !strings.Contains(out.String(), want) {
		t.Fatalf("output lacks %q:\n%s", want, out.String())
	}
	if !strings.Contains(out.String(), "--port <other>") {
		t.Fatalf("the occupied-port fix does not name the flag:\n%s", out.String())
	}
}
