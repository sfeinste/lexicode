package ports

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Sandbox is an execution substrate for a run: it prepares an isolated workspace and hands back
// an instance that commands can be executed in. Transcribed verbatim from contracts §2.3
// (story S17). V1 ships one implementation, the Docker adapter in internal/module/docker.
//
// Prepare owns the whole provisioning checklist of architecture §10.3 — image ready, container
// created, repo cloned, branch created, setup script — reporting each discrete step through the
// ProvisionSink so the UI renders a checklist, never a spinner. The clone runs *inside* the
// container (exec'd git), so repository credentials embedded in the clone URL never touch the
// host filesystem.
type Sandbox interface {
	// ID is the stable identifier, e.g. "docker". It must be unique across registered sandboxes.
	ID() string
	// Available is the preflight check — can this sandbox create instances right now? The
	// error is surfaced as the module's degraded state; the server keeps working without it.
	Available(ctx context.Context) error
	// Prepare provisions a ready-to-run instance: image, container, workspace clone, branch,
	// files, setup script. Every discrete step is reported through sink. On error the adapter
	// cleans up whatever it created; nothing to Destroy remains.
	Prepare(ctx context.Context, spec SandboxSpec, sink ProvisionSink) (Instance, error)
	// Reattach finds a live instance created by an earlier process, for crash recovery
	// (architecture §10.6). ref.LogOffset carries how much of the instance's log stream was
	// already consumed, so the caller resumes without re-emitting. A missing or dead
	// container fails with an error matching ErrInstanceGone.
	Reattach(ctx context.Context, ref InstanceRef) (Instance, error)
}

// SandboxSpec is everything Prepare needs. The kernel assembles it; the adapter consumes it.
type SandboxSpec struct {
	RunID, ProjectID string
	Image            string            // "" = built-in, built on demand (D-7)
	Clone            CloneSpec         // url, ref, branch to create, git identity
	SetupScript      string            // run in the workspace after clone; non-zero exit fails Prepare
	Env              map[string]string // secrets, OAuth token, git identity
	Files            map[string][]byte // .claude/settings.json, .mcp.json, prompt file
	Network          NetworkPolicy     // {Mode: none|allowlist|open, Allow: []string}
	Labels           map[string]string
	Limits           ResourceLimits // cpu, memory, pids, wall clock
}

// CloneSpec tells the sandbox how to populate /workspace. A zero URL means no clone (the
// workspace starts empty). Ref is what to check out — a branch name or a commit SHA; empty
// means the remote's default branch. Branch, when set, is the new branch created from Ref
// (S19's `{agent}/{ticket-key}-{slug}`).
type CloneSpec struct {
	URL    string // authenticated clone URL (ForgeProvider.CloneURL); never logged
	Ref    string
	Branch string
	// UserName and UserEmail configure the repository-local git identity, so commits made in
	// the workspace attribute to the agent.
	UserName  string
	UserEmail string
}

// StepState is the lifecycle of one provisioning step: pending|running|ok|failed.
type StepState string

const (
	StepPending StepState = "pending"
	StepRunning StepState = "running"
	StepOK      StepState = "ok"
	StepFailed  StepState = "failed"
)

// ProvisionSink receives provisioning progress. Steps become checklist rows (architecture
// §10.3); Log lines are the verbose stream underneath the currently running step. Both must be
// cheap and non-blocking for the adapter to call.
type ProvisionSink interface {
	Step(name string, state StepState, detail string) // pending|running|ok|failed
	Log(line string)
}

// Instance is one live sandbox: an isolated workspace that commands can be executed in.
type Instance interface {
	// Ref identifies this instance durably — persist it, and hand it back to
	// Sandbox.Reattach after a crash.
	Ref() InstanceRef
	// Exec starts argv inside the instance and returns its attached streams. It does not wait;
	// call Streams.Wait for the exit code.
	Exec(ctx context.Context, argv []string, opts ExecOpts) (Streams, error)
	// ReadFile returns one file's bytes from inside the instance. Relative paths are resolved
	// against the workspace root.
	ReadFile(ctx context.Context, path string) ([]byte, error)
	// Destroy removes the instance and its workspace volume. Idempotent: destroying an
	// already-destroyed instance is nil.
	Destroy(ctx context.Context) error
}

// ExecOpts modifies one Exec. The zero value runs in the workspace root with the instance's
// own environment and no TTY.
type ExecOpts struct {
	// WorkDir is the working directory; "" means the workspace root.
	WorkDir string
	// Env is added to the instance's environment for this exec only.
	Env map[string]string
	// TTY allocates a pseudo-terminal, merging stderr into stdout.
	TTY bool
}

// Streams is the attached I/O of one Exec.
type Streams struct {
	Stdin  io.WriteCloser
	Stdout io.Reader
	Stderr io.Reader
	Wait   func() (exitCode int, err error)
}

// InstanceRef is the durable identity of an Instance — small, serializable, and enough for
// Reattach to find the instance again after a crash. The Docker adapter stamps InstanceID on
// the container as the `lexicode.instance` label and finds it back by that label (§10.6).
type InstanceRef struct {
	SandboxID  string `json:"sandbox_id"`  // which sandbox adapter, e.g. "docker"
	InstanceID string `json:"instance_id"` // unique per Prepare
	RunID      string `json:"run_id"`
	// LogOffset is how many bytes of the instance's log stream the caller has already
	// consumed (persisted as runs.log_offset). Reattach hands it back so the stream resumes
	// where it stopped instead of re-emitting from the start.
	LogOffset int64 `json:"log_offset"`
}

// NetworkMode is the egress stance of one run: none|allowlist|open (D-10).
type NetworkMode string

const (
	// NetworkNone allows only the Anthropic API host and the repo's git host (via the S18
	// egress proxy).
	NetworkNone NetworkMode = "none"
	// NetworkAllowlist is NetworkNone plus the repo's network_allowlist domains.
	NetworkAllowlist NetworkMode = "allowlist"
	// NetworkOpen is the default bridge network — no proxy, no restrictions.
	NetworkOpen NetworkMode = "open"
)

// NetworkPolicy is the resolved egress policy for one run.
type NetworkPolicy struct {
	Mode  NetworkMode
	Allow []string // extra allowed domains; only meaningful under NetworkAllowlist
}

// ResourceLimits bounds one instance. Zero fields mean "no limit". WallClock is enforced by
// the scheduler (a run-duration timeout), not by the container runtime; it rides along here so
// the whole budget is one value.
type ResourceLimits struct {
	CPUs        float64 // fractional cores, e.g. 2.0
	MemoryBytes int64
	Pids        int64
	WallClock   time.Duration
}

// ImageMissingToolsError is returned by Prepare when a custom image_ref lacks a tool an agent
// run cannot work without (`git`, `claude`). It matches ErrImageMissingTools under errors.Is,
// and its message names the image and every missing tool so the project settings UI can say
// exactly what to fix.
type ImageMissingToolsError struct {
	Image   string
	Missing []string
}

func (e *ImageMissingToolsError) Error() string {
	return fmt.Sprintf("image_missing_tools: image %q lacks required tools: %s",
		e.Image, strings.Join(e.Missing, ", "))
}

// ErrImageMissingTools matches any ImageMissingToolsError under errors.Is.
var ErrImageMissingTools = errors.New("image_missing_tools")

func (e *ImageMissingToolsError) Is(target error) bool { return target == ErrImageMissingTools }

// ErrInstanceGone is returned by Reattach when the referenced instance no longer exists (or is
// no longer running). The scheduler reacts by terminating the run as failed with reason
// "orchestrator restarted" (§10.6).
var ErrInstanceGone = errors.New("sandbox instance no longer exists")
