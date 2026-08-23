package docker

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"golang.org/x/sync/singleflight"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// Container labels (architecture §10.6). labelInstance carries the InstanceRef.InstanceID that
// Reattach finds the container back by — one value per container. labelOwner carries the
// identity of the Lexicode instance that created it, which is what the orphan sweeper keys on;
// see owner.go for why the two cannot be the same label.
const (
	labelRun      = "lexicode.run"
	labelInstance = "lexicode.instance"
	labelProject  = "lexicode.project"
	labelOwner    = "lexicode.owner"
)

// internalNetwork is the Docker network `none` and `allowlist` containers join: internal, so
// there is no default route out. S18's egress proxy is the only way out of it; until S18 lands,
// these containers simply have no egress (see prepareNetwork).
const internalNetwork = "lexicode-internal"

// workspaceDir is the run's git worktree: an anonymous volume (the rootfs is read-only)
// removed together with the container.
const workspaceDir = "/workspace"

// scaffoldingPaths are the orchestrator's own directories inside the workspace: `.lexicode/`
// holds the run's MCP config (and therefore the run's live MCP token) and prompt, `.claude/`
// holds the generated settings file. They are provisioning inputs, not the user's source, and
// they must never reach the user's repository — the §10.5 failure-artifact path runs
// `git add -A && git commit && git push`, which would otherwise publish the run token into the
// repo and its history.
//
// The clone step writes them into `.git/info/exclude` rather than `.gitignore`: exclude is
// local to the checkout, so it never shows up in a diff, a commit or a pull request the way an
// edited `.gitignore` would. It holds for every path out of the container — the artifact push,
// an agent's own `git add -A`, a `git status` the agent reads — not just for the one caller
// that remembers.
//
// The strings mirror internal/service/runs' settingsPath/mcpConfigPath prefixes. The packages
// must not import each other (module → service is a forbidden edge), so, as elsewhere in this
// codebase, the string IS the protocol.
var scaffoldingPaths = []string{".lexicode/", ".claude/"}

// RunStateFn resolves a run's current state for the orphan sweeper: found=false means no such
// run. The module wires this from the kernel store in Init; tests inject a fake. The sandbox
// holds this one narrow function, never the store.
type RunStateFn func(ctx context.Context, runID string) (state domain.RunState, found bool, err error)

// Sandbox is the Docker implementation of ports.Sandbox (story S17).
type Sandbox struct {
	cli        *client.Client
	logger     *slog.Logger
	runState   RunStateFn
	extraBinds []string // test seam: host bind mounts added to every container
	// owner is this Lexicode instance's identity, stamped on every container it creates and
	// the only containers Sweep will remove (owner.go). The module sets it from
	// Options.DataDir; NewSandbox leaves the per-process fallback in place.
	owner string
	// proxyPort is the host port the S18 egress proxy listens on; none/allowlist containers
	// get an egress relay targeting it (relay.go). Zero disables the relay — containers on
	// the internal network then simply have no egress (restrictive, never permissive).
	proxyPort int

	// build deduplicates concurrent builds of the same image tag: concurrent runs needing the
	// same image share one build. Followers wait for the winner and see "ready" — build
	// progress streams only to the sink of the run that started the build.
	build singleflight.Group
}

// NewSandbox builds a Sandbox against the given Docker endpoint. host "" means "use the
// environment": DOCKER_HOST if set, the platform default socket otherwise.
func NewSandbox(host string, logger *slog.Logger) (*Sandbox, error) {
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	} else {
		opts = append(opts, client.FromEnv)
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker: building client: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Sandbox{cli: cli, logger: logger, owner: processOwner}, nil
}

// ID implements ports.Sandbox.
func (s *Sandbox) ID() string { return "docker" }

// Available implements ports.Sandbox: can the daemon be reached right now? The module surfaces
// the error as its degraded state; the rest of the server keeps working.
func (s *Sandbox) Available(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := s.cli.Ping(ctx); err != nil {
		return fmt.Errorf("docker daemon unreachable: %w", err)
	}
	return nil
}

// Prepare implements ports.Sandbox. It walks the §10.3 checklist — image, container, clone,
// branch, setup script — reporting each step through sink. On any failure after the container
// exists, the container is destroyed before returning: a failed Prepare leaks nothing.
func (s *Sandbox) Prepare(ctx context.Context, spec ports.SandboxSpec, sink ports.ProvisionSink) (ports.Instance, error) {
	imageRef, err := s.ensureImage(ctx, spec.Image, sink)
	if err != nil {
		return nil, err
	}

	inst, err := s.createAndStart(ctx, spec, imageRef, sink)
	if err != nil {
		return nil, err
	}

	fail := func(err error) (ports.Instance, error) {
		// Prepare's own ctx may already be cancelled; cleanup gets a fresh deadline.
		dctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if derr := inst.Destroy(dctx); derr != nil {
			s.logger.Warn("docker: could not clean up container after failed prepare",
				slog.String("instance", inst.Ref().InstanceID),
				slog.String("error", derr.Error()))
		}
		return nil, err
	}

	// Custom image_ref validation: an agent run cannot work without git and claude on PATH.
	if spec.Image != "" {
		if err := s.validateTools(ctx, inst, spec.Image); err != nil {
			return fail(err)
		}
	}

	if spec.Clone.URL != "" {
		if err := s.clone(ctx, inst, spec.Clone, sink); err != nil {
			return fail(err)
		}
	}

	if spec.SetupScript != "" {
		if err := s.runSetupScript(ctx, inst, spec.SetupScript, sink); err != nil {
			return fail(err)
		}
	}

	return inst, nil
}

// Reattach implements ports.Sandbox: find the container by its lexicode.instance label and
// resume (§10.6). The ref's LogOffset rides along untouched — Instance.Logs skips that many
// bytes of the container log stream, which is how "resume from where the last process stopped"
// is carried across a crash.
func (s *Sandbox) Reattach(ctx context.Context, ref ports.InstanceRef) (ports.Instance, error) {
	args := filters.NewArgs(filters.Arg("label", labelInstance+"="+ref.InstanceID))
	list, err := s.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return nil, fmt.Errorf("docker: listing containers for reattach: %w", err)
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("reattach %s: %w", ref.InstanceID, ports.ErrInstanceGone)
	}
	c := list[0]
	if c.State != container.StateRunning {
		return nil, fmt.Errorf("reattach %s: container %s is %s: %w",
			ref.InstanceID, shortID(c.ID), c.State, ports.ErrInstanceGone)
	}
	return &Instance{cli: s.cli, containerID: c.ID, ref: ref, logger: s.logger}, nil
}

// createAndStart is the "container created" step: create with labels, limits, read-only rootfs
// and the network the policy calls for, materialize spec.Files into the workspace, then start.
func (s *Sandbox) createAndStart(ctx context.Context, spec ports.SandboxSpec, imageRef string, sink ports.ProvisionSink) (*Instance, error) {
	const step = "container"
	sink.Step(step, ports.StepRunning, "")

	fail := func(err error) (*Instance, error) {
		sink.Step(step, ports.StepFailed, err.Error())
		return nil, err
	}

	networkMode, err := s.prepareNetwork(ctx, spec.Network)
	if err != nil {
		return fail(err)
	}

	instanceID := domain.NewID()
	labels := s.containerLabels(spec, instanceID)

	// HOME defaults to /tmp: the rootfs is read-only, and /tmp is the writable scratch space,
	// so tools that write dotfiles (git's global config, claude's state) have somewhere to go.
	// The spec's env wins on conflict — later entries override earlier ones in Docker.
	env := append([]string{"HOME=/tmp"}, sortedEnv(spec.Env)...)

	cfg := &container.Config{
		Image:  imageRef,
		Cmd:    []string{"sleep", "infinity"},
		Env:    env,
		Labels: labels,
		// An anonymous volume: initialized from the image's /workspace (owned by the agent
		// user in the built-in image) and removed with the container (Destroy passes
		// RemoveVolumes).
		Volumes:    map[string]struct{}{workspaceDir: {}},
		WorkingDir: workspaceDir,
	}
	hostCfg := &container.HostConfig{
		Init:           boolPtr(true), // reap zombies; agent processes fork a lot
		ReadonlyRootfs: true,
		Tmpfs:          map[string]string{"/tmp": "rw,exec,nosuid,size=1g"},
		NetworkMode:    networkMode,
		// host-gateway makes host.docker.internal resolve on native Linux too (D-12: open
		// runs reach the MCP endpoint at host.docker.internal:<proxy port>). Harmless on the
		// internal network, where the route out does not exist anyway.
		ExtraHosts: []string{"host.docker.internal:host-gateway"},
		Binds:      s.extraBinds,
		Resources: container.Resources{
			NanoCPUs:  int64(spec.Limits.CPUs * 1e9),
			Memory:    spec.Limits.MemoryBytes,
			PidsLimit: pidsPtr(spec.Limits.Pids),
		},
	}

	created, err := s.cli.ContainerCreate(ctx, cfg, hostCfg, &network.NetworkingConfig{}, nil,
		"lexicode-"+strings.ToLower(instanceID))
	if err != nil {
		return fail(fmt.Errorf("docker: creating container: %w", err))
	}

	ref := ports.InstanceRef{
		SandboxID:  s.ID(),
		InstanceID: instanceID,
		RunID:      spec.RunID,
	}
	inst := &Instance{cli: s.cli, containerID: created.ID, ref: ref, logger: s.logger}

	destroyAndFail := func(err error) (*Instance, error) {
		dctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if derr := inst.Destroy(dctx); derr != nil {
			s.logger.Warn("docker: could not clean up container after failed create",
				slog.String("container", shortID(created.ID)),
				slog.String("error", derr.Error()))
		}
		return fail(err)
	}

	// Files are materialized before start (tar copy into the workspace volume), so nothing the
	// agent could run has a window where its settings are absent.
	if len(spec.Files) > 0 {
		tarball, err := filesTar(spec.Files)
		if err != nil {
			return destroyAndFail(fmt.Errorf("docker: materializing files: %w", err))
		}
		err = s.cli.CopyToContainer(ctx, created.ID, workspaceDir, tarball,
			container.CopyToContainerOptions{})
		if err != nil {
			return destroyAndFail(fmt.Errorf("docker: copying files into container: %w", err))
		}
	}

	if err := s.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return destroyAndFail(fmt.Errorf("docker: starting container: %w", err))
	}

	sink.Step(step, ports.StepOK, "created "+shortID(created.ID))
	return inst, nil
}

// containerLabels is the label set every run container carries: the run and project it belongs
// to, its own instance id, and the Lexicode instance that owns it. The four are reserved — a
// spec label may not overwrite one, or a caller could make its container look like another
// instance's (or like no run at all, which the sweeper reads as an orphan).
func (s *Sandbox) containerLabels(spec ports.SandboxSpec, instanceID string) map[string]string {
	labels := map[string]string{
		labelRun:      spec.RunID,
		labelInstance: instanceID,
		labelProject:  spec.ProjectID,
		labelOwner:    s.owner,
	}
	for k, v := range spec.Labels {
		if _, reserved := labels[k]; !reserved {
			labels[k] = v
		}
	}
	return labels
}

// prepareNetwork maps the resolved policy onto a Docker network. `open` (and an unset mode) is
// the default bridge. `none` and `allowlist` join lexicode-internal, an internal network with
// no default route: the S18 egress proxy — reached through the lexicode-egress relay — is the
// only way out of it. When no proxy port is configured (proxyPort 0: direct-constructed test
// sandboxes), these containers simply have no egress — restrictive, never permissive.
func (s *Sandbox) prepareNetwork(ctx context.Context, policy ports.NetworkPolicy) (container.NetworkMode, error) {
	switch policy.Mode {
	case ports.NetworkOpen, "":
		return "bridge", nil
	case ports.NetworkNone, ports.NetworkAllowlist:
		if err := s.ensureInternalNetwork(ctx); err != nil {
			return "", err
		}
		if s.proxyPort > 0 {
			if err := s.ensureRelay(ctx, s.proxyPort); err != nil {
				return "", err
			}
		}
		return container.NetworkMode(internalNetwork), nil
	default:
		return "", fmt.Errorf("docker: unknown network mode %q", policy.Mode)
	}
}

// ensureInternalNetwork creates lexicode-internal if it does not exist yet. Creation can race
// with another Prepare; the loser re-checks instead of failing.
func (s *Sandbox) ensureInternalNetwork(ctx context.Context) error {
	exists := func() (bool, error) {
		args := filters.NewArgs(filters.Arg("name", internalNetwork))
		nets, err := s.cli.NetworkList(ctx, network.ListOptions{Filters: args})
		if err != nil {
			return false, fmt.Errorf("docker: listing networks: %w", err)
		}
		for _, n := range nets {
			if n.Name == internalNetwork {
				return true, nil
			}
		}
		return false, nil
	}

	ok, err := exists()
	if err != nil || ok {
		return err
	}
	_, err = s.cli.NetworkCreate(ctx, internalNetwork, network.CreateOptions{
		Driver:   "bridge",
		Internal: true,
		Labels:   map[string]string{"lexicode.network": "internal"},
	})
	if err != nil {
		if ok, lerr := exists(); lerr == nil && ok {
			return nil // lost the creation race; the network is there
		}
		return fmt.Errorf("docker: creating internal network: %w", err)
	}
	return nil
}

// validateTools checks a custom image_ref for the tools an agent run cannot work without.
// Failure is the named error ports.ErrImageMissingTools (D-7).
func (s *Sandbox) validateTools(ctx context.Context, inst *Instance, imageRef string) error {
	const probe = `missing=""
for t in git claude; do command -v "$t" >/dev/null 2>&1 || missing="$missing $t"; done
printf '%s' "$missing"`
	code, out, err := inst.runCmd(ctx, []string{"/bin/sh", "-c", probe}, nil, nil)
	if err != nil {
		return fmt.Errorf("docker: probing image %q for required tools: %w", imageRef, err)
	}
	if code != 0 {
		return fmt.Errorf("docker: probing image %q for required tools: exit %d: %s",
			imageRef, code, strings.TrimSpace(out))
	}
	missing := strings.Fields(out)
	if len(missing) > 0 {
		return &ports.ImageMissingToolsError{Image: imageRef, Missing: missing}
	}
	return nil
}

// clone is the "repo cloned" (and "branch created") step. The clone runs inside the container
// via exec'd git, so the credential-bearing URL never touches the host. It is init+fetch+
// checkout rather than `git clone` because spec.Files were already materialized into the
// workspace — clone refuses a non-empty directory; fetch does not. A shallow fetch is tried
// first and falls back to a full fetch for refs the transport cannot shallow-fetch (e.g. an
// arbitrary SHA).
func (s *Sandbox) clone(ctx context.Context, inst *Instance, c ports.CloneSpec, sink ports.ProvisionSink) error {
	const step = "clone"
	sink.Step(step, ports.StepRunning, "")

	// $LEXICODE_EXCLUDES is appended to .git/info/exclude immediately after `git init`, so
	// there is no window in which a `git add -A` could pick the orchestrator's scaffolding up
	// (see scaffoldingPaths).
	const script = `set -e
git config --global --add safe.directory '*'
git init -q .
mkdir -p .git/info
printf '%s\n' "$LEXICODE_EXCLUDES" >> .git/info/exclude
git remote add origin "$LEXICODE_CLONE_URL"
ref="$LEXICODE_CLONE_REF"
[ -n "$ref" ] || ref=HEAD
git fetch --depth 1 origin "$ref" 2>&1 || git fetch origin "$ref" 2>&1
git checkout -q -f FETCH_HEAD`
	env := map[string]string{
		"LEXICODE_CLONE_URL": c.URL,
		"LEXICODE_CLONE_REF": c.Ref,
		"LEXICODE_EXCLUDES":  strings.Join(scaffoldingPaths, "\n"),
	}
	code, out, err := inst.runCmd(ctx, []string{"/bin/sh", "-c", script}, env, sink.Log)
	if err != nil {
		sink.Step(step, ports.StepFailed, err.Error())
		return fmt.Errorf("docker: cloning repository: %w", err)
	}
	if code != 0 {
		detail := "exit " + fmt.Sprint(code)
		sink.Step(step, ports.StepFailed, detail)
		return fmt.Errorf("docker: cloning repository failed (%s):\n%s", detail, out)
	}

	sha := "unknown"
	if code, out, err := inst.runCmd(ctx, []string{"git", "rev-parse", "--short", "HEAD"}, nil, nil); err == nil && code == 0 {
		sha = strings.TrimSpace(out)
	}
	refLabel := c.Ref
	if refLabel == "" {
		refLabel = "HEAD"
	}
	sink.Step(step, ports.StepOK, refLabel+"@"+sha)

	// Repository-local git identity, so commits attribute to the agent.
	for _, kv := range [][2]string{{"user.name", c.UserName}, {"user.email", c.UserEmail}} {
		if kv[1] == "" {
			continue
		}
		code, out, err := inst.runCmd(ctx, []string{"git", "config", kv[0], kv[1]}, nil, nil)
		if err != nil || code != 0 {
			return fmt.Errorf("docker: setting git %s: exit %d, err %v: %s", kv[0], code, err, out)
		}
	}

	if c.Branch != "" {
		const branchStep = "branch"
		sink.Step(branchStep, ports.StepRunning, "")
		code, out, err := inst.runCmd(ctx, []string{"git", "checkout", "-q", "-b", c.Branch}, nil, sink.Log)
		if err != nil || code != 0 {
			detail := fmt.Sprintf("exit %d, err %v", code, err)
			sink.Step(branchStep, ports.StepFailed, detail)
			return fmt.Errorf("docker: creating branch %q (%s):\n%s", c.Branch, detail, out)
		}
		sink.Step(branchStep, ports.StepOK, c.Branch)
	}
	return nil
}

// runSetupScript is the "setup script" step: /bin/sh -c in the workspace, output streamed into
// the checklist, non-zero exit failing Prepare with the script's output in the failure — the
// user must be able to read why (§10.3, S19's acceptance).
func (s *Sandbox) runSetupScript(ctx context.Context, inst *Instance, script string, sink ports.ProvisionSink) error {
	const step = "setup script"
	sink.Step(step, ports.StepRunning, "")
	start := time.Now()

	code, out, err := inst.runCmd(ctx, []string{"/bin/sh", "-c", script}, nil, sink.Log)
	if err != nil {
		sink.Step(step, ports.StepFailed, err.Error())
		return fmt.Errorf("docker: running setup script: %w", err)
	}
	if code != 0 {
		detail := fmt.Sprintf("exit %d", code)
		sink.Step(step, ports.StepFailed, detail)
		return fmt.Errorf("setup script failed (%s):\n%s", detail, out)
	}
	sink.Step(step, ports.StepOK, time.Since(start).Round(time.Second).String())
	return nil
}

func sortedEnv(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func boolPtr(b bool) *bool { return &b }

// pidsPtr maps "0 = no limit" onto Docker's "nil = unset".
func pidsPtr(n int64) *int64 {
	if n <= 0 {
		return nil
	}
	return &n
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

var _ ports.Sandbox = (*Sandbox)(nil)
