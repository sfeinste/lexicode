package docker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/spruce/lexicode/internal/kernel/ports"
)

// Instance is one live container, implementing ports.Instance. It also carries one capability
// the frozen port does not: Logs, the container log stream Reattach resumes from. Only the
// docker-aware caller (the S20 runtime adapter, via the wiring site) reaches for the concrete
// type — the same precedent as the github module's ListDir.
type Instance struct {
	cli         *client.Client
	containerID string
	ref         ports.InstanceRef
	logger      *slog.Logger
}

// Ref implements ports.Instance.
func (i *Instance) Ref() ports.InstanceRef { return i.ref }

// ContainerID exposes the raw container ID for logging and tests. Not part of the port.
func (i *Instance) ContainerID() string { return i.containerID }

// Exec implements ports.Instance: start argv inside the container with attached streams.
// Without a TTY the Docker attach stream is multiplexed; it is demultiplexed here so Stdout
// and Stderr are the plain streams the port promises. Wait polls the exec's exit code after
// the process ends and then releases the connection; call it exactly once, after reading the
// streams to EOF (or from another goroutine while reading).
//
// A first failure is retried once against a revived container. The container runs as root
// with a writable rootfs (see the "Container posture" block in sandbox.go), so the agent can
// end the container's life from inside it — `kill 1`, a `pkill` that catches `sleep
// infinity`, an OOM kill after a runaway build. The teardown path then finds a stopped
// container and the §10.5 artifact push — the one thing standing between a failed run and
// losing the agent's work — would fail before it started. The workspace is an anonymous
// volume that outlives the process, so starting the container again gets the work back.
func (i *Instance) Exec(ctx context.Context, argv []string, opts ports.ExecOpts) (ports.Streams, error) {
	st, err := i.exec(ctx, argv, opts)
	if err == nil {
		return st, nil
	}
	if !i.revive(ctx, err) {
		return ports.Streams{}, err
	}
	return i.exec(ctx, argv, opts)
}

// revive restarts a container that is not running any more, reporting whether a retry is
// worth attempting. A container that is gone, or that is running (so the failure was
// something else), is not revivable.
func (i *Instance) revive(ctx context.Context, cause error) bool {
	insp, err := i.cli.ContainerInspect(ctx, i.containerID)
	if err != nil || insp.State == nil || insp.State.Running {
		return false
	}
	if err := i.cli.ContainerStart(ctx, i.containerID, container.StartOptions{}); err != nil {
		i.logger.Warn("docker: could not restart the run's stopped container",
			slog.String("container", shortID(i.containerID)),
			slog.String("error", err.Error()))
		return false
	}
	i.logger.Warn("docker: the run's container had stopped; restarted it to run one more command",
		slog.String("container", shortID(i.containerID)),
		slog.String("exit_code", fmt.Sprint(insp.State.ExitCode)),
		slog.String("cause", cause.Error()))
	return true
}

func (i *Instance) exec(ctx context.Context, argv []string, opts ports.ExecOpts) (ports.Streams, error) {
	created, err := i.cli.ContainerExecCreate(ctx, i.containerID, container.ExecOptions{
		Cmd:          argv,
		WorkingDir:   opts.WorkDir, // "" = the container's default, /workspace
		Env:          sortedEnv(opts.Env),
		Tty:          opts.TTY,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return ports.Streams{}, fmt.Errorf("docker: creating exec: %w", err)
	}

	resp, err := i.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{Tty: opts.TTY})
	if err != nil {
		return ports.Streams{}, fmt.Errorf("docker: attaching exec: %w", err)
	}

	var stdout, stderr io.Reader
	if opts.TTY {
		// A TTY merges stderr into stdout; the attach stream is raw.
		stdout = resp.Reader
		stderr = bytes.NewReader(nil)
	} else {
		or, ow := io.Pipe()
		er, ew := io.Pipe()
		go func() {
			_, err := stdcopy.StdCopy(ow, ew, resp.Reader)
			_ = ow.CloseWithError(err)
			_ = ew.CloseWithError(err)
		}()
		stdout, stderr = or, er
	}

	var closeOnce sync.Once
	wait := func() (int, error) {
		defer closeOnce.Do(resp.Close)
		for {
			insp, err := i.cli.ContainerExecInspect(ctx, created.ID)
			if err != nil {
				return -1, fmt.Errorf("docker: inspecting exec: %w", err)
			}
			if !insp.Running {
				return insp.ExitCode, nil
			}
			select {
			case <-ctx.Done():
				return -1, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
	}

	return ports.Streams{
		Stdin:  execStdin{resp: resp},
		Stdout: stdout,
		Stderr: stderr,
		Wait:   wait,
	}, nil
}

// execStdin adapts the hijacked connection's write half to io.WriteCloser: Close half-closes
// the write side so the process sees stdin EOF while output keeps flowing.
type execStdin struct {
	resp types.HijackedResponse
}

func (w execStdin) Write(p []byte) (int, error) { return w.resp.Conn.Write(p) }

func (w execStdin) Close() error { return w.resp.CloseWrite() }

// ReadFile implements ports.Instance. Relative paths resolve against the workspace root.
func (i *Instance) ReadFile(ctx context.Context, p string) ([]byte, error) {
	if !path.IsAbs(p) {
		p = path.Join(workspaceDir, p)
	}
	rc, _, err := i.cli.CopyFromContainer(ctx, i.containerID, p)
	if err != nil {
		return nil, fmt.Errorf("docker: reading %s: %w", p, err)
	}
	defer func() { _ = rc.Close() }()

	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("docker: reading %s: archive held no regular file", p)
		}
		if err != nil {
			return nil, fmt.Errorf("docker: reading %s: %w", p, err)
		}
		if hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
}

// Destroy implements ports.Instance: remove the container and its anonymous workspace volume.
// Idempotent — a container already gone is success, not an error.
func (i *Instance) Destroy(ctx context.Context) error {
	err := i.cli.ContainerRemove(ctx, i.containerID, container.RemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("docker: removing container %s: %w", shortID(i.containerID), err)
	}
	return nil
}

// Logs returns the container's log stream — everything written to the container's stdout and
// stderr (the main process, plus anything an exec'd process writes to /proc/1/fd/1|2) — with
// the first offset bytes skipped. This is how a crash-recovered process resumes exactly where
// the last one stopped: the caller persists how many bytes it consumed (runs.log_offset), the
// scheduler hands it back inside InstanceRef, and Reattach's instance skips it here. Not part
// of the frozen port; the runtime adapter reaches it through the concrete type.
func (i *Instance) Logs(ctx context.Context, offset int64, follow bool) (io.ReadCloser, error) {
	raw, err := i.cli.ContainerLogs(ctx, i.containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
	})
	if err != nil {
		return nil, fmt.Errorf("docker: opening log stream: %w", err)
	}

	// The daemon multiplexes stdout/stderr (the container runs without a TTY); demultiplex
	// both into one ordered stream, then skip what was already consumed. Offsets count
	// demultiplexed bytes, so they are stable across processes.
	pr, pw := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(pw, pw, raw)
		_ = pw.CloseWithError(err)
		_ = raw.Close()
	}()
	return &skipReadCloser{r: pr, skip: offset, close: func() error {
		_ = raw.Close()
		return pr.Close()
	}}, nil
}

// skipReadCloser discards the first skip bytes of r, then reads through.
type skipReadCloser struct {
	r     io.Reader
	skip  int64
	close func() error
}

func (s *skipReadCloser) Read(p []byte) (int, error) {
	if s.skip > 0 {
		if _, err := io.CopyN(io.Discard, s.r, s.skip); err != nil && err != io.EOF {
			return 0, err
		}
		s.skip = 0
	}
	return s.r.Read(p)
}

func (s *skipReadCloser) Close() error { return s.close() }

// runCmd runs argv to completion, streaming each combined-output line to onLine (when set) and
// returning the exit code plus a bounded tail of the combined output — enough for an error
// message that quotes what happened without holding megabytes.
func (i *Instance) runCmd(ctx context.Context, argv []string, env map[string]string, onLine func(string)) (int, string, error) {
	st, err := i.Exec(ctx, argv, ports.ExecOpts{Env: env})
	if err != nil {
		return -1, "", err
	}
	_ = st.Stdin.Close()

	var (
		mu  sync.Mutex
		out tailBuffer
		wg  sync.WaitGroup
	)
	consume := func(r io.Reader) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			mu.Lock()
			out.writeLine(line)
			mu.Unlock()
			if onLine != nil {
				onLine(line)
			}
		}
	}
	wg.Add(2)
	go consume(st.Stdout)
	go consume(st.Stderr)
	wg.Wait()

	code, err := st.Wait()
	return code, out.String(), err
}

// tailBuffer keeps the last maxTail bytes of what was written, line by line.
type tailBuffer struct {
	lines []string
	size  int
}

const maxTail = 64 * 1024

func (t *tailBuffer) writeLine(line string) {
	t.lines = append(t.lines, line)
	t.size += len(line) + 1
	for t.size > maxTail && len(t.lines) > 1 {
		t.size -= len(t.lines[0]) + 1
		t.lines = t.lines[1:]
	}
}

func (t *tailBuffer) String() string { return strings.Join(t.lines, "\n") }

var _ ports.Instance = (*Instance)(nil)
