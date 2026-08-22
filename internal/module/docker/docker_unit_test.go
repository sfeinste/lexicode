package docker

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

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
	s := &Sandbox{}
	if _, err := s.Sweep(context.Background()); err == nil {
		t.Error("Sweep without a run-state lookup should refuse, not guess")
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
