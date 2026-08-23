package docker

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

func TestBuiltinImageTag(t *testing.T) {
	tag := BuiltinImageTag()
	sum := sha256.Sum256(dockerfile)
	want := "lexicode/agent-base:" + hex.EncodeToString(sum[:])[:12]
	if tag != want {
		t.Errorf("BuiltinImageTag() = %q, want %q", tag, want)
	}
	if BuiltinImageTag() != tag {
		t.Error("tag is not stable across calls")
	}
}

// The D-7 image contents are a decision, not an accident; a Dockerfile edit that drops a
// required tool should fail loudly here before anyone builds it.
func TestDockerfileContents(t *testing.T) {
	df := string(dockerfile)
	for _, want := range []string{
		"FROM debian:bookworm-slim",
		"git", "curl", "jq", "ripgrep", "build-essential",
		"nodejs.org/dist", // pinned tarball install, not nodesource/nvm
		"@anthropic-ai/claude-code",
		"useradd -m -u 1000",
		"chown agent:agent /workspace",
		"USER agent",
		"WORKDIR /workspace",
	} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile is missing %q", want)
		}
	}
}

func TestFilesTar(t *testing.T) {
	r, err := filesTar(map[string][]byte{
		".claude/settings.json": []byte(`{"a":1}`),
		"prompt.md":             []byte("do the thing"),
	})
	if err != nil {
		t.Fatalf("filesTar: %v", err)
	}
	entries := map[string]string{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading tar: %v", err)
		}
		switch hdr.Typeflag {
		case tar.TypeReg:
			if hdr.Uid != 1000 || hdr.Gid != 1000 {
				t.Errorf("%s owned by %d:%d, want 1000:1000", hdr.Name, hdr.Uid, hdr.Gid)
			}
			b, _ := io.ReadAll(tr)
			entries[hdr.Name] = string(b)
		case tar.TypeDir:
			entries[hdr.Name] = "<dir>"
		}
	}
	if entries[".claude/"] != "<dir>" {
		t.Error("missing parent directory entry for .claude/")
	}
	if entries[".claude/settings.json"] != `{"a":1}` {
		t.Errorf("settings.json content = %q", entries[".claude/settings.json"])
	}
	if entries["prompt.md"] != "do the thing" {
		t.Errorf("prompt.md content = %q", entries["prompt.md"])
	}
}

// TestFilesTarMarksShebangFilesExecutable covers the S19 git-hook path: a hook git would
// silently skip without the executable bit.
func TestFilesTarMarksShebangFilesExecutable(t *testing.T) {
	r, err := filesTar(map[string][]byte{
		".lexicode/hooks/commit-msg": []byte("#!/bin/sh\nexit 0\n"),
		".lexicode/mcp.json":         []byte("{}"),
	})
	if err != nil {
		t.Fatalf("filesTar: %v", err)
	}
	modes := map[string]int64{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading tar: %v", err)
		}
		if hdr.Typeflag == tar.TypeReg {
			modes[hdr.Name] = hdr.Mode
		}
	}
	if modes[".lexicode/hooks/commit-msg"] != 0o755 {
		t.Errorf("commit-msg mode = %04o, want 0755", modes[".lexicode/hooks/commit-msg"])
	}
	if modes[".lexicode/mcp.json"] != 0o644 {
		t.Errorf("mcp.json mode = %04o, want 0644", modes[".lexicode/mcp.json"])
	}
}

func TestFilesTarRejectsEscapingPaths(t *testing.T) {
	for _, bad := range []string{"/etc/passwd", "../outside", "a/../../b", ""} {
		if _, err := filesTar(map[string][]byte{bad: nil}); err == nil {
			t.Errorf("filesTar accepted path %q", bad)
		}
	}
}

func TestPrepareNetworkRejectsUnknownMode(t *testing.T) {
	s := &Sandbox{}
	if _, err := s.prepareNetwork(context.Background(), ports.NetworkPolicy{Mode: "vpn"}); err == nil {
		t.Error("unknown network mode was accepted")
	}
}

func TestSweepRefusesWithoutRunStateLookup(t *testing.T) {
	s := &Sandbox{owner: "ws-test"}
	if _, err := s.Sweep(context.Background()); err == nil {
		t.Error("Sweep without a run-state lookup should refuse, not guess")
	}
}

// Without an owner identity the sweeper cannot tell its own containers from another Lexicode's,
// and "not my run" would mean "nobody's run". Refusing beats reaping someone else's live run.
func TestSweepRefusesWithoutOwner(t *testing.T) {
	s := &Sandbox{runState: func(context.Context, string) (domain.RunState, bool, error) {
		return "", false, nil // every run unknown: without the owner guard, everything looks orphaned
	}}
	if _, err := s.Sweep(context.Background()); err == nil {
		t.Error("Sweep without an owner identity should refuse, not guess")
	}
}

// Every container is stamped with the owner, so a sweep can filter on it.
func TestContainerLabelsCarryOwner(t *testing.T) {
	s := &Sandbox{owner: "ws-abc"}
	labels := s.containerLabels(ports.SandboxSpec{
		RunID:     "run-1",
		ProjectID: "proj-1",
		Labels:    map[string]string{"extra": "kept", labelOwner: "impostor", labelRun: "spoof"},
	}, "inst-1")

	want := map[string]string{
		labelOwner:    "ws-abc",
		labelRun:      "run-1",
		labelProject:  "proj-1",
		labelInstance: "inst-1",
		"extra":       "kept",
	}
	for k, v := range want {
		if labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, labels[k], v)
		}
	}
}

// The owner is the workspace: same data dir → same identity across restarts (so a boot sweep
// still recognises the containers the previous process left), different data dir → different
// identity (so the acceptance scripts and the product never see each other's containers).
func TestOwnerIDIdentifiesTheWorkspace(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()

	mine := ownerID(a)
	if mine == "" {
		t.Error("ownerID returned empty for a real data dir")
	}
	if again := ownerID(a); again != mine {
		t.Errorf("ownerID is not stable for one data dir: %q then %q", mine, again)
	}
	if theirs := ownerID(b); theirs == mine {
		t.Errorf("two data dirs share the owner id %q", mine)
	}
	if same := ownerID(a + string(filepath.Separator) + "."); same != mine {
		t.Errorf("ownerID(%q) = %q, want %q: the two paths are the same dir", a+"/.", same, mine)
	}
	if got := ownerID(""); got != "" {
		t.Errorf("ownerID(\"\") = %q, want empty so the caller can keep its fallback", got)
	}
	if strings.Contains(mine, a) {
		t.Error("ownerID leaks the operator's path into a container label")
	}
}

// Init derives the sandbox owner from the configured data dir; with no data dir the sandbox
// still has an owner (the per-process fallback), never an empty one that Sweep would refuse.
func TestModuleInitDerivesOwnerFromDataDir(t *testing.T) {
	dir := t.TempDir()
	m := New(Options{DataDir: dir})
	k := kernel.New(kernel.Options{})
	if err := k.RegisterModule(m); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}
	if err := k.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got, want := m.Sandbox().owner, ownerID(dir); got != want {
		t.Errorf("sandbox owner = %q, want %q", got, want)
	}

	bare := New(Options{})
	k2 := kernel.New(kernel.Options{})
	if err := k2.RegisterModule(bare); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}
	if err := k2.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if bare.Sandbox().owner != processOwner {
		t.Errorf("owner without a data dir = %q, want the process fallback %q",
			bare.Sandbox().owner, processOwner)
	}
}

// Module.Init constructs the client (no I/O) and registers the port under "docker".
func TestModuleInitRegistersSandbox(t *testing.T) {
	k := kernel.New(kernel.Options{})
	m := New(Options{})
	if err := k.RegisterModule(m); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}
	if err := k.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	sb, err := k.Sandbox("docker")
	if err != nil {
		t.Fatalf("Sandbox(docker): %v", err)
	}
	if sb.ID() != "docker" {
		t.Errorf("sandbox ID = %q", sb.ID())
	}
	if m.Sandbox() == nil {
		t.Error("Module.Sandbox() is nil after Init")
	}
}

func TestImageMissingToolsErrorShape(t *testing.T) {
	err := &ports.ImageMissingToolsError{Image: "debian:bookworm-slim", Missing: []string{"claude"}}
	if !strings.Contains(err.Error(), "image_missing_tools") {
		t.Errorf("error %q does not carry the stable name", err.Error())
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("error %q does not name the missing tool", err.Error())
	}
}

func TestTailBufferKeepsTheTail(t *testing.T) {
	var b tailBuffer
	long := strings.Repeat("x", 1024)
	for i := 0; i < 200; i++ {
		b.writeLine(long)
	}
	b.writeLine("the-end")
	s := b.String()
	if len(s) > maxTail+1030 {
		t.Errorf("tail buffer grew to %d bytes", len(s))
	}
	if !strings.HasSuffix(s, "the-end") {
		t.Error("tail buffer lost the last line")
	}
}

func TestSkipReadCloserSkips(t *testing.T) {
	src := io.NopCloser(strings.NewReader("0123456789"))
	r := &skipReadCloser{r: src, skip: 4, close: src.Close}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != "456789" {
		t.Errorf("read %q, want %q", b, "456789")
	}
}
