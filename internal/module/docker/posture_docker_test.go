//go:build docker

// The POC container posture, proved against a real daemon: the container an agent gets is
// unrestricted, so a run can install what it needs. See the "Container posture" block in
// sandbox.go for what was removed to make this true and how to put it back — these tests are
// the other half of that record, and they fail if the restrictions come back silently.
//
//	go test -tags docker -run TestPOC -timeout 30m ./internal/module/docker/
package docker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// postureContainer prepares a bare container under the shipped defaults (network policy open,
// no clone, no setup script) and returns it with cleanup registered.
func postureContainer(t *testing.T, projectID string) ports.Instance {
	t.Helper()
	sb := newTestSandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	t.Cleanup(cancel)
	inst, err := sb.Prepare(ctx, ports.SandboxSpec{
		RunID:     "run-" + domain.NewID(),
		ProjectID: projectID,
		Network:   ports.NetworkPolicy{Mode: ports.NetworkOpen},
		Limits: ports.ResourceLimits{
			CPUs: 2, MemoryBytes: 4 << 30, Pids: 512,
		},
	}, newTestSink(t))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { destroyQuietly(t, inst) })
	return inst
}

// skipWithoutEgress skips the test when the machine has no working egress from a container:
// these tests are about permission, not connectivity, and a laptop on a plane should not fail
// the suite.
func skipWithoutEgress(t *testing.T, inst ports.Instance, url string) {
	t.Helper()
	code, out := execOutput(t, inst, "curl", "-sS", "-o", "/dev/null", "-m", "30",
		"-w", "%{http_code}", url)
	if code != 0 {
		t.Skipf("no network from the container (curl %s: exit %d, %s); "+
			"this test proves the container may install things, not that it can reach a mirror",
			url, code, strings.TrimSpace(out))
	}
}

// longExec is execOutput with a timeout an apt or npm install can actually finish inside.
func longExec(t *testing.T, inst ports.Instance, d time.Duration, argv ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return execOutputCtx(ctx, t, inst, argv...)
}

// TestPOCContainerIsUsable is the point of the whole change: as root, on a writable rootfs,
// with a writable $HOME, a run can install a system package and a global npm package.
func TestPOCContainerIsUsable(t *testing.T) {
	inst := postureContainer(t, "proj-poc-usable")

	// 1. Root, on a writable root filesystem.
	if code, out := execOutput(t, inst, "id", "-u"); code != 0 || strings.TrimSpace(out) != "0" {
		t.Fatalf("id -u = %q (exit %d), want 0", strings.TrimSpace(out), code)
	}
	for _, path := range []string{"/usr/local/lib", "/usr/bin", "/etc", "/opt"} {
		if code, out := execOutput(t, inst, "/bin/sh", "-c",
			"touch "+path+"/.lexicode-posture-probe"); code != 0 {
			t.Errorf("%s is not writable: exit %d, %s", path, code, out)
		}
	}

	// 2. $HOME is writable — npm's cache and config, git's global config and claude's state
	// all live there, and under the read-only rootfs every one of those writes failed.
	code, out := execOutput(t, inst, "/bin/sh", "-c",
		`printf 'HOME=%s\n' "$HOME"; touch "$HOME/.lexicode-posture-probe" && `+
			`printf 'writable=yes\n' || printf 'writable=no\n'`)
	t.Logf("home probe: exit %d\n%s", code, out)
	if code != 0 || !strings.Contains(out, "writable=yes") {
		t.Errorf("$HOME is not writable: exit %d, %s", code, out)
	}

	skipWithoutEgress(t, inst, "https://deb.debian.org/")

	// 3. apt-get: the system package manager works, which it could not before — the failure
	// was permissions on a read-only /var and /usr, not the network.
	code, out = longExec(t, inst, 10*time.Minute, "/bin/sh", "-c",
		"apt-get update >/dev/null && apt-get install -y --no-install-recommends file "+
			">/dev/null && file --version | head -1")
	t.Logf("apt-get install file: exit %d\n%s", code, out)
	if code != 0 || !strings.Contains(out, "file-") {
		t.Fatalf("apt-get install failed: exit %d\n%s", code, out)
	}

	// 4. npm -g: writes into /usr/local/lib/node_modules plus a cache under $HOME.
	code, out = longExec(t, inst, 10*time.Minute, "/bin/sh", "-c",
		"npm install -g --silent cowsay >/dev/null 2>&1 && command -v cowsay && "+
			"cowsay -f default 'the sandbox is open' | head -3")
	t.Logf("npm install -g cowsay: exit %d\n%s", code, out)
	if code != 0 || !strings.Contains(out, "/cowsay") {
		t.Fatalf("npm install -g failed: exit %d\n%s", code, out)
	}
}

// goVersion is the toolchain TestPOCGoToolchainIsSelfService installs. Pinned so the test is
// deterministic; the point is that a run can fetch and install one at all, not which one.
const goVersion = "1.25.0"

// TestPOCGoToolchainIsSelfService is the concrete gap the owner hit: the base image has no Go,
// and under the old posture a run could not add one. Install the official tarball, compile a
// program, run it.
func TestPOCGoToolchainIsSelfService(t *testing.T) {
	inst := postureContainer(t, "proj-poc-go")
	skipWithoutEgress(t, inst, "https://go.dev/dl/")

	script := `set -e
arch="$(dpkg --print-architecture)"
curl -fsSL "https://go.dev/dl/go` + goVersion + `.linux-${arch}.tar.gz" -o /tmp/go.tar.gz
tar -C /usr/local -xzf /tmp/go.tar.gz
export PATH=/usr/local/go/bin:$PATH
go version
mkdir -p /workspace/hello
cd /workspace/hello
cat > main.go <<'EOF'
package main

import (
	"fmt"
	"runtime"
)

func main() {
	fmt.Printf("hello from %s/%s on %s\n", runtime.GOOS, runtime.GOARCH, runtime.Version())
}
EOF
go mod init hello >/dev/null
go build -o hello .
./hello`

	code, out := longExec(t, inst, 15*time.Minute, "/bin/sh", "-c", script)
	t.Logf("install Go %s, compile and run: exit %d\n%s", goVersion, code, out)
	if code != 0 {
		t.Fatalf("installing and using Go failed: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "go version go"+goVersion) {
		t.Errorf("output does not name the installed toolchain:\n%s", out)
	}
	if !strings.Contains(out, "hello from linux/") ||
		!strings.Contains(out, "go"+goVersion+"\n") {
		t.Errorf("the compiled program did not run:\n%s", out)
	}
}
