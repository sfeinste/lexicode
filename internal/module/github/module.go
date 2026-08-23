package github

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// moduleName is both the kernel module name and the ForgeProvider port ID.
const moduleName = "github"

// PermissionLookup resolves an agent's permissions at write time. The forge consults the
// source of truth on every write rather than trusting a permissions blob a caller hands it —
// the grant check is enforcement (brief D7), so it lives as close to the wire as possible.
type PermissionLookup func(ctx context.Context, agentID string) (domain.AgentPermissions, error)

// OutputRecorder appends one run_outputs row (contracts §2.2 write step 3).
type OutputRecorder func(ctx context.Context, out domain.RunOutput) error

// AuditRecorder writes one audit entry. Its shape is audit.Writer.Write, so wiring it is a
// method value; tests substitute a capture.
type AuditRecorder func(ctx context.Context, action string, target audit.Target, before, after any) error

// Options configures New. The zero value is what cmd/lexicode passes: every nil dependency is
// wired from the kernel in Init (permissions from the store, outputs from the store, audit from
// the kernel's writer), so the adapter still only ever holds these three narrow functions —
// never the whole store. Non-nil values win, which is how the tests inject fakes and how the
// fixture servers are reached.
type Options struct {
	// BaseURL overrides the GitHub API base URL (fixture servers now, GitHub Enterprise
	// later). Empty means api.github.com.
	BaseURL string
	// Transport is the base RoundTripper underneath the module's retry/rate-limit transport.
	// Nil means http.DefaultTransport.
	Transport http.RoundTripper
	// Logger overrides the kernel logger (tests capture output through it). It is wrapped in
	// the token-redacting handler either way.
	Logger *slog.Logger

	// Permissions, RecordOutput and RecordAudit are the forge's only reach into the rest of
	// the system. Nil means "wire from the kernel in Init".
	Permissions  PermissionLookup
	RecordOutput OutputRecorder
	RecordAudit  AuditRecorder

	// MaxAttempts, RetryBase and MaxRateLimitSleep tune the transport; zero means the
	// package defaults (3 tries, 500ms base backoff, 5s max rate-limit sleep).
	MaxAttempts       int
	RetryBase         time.Duration
	MaxRateLimitSleep time.Duration
}

// Module is the github module: it implements the kernel Module lifecycle and registers two
// ports — the ForgeProvider this binary ships (story S14) and the github.poll EventSource
// (story S25). The poller reuses the forge's transport, so one rate-limit policy governs both.
type Module struct {
	forge  *Forge
	poller *Poller
	emit   ports.Emit // wired from the kernel bus in Init; Start hands it to the poller
}

// New builds the module. See Options for what may be left zero.
func New(opts Options) *Module {
	redactor := &Redactor{}
	f := &Forge{
		baseURL:  opts.BaseURL,
		redactor: redactor,
		perms:    opts.Permissions,
		record:   opts.RecordOutput,
		auditRec: opts.RecordAudit,
		health:   func(kernel.ModuleState, string) {},
		now:      domain.Now,
		transport: newTransport(opts.Transport, opts.MaxAttempts, opts.RetryBase,
			opts.MaxRateLimitSleep),
	}
	if opts.Logger != nil {
		f.setLogger(opts.Logger)
	} else {
		f.setLogger(slog.Default())
	}
	// Both the transport's rate-limit policy (S14) and the poller's denied-resource policy
	// (LEXI-9) degrade this module, and they can be outstanding at the same time. They go
	// through one composer so neither clears the other's reason on recovery.
	f.mh = newModuleHealth(func(state kernel.ModuleState, reason string) { f.health(state, reason) })
	f.transport.onExhausted = func(reset time.Time) {
		f.logger.Warn("github: rate limit exhausted; module degraded",
			slog.Time("reset", reset))
		f.mh.degrade(rateLimitKey, degradedReason(reset))
	}
	f.transport.onHealthy = func() {
		if f.mh.recover(rateLimitKey) {
			f.logger.Info("github: rate limit recovered")
		}
	}
	m := &Module{forge: f}
	m.poller = newPoller(f)
	if opts.Logger != nil {
		m.poller.logger = opts.Logger.With("module", moduleName)
	}
	return m
}

// Name implements kernel.Module.
func (m *Module) Name() string { return moduleName }

// Poller returns the concrete event source, for tests and the one wiring site.
func (m *Module) Poller() *Poller { return m.poller }

// Forge returns the concrete adapter. cmd/lexicode (the wiring site) uses it to satisfy the
// bootstrap service's DocLister seam with the adapter's extra ListDir method — a capability the
// frozen ForgeProvider port does not carry (see Forge.ListDir). No other caller should reach
// for the concrete type.
func (m *Module) Forge() *Forge { return m.forge }

// Init registers the forge port and wires the dependencies that were not injected: the
// permission lookup and output recorder read the kernel's store, the audit recorder is the
// kernel's writer, and health transitions go to kernel.SetModuleState. No I/O happens here.
func (m *Module) Init(k *kernel.Kernel) error {
	if m.forge.logger == nil || m.forge.loggerDefaulted {
		m.forge.setLogger(k.Logger())
	}
	if m.forge.perms == nil && k.Store() != nil {
		st := k.Store()
		m.forge.perms = func(ctx context.Context, agentID string) (domain.AgentPermissions, error) {
			a, err := st.Agents().ByID(ctx, agentID)
			if err != nil {
				return domain.AgentPermissions{}, err
			}
			return a.Permissions, nil
		}
	}
	if m.forge.record == nil && k.Store() != nil {
		repo := k.Store().RunOutputs()
		m.forge.record = func(ctx context.Context, out domain.RunOutput) error {
			return repo.Append(ctx, &out)
		}
	}
	if m.forge.auditRec == nil && k.Audit() != nil {
		m.forge.auditRec = k.Audit().Write
	}
	m.forge.health = func(state kernel.ModuleState, reason string) {
		if err := k.SetModuleState(moduleName, state, reason); err != nil {
			m.forge.logger.Error("github: could not report module state",
				slog.String("error", err.Error()))
		}
	}
	if err := k.RegisterForge(m.forge); err != nil {
		return err
	}

	// The poller (S25): store and credential wiring, the bus emit, and the repo lifecycle
	// subscriptions. Subscriptions are registered here in Init so the bus's Start (which the
	// wiring site calls after every module's Init) delivers boot recovery to them too.
	m.poller.logger = m.forge.logger
	if m.poller.store == nil {
		m.poller.store = k.Store()
	}
	if m.poller.creds == nil && k.Secrets() != nil {
		sec := k.Secrets()
		m.poller.creds = func(ctx context.Context, rp domain.Repo) (ports.Creds, error) {
			if rp.TokenSecretID == nil {
				return ports.Creds{}, errors.New("github.poll: the connected repo has no stored token; reconnect the repository")
			}
			token, err := sec.Get(ctx, *rp.TokenSecretID)
			if err != nil {
				return ports.Creds{}, err
			}
			return ports.Creds{Token: token}, nil
		}
	}
	if b := k.Bus(); b != nil {
		m.emit = func(ctx context.Context, e domain.Event) error {
			// Idempotent per contracts §2.1: a duplicate dedupe key means the event is
			// already persisted — the cursors' overlap window, not a failure.
			if err := b.Publish(ctx, e); err != nil && !errors.Is(err, bus.ErrDuplicate) {
				return err
			}
			return nil
		}
		if err := b.SubscribeKind("github.poll.repo-connected", "repo.connected",
			func(_ context.Context, e domain.Event) error {
				if e.ProjectID != nil {
					m.poller.EnsureWorker(*e.ProjectID)
				}
				return nil
			}); err != nil {
			return err
		}
		if err := b.SubscribeKind("github.poll.repo-disconnected", "repo.disconnected",
			func(ctx context.Context, e domain.Event) error {
				if e.ProjectID != nil {
					m.poller.RemoveWorker(ctx, *e.ProjectID)
				}
				return nil
			}); err != nil {
			return err
		}
	}
	return k.RegisterEventSource(m.poller)
}

// Start implements kernel.Module: it starts the poller's per-project workers (the EventSource
// port's Start, with the bus-publish emit wired in Init). Nothing else needs verifying at
// boot: the forge holds no credentials of its own, so it reports ready and stays that way
// until the rate-limit transport degrades it at runtime. Without a store or bus (forge-only
// tests) there is nothing to poll and Start is a no-op.
func (m *Module) Start(ctx context.Context) error {
	if m.poller.store == nil || m.emit == nil {
		return nil
	}
	return m.poller.Start(ctx, m.emit)
}

// Stop implements kernel.Module: drains the poller's workers.
func (m *Module) Stop(ctx context.Context) error { return m.poller.Stop(ctx) }

// compile-time checks: the module fits the kernel lifecycle and the forge fits the port.
var (
	_ kernel.Module       = (*Module)(nil)
	_ ports.ForgeProvider = (*Forge)(nil)
	_ ports.EventSource   = (*Poller)(nil)
)
