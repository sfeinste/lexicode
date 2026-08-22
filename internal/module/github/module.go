package github

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/audit"
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

// Module is the github module: it implements the kernel Module lifecycle and registers the one
// ForgeProvider this binary ships (story S14).
type Module struct {
	forge *Forge
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
	f.transport.onExhausted = func(reset time.Time) {
		f.logger.Warn("github: rate limit exhausted; module degraded",
			slog.Time("reset", reset))
		f.health(kernel.StateDegraded, degradedReason(reset))
	}
	f.transport.onHealthy = func() {
		f.logger.Info("github: rate limit recovered; module ready")
		f.health(kernel.StateReady, "")
	}
	return &Module{forge: f}
}

// Name implements kernel.Module.
func (m *Module) Name() string { return moduleName }

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
	return k.RegisterForge(m.forge)
}

// Start implements kernel.Module. There is nothing to verify at boot: no repository may be
// connected yet and this module holds no credentials of its own (they arrive per call as
// ports.Creds), so the module reports ready and stays that way until the rate-limit transport
// degrades it at runtime.
func (m *Module) Start(context.Context) error { return nil }

// Stop implements kernel.Module. The module runs no background work.
func (m *Module) Stop(context.Context) error { return nil }

// compile-time checks: the module fits the kernel lifecycle and the forge fits the port.
var (
	_ kernel.Module       = (*Module)(nil)
	_ ports.ForgeProvider = (*Forge)(nil)
)
