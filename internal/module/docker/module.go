// Package docker is the Docker sandbox module (story S17): the one V1 implementation of
// ports.Sandbox. It builds the embedded agent base image on demand (D-7), provisions one
// container per run — labelled, resource-limited, read-only rootfs with a writable workspace
// volume — executes the workspace clone and setup script inside the container, and sweeps
// orphaned containers on boot and hourly (§10.6).
package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// moduleName is both the kernel module name and the Sandbox port ID.
const moduleName = "docker"

// Options configures New. The zero value is what cmd/lexicode passes when no docker_host is
// configured: the client follows the environment, and the run-state lookup is wired from the
// kernel store in Init.
type Options struct {
	// Host overrides the Docker endpoint (config docker_host). Empty means "use the
	// environment": DOCKER_HOST if set, the platform default socket otherwise.
	Host string
	// Logger overrides the kernel logger; tests capture output through it.
	Logger *slog.Logger
	// RunState is the orphan sweeper's only reach into the rest of the system: what state is
	// this run in, if it exists at all? Nil means "wire from the kernel store in Init".
	RunState RunStateFn
	// SweepInterval is how often the orphan sweeper runs after boot. Zero means hourly.
	SweepInterval time.Duration
	// ExtraBinds is a test seam: host bind mounts ("src:dst:ro") added to every container this
	// module creates. The docker-tagged tests use it to offer a fixture git repository to the
	// in-container clone. Production wiring leaves it empty.
	ExtraBinds []string
	// ProxyPort is the host port the S18 egress proxy binds (config proxy_port). Zero
	// disables the proxy and the relay: none/allowlist containers then have zero egress.
	ProxyPort int
	// ProxyAppend is the proxy's decision-log sink. Nil means "wire from the kernel store in
	// Init"; tests inject a recorder.
	ProxyAppend ActivityAppender
}

// Module implements the kernel module lifecycle and registers the one Sandbox this binary
// ships.
type Module struct {
	opts    Options
	sandbox *Sandbox
	proxy   *Proxy

	cancelSweep context.CancelFunc
	sweepDone   chan struct{}
}

// New builds the module. The Docker client is not constructed here — a malformed host string
// should abort boot with a named module, which is Init's job.
func New(opts Options) *Module {
	return &Module{opts: opts}
}

// Name implements kernel.Module.
func (m *Module) Name() string { return moduleName }

// Sandbox returns the concrete adapter for the wiring site and for callers that need the one
// capability the frozen port does not carry (Instance.Logs, via type assertion on the
// instance). Nil until Init has run, unless the module was built by tests via NewSandbox.
func (m *Module) Sandbox() *Sandbox { return m.sandbox }

// Proxy returns the S18 egress proxy for the wiring site: the scheduler (S22) registers runs
// on it, the env assembly (S19) asks it for ProxyEnv. Nil until Init has run, or when
// Options.ProxyPort is zero.
func (m *Module) Proxy() *Proxy { return m.proxy }

// Init implements kernel.Module: build the client, wire the run-state lookup from the kernel
// store, and register the sandbox port. No I/O happens here; an unreachable daemon is Start's
// business (degraded, not fatal).
func (m *Module) Init(k *kernel.Kernel) error {
	logger := m.opts.Logger
	if logger == nil {
		logger = k.Logger()
	}
	sb, err := NewSandbox(m.opts.Host, logger)
	if err != nil {
		return err
	}
	sb.extraBinds = m.opts.ExtraBinds
	sb.runState = m.opts.RunState
	if sb.runState == nil && k.Store() != nil {
		st := k.Store()
		sb.runState = func(ctx context.Context, runID string) (domain.RunState, bool, error) {
			run, err := st.Runs().ByID(ctx, runID)
			if errors.Is(err, store.ErrNotFound) {
				return "", false, nil
			}
			if err != nil {
				return "", false, err
			}
			return run.State, true, nil
		}
	}
	m.sandbox = sb

	if m.opts.ProxyPort > 0 {
		appendFn := m.opts.ProxyAppend
		if appendFn == nil && k.Store() != nil {
			st := k.Store()
			appendFn = func(ctx context.Context, a *domain.Activity) error {
				return st.Activities().AppendNext(ctx, a)
			}
		}
		m.proxy = NewProxy(ProxyOptions{Logger: logger, Append: appendFn})
		sb.proxyPort = m.opts.ProxyPort
	}

	return k.RegisterSandbox(sb)
}

// Start implements kernel.Module: preflight the daemon, then start the orphan sweeper (one
// sweep now, then hourly). An unreachable daemon returns an error, which the kernel records as
// this module's degraded state — the server keeps working; runs cannot start until Docker is
// back, and Available() keeps answering the current truth per call.
func (m *Module) Start(ctx context.Context) error {
	// The proxy starts before the daemon preflight: it is host-side and must serve whenever
	// runs exist, including runs that outlived a Docker hiccup and come back with the daemon.
	if m.proxy != nil {
		if err := m.proxy.Start(fmt.Sprintf("0.0.0.0:%d", m.opts.ProxyPort)); err != nil {
			return err
		}
	}

	if err := m.sandbox.Available(ctx); err != nil {
		return err
	}

	interval := m.opts.SweepInterval
	if interval <= 0 {
		interval = time.Hour
	}
	// The sweeper outlives the boot context; Stop cancels it.
	sweepCtx, cancel := context.WithCancel(context.Background())
	m.cancelSweep = cancel
	m.sweepDone = make(chan struct{})
	go m.sweepLoop(sweepCtx, interval)
	return nil
}

// Stop implements kernel.Module.
func (m *Module) Stop(ctx context.Context) error {
	var errProxy error
	if m.proxy != nil {
		errProxy = m.proxy.Stop(ctx)
	}
	if m.cancelSweep == nil {
		return errProxy
	}
	m.cancelSweep()
	select {
	case <-m.sweepDone:
		return errProxy
	case <-ctx.Done():
		return fmt.Errorf("docker: sweeper did not stop: %w", ctx.Err())
	}
}

func (m *Module) sweepLoop(ctx context.Context, interval time.Duration) {
	defer close(m.sweepDone)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		removed, err := m.sandbox.Sweep(ctx)
		if err != nil && ctx.Err() == nil {
			m.sandbox.logger.Warn("docker: orphan sweep failed", slog.String("error", err.Error()))
		} else if removed > 0 {
			m.sandbox.logger.Info("docker: removed orphaned containers", slog.Int("removed", removed))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// compile-time checks: the module fits the kernel lifecycle and the sandbox fits the port.
var (
	_ kernel.Module = (*Module)(nil)
	_ ports.Sandbox = (*Sandbox)(nil)
)
