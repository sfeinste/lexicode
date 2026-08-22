package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"

	"github.com/spruce/lexicode/internal/kernel/ports"
)

// ensureImage is the "image" step: the built-in image is built on demand from the embedded
// Dockerfile (D-7); a custom image_ref is pulled if absent. Returns the image reference to
// create the container from.
func (s *Sandbox) ensureImage(ctx context.Context, custom string, sink ports.ProvisionSink) (string, error) {
	const step = "image"

	if custom != "" {
		ok, err := s.imageExists(ctx, custom)
		if err != nil {
			sink.Step(step, ports.StepFailed, err.Error())
			return "", err
		}
		if ok {
			sink.Step(step, ports.StepOK, custom+" (cached)")
			return custom, nil
		}
		sink.Step(step, ports.StepRunning, "pulling "+custom)
		if err := s.pullImage(ctx, custom, sink); err != nil {
			sink.Step(step, ports.StepFailed, err.Error())
			return "", err
		}
		sink.Step(step, ports.StepOK, custom+" (pulled)")
		return custom, nil
	}

	tag := BuiltinImageTag()
	ok, err := s.imageExists(ctx, tag)
	if err != nil {
		sink.Step(step, ports.StepFailed, err.Error())
		return "", err
	}
	if ok {
		sink.Step(step, ports.StepOK, "ready (cached)")
		return tag, nil
	}

	sink.Step(step, ports.StepRunning, "building "+tag)
	start := time.Now()
	// Concurrent runs needing the same image share one build. Only the run that starts the
	// build streams its progress; followers wait and report the result. The winner's ctx
	// governs the build — a follower whose own ctx ends first stops waiting via shared below.
	result := s.build.DoChan(tag, func() (any, error) {
		return nil, s.buildBuiltin(ctx, tag, sink)
	})
	select {
	case r := <-result:
		if r.Err != nil {
			sink.Step(step, ports.StepFailed, r.Err.Error())
			return "", r.Err
		}
	case <-ctx.Done():
		sink.Step(step, ports.StepFailed, ctx.Err().Error())
		return "", ctx.Err()
	}
	sink.Step(step, ports.StepOK, "built "+time.Since(start).Round(time.Second).String())
	return tag, nil
}

func (s *Sandbox) imageExists(ctx context.Context, ref string) (bool, error) {
	args := filters.NewArgs(filters.Arg("reference", ref))
	list, err := s.cli.ImageList(ctx, image.ListOptions{Filters: args})
	if err != nil {
		return false, fmt.Errorf("docker: listing images: %w", err)
	}
	return len(list) > 0, nil
}

// buildBuiltin builds the embedded Dockerfile into tag, streaming progress lines to sink.Log.
// The build context is exactly one file — the Dockerfile — so the daemon fetches everything
// else itself.
func (s *Sandbox) buildBuiltin(ctx context.Context, tag string, sink ports.ProvisionSink) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(dockerfile))}); err != nil {
		return fmt.Errorf("docker: writing build context: %w", err)
	}
	if _, err := tw.Write(dockerfile); err != nil {
		return fmt.Errorf("docker: writing build context: %w", err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("docker: writing build context: %w", err)
	}

	resp, err := s.cli.ImageBuild(ctx, &buf, build.ImageBuildOptions{
		Tags:        []string{tag},
		Dockerfile:  "Dockerfile",
		Remove:      true,
		ForceRemove: true,
	})
	if err != nil {
		return fmt.Errorf("docker: building %s: %w", tag, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := drainJSONStream(resp.Body, sink); err != nil {
		return fmt.Errorf("docker: building %s: %w", tag, err)
	}
	return nil
}

// pullImage pulls a custom image_ref, streaming coarse progress to sink.Log.
func (s *Sandbox) pullImage(ctx context.Context, ref string, sink ports.ProvisionSink) error {
	rc, err := s.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("docker: pulling %s: %w", ref, err)
	}
	defer func() { _ = rc.Close() }()
	if err := drainJSONStream(rc, sink); err != nil {
		return fmt.Errorf("docker: pulling %s: %w", ref, err)
	}
	return nil
}

// streamMsg is the subset of the daemon's JSON progress stream that matters here: build output
// lines, pull status lines, and the error record that means the operation failed.
type streamMsg struct {
	Stream      string `json:"stream"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	ErrorDetail *struct {
		Message string `json:"message"`
	} `json:"errorDetail"`
}

// drainJSONStream decodes a build/pull progress stream, forwarding human-readable lines to
// sink.Log and returning the daemon's error if the operation failed. Per-layer byte counters
// are dropped — the checklist wants log lines, not a progress bar repainted 60 times a second.
func drainJSONStream(r io.Reader, sink ports.ProvisionSink) error {
	dec := json.NewDecoder(r)
	for {
		var msg streamMsg
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decoding daemon stream: %w", err)
		}
		if msg.Error != "" || msg.ErrorDetail != nil {
			detail := msg.Error
			if msg.ErrorDetail != nil && msg.ErrorDetail.Message != "" {
				detail = msg.ErrorDetail.Message
			}
			return errors.New(detail)
		}
		for _, line := range []string{msg.Stream, msg.Status} {
			if line = strings.TrimSpace(line); line != "" {
				sink.Log(line)
			}
		}
	}
}

// filesTar packs spec.Files into a tar rooted at the workspace. Paths must be relative and
// must not escape (".."); entries are owned by the agent user (uid/gid 1000) so the run can
// read and overwrite them.
func filesTar(files map[string][]byte) (io.Reader, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	seenDirs := map[string]bool{}

	for _, name := range names {
		clean := path.Clean(name)
		if clean == "." || clean == "" || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("file path %q must be relative to the workspace and must not escape it", name)
		}

		// Parent directories first, outermost in.
		var parents []string
		for dir := path.Dir(clean); dir != "."; dir = path.Dir(dir) {
			parents = append(parents, dir)
		}
		for i := len(parents) - 1; i >= 0; i-- {
			dir := parents[i]
			if seenDirs[dir] {
				continue
			}
			seenDirs[dir] = true
			if err := tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeDir, Name: dir + "/", Mode: 0o755, Uid: 1000, Gid: 1000,
			}); err != nil {
				return nil, fmt.Errorf("packing %s: %w", dir, err)
			}
		}

		content := files[name]
		// Shebang files are executable: S19 materializes git hooks (.lexicode/hooks/…)
		// through this path, and git silently skips a hook without the executable bit.
		mode := int64(0o644)
		if bytes.HasPrefix(content, []byte("#!")) {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: clean, Mode: mode, Size: int64(len(content)), Uid: 1000, Gid: 1000,
		}); err != nil {
			return nil, fmt.Errorf("packing %s: %w", name, err)
		}
		if _, err := tw.Write(content); err != nil {
			return nil, fmt.Errorf("packing %s: %w", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}
