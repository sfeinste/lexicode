// Package kernel is the part of Lexicode that knows nothing about GitHub, Docker, Claude or
// React: store, bus, registry, scheduler, guard, identity and the HTTP surface (architecture §2).
//
// The dependency rule (architecture §2.1) is the reason this package exists at all: it imports
// nothing from internal/module, internal/service or internal/api, ever. Wiring happens exactly
// once, in cmd/lexicode. importgraph_test.go enforces this and is the architecture's only real
// defence — a compile error is not available for a rule about who imports whom.
//
// The kernel is assembled once at boot:
//
//	k := kernel.New(kernel.Options{Logger: logger, Mux: mux})
//	k.RegisterModule(github.New(), docker.New())
//	if err := k.Init(); err != nil { return err }   // Init all, in registration order
//	k.Start(ctx)                                    // Start all; a failure degrades, never aborts
//	… serve …
//	k.Stop(context.Background())                    // reverse order, 30s overall deadline
package kernel

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Kernel owns the subsystems every module and service shares. Subsystems are added one story at a
// time; each one is a field here plus an accessor, and nothing else in the shape changes. The
// accessors that contracts §1 lists but that have no subsystem yet — Scheduler (S05),
// Secrets and Audit (S07), SSE (S06) — are deliberately absent rather than
// stubbed, so that no caller can be written against a stub that later changes meaning.
type Kernel struct {
	logger      *slog.Logger
	mux         *http.ServeMux
	store       *store.Store
	bus         *bus.Bus
	auth        *auth.Service
	stopTimeout time.Duration

	eventSources      *registry[ports.EventSource]
	forges            *registry[ports.ForgeProvider]
	sandboxes         *registry[ports.Sandbox]
	runtimes          *registry[ports.AgentRuntime]
	actions           *registry[ports.TriggerAction]
	contextProviders  *registry[ports.ContextProvider]
	notifiers         *registry[ports.Notifier]
	credentialSources *registry[ports.CredentialSource]

	mu sync.Mutex
	// modules is the registration order; Stop walks it backwards.
	modules []*moduleEntry
	// initializing names the module whose Init is on the stack, so that a port can be attributed
	// to the module that registered it without every adapter having to repeat its own name.
	initializing string
	// registerErr is the first registration failure seen during Init. It is sticky so that a
	// module which swallows the error returned by a Register* call still aborts boot: the
	// duplicate-ID rule is not something a module gets to opt out of.
	registerErr error
}

// Options configures New. The zero value is usable: it logs to slog.Default and builds its own
// mux, which is what tests want.
type Options struct {
	// Logger is the process logger. Nil means slog.Default().
	Logger *slog.Logger
	// Mux is the HTTP mux modules and services register routes on. Nil means a fresh one.
	Mux *http.ServeMux
	// StopTimeout bounds the whole shutdown sequence. Zero means StopTimeout, which is what the
	// architecture specifies; tests set it lower so that they can observe the deadline.
	StopTimeout time.Duration
	// Store is the open, migrated database. cmd/lexicode opens and migrates it before building
	// the kernel — wiring stays in cmd (architecture §2.1). Nil is tolerated only for tests
	// that exercise the kernel without a database; modules may assume it is set.
	Store *store.Store
	// Bus is the persist-then-dispatch event bus (D-13). cmd/lexicode constructs it over the
	// same store and starts it after Init, once every module's subscriptions exist. Nil is
	// tolerated only for tests that exercise the kernel without one; modules may assume it is
	// set.
	Bus *bus.Bus
	// Auth is the identity service (S05). The kernel uses it to guard the routes it owns
	// itself — today, GET /api/v1/system/modules behind RequireAuth. Nil is tolerated only for
	// tests that exercise the kernel without a database; cmd/lexicode always sets it.
	Auth *auth.Service
}

// New builds a kernel and registers the routes the kernel itself owns.
func New(opts Options) *Kernel {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	mux := opts.Mux
	if mux == nil {
		mux = http.NewServeMux()
	}

	timeout := opts.StopTimeout
	if timeout <= 0 {
		timeout = StopTimeout
	}

	k := &Kernel{
		logger:            logger,
		mux:               mux,
		store:             opts.Store,
		bus:               opts.Bus,
		auth:              opts.Auth,
		stopTimeout:       timeout,
		eventSources:      newRegistry[ports.EventSource]("event source"),
		forges:            newRegistry[ports.ForgeProvider]("forge"),
		sandboxes:         newRegistry[ports.Sandbox]("sandbox"),
		runtimes:          newRegistry[ports.AgentRuntime]("agent runtime"),
		actions:           newRegistry[ports.TriggerAction]("trigger action"),
		contextProviders:  newRegistry[ports.ContextProvider]("context provider"),
		notifiers:         newRegistry[ports.Notifier]("notifier"),
		credentialSources: newRegistry[ports.CredentialSource]("credential source"),
	}
	k.registerSystemRoutes()
	return k
}

// Logger is the process logger. Modules are expected to derive their own with
// Logger().With("module", name).
func (k *Kernel) Logger() *slog.Logger { return k.logger }

// Store is the open, migrated database (contracts §1). It is the one shared persistence handle:
// repositories, transactions and migrations all hang off it.
func (k *Kernel) Store() *store.Store { return k.store }

// Bus is the persist-then-dispatch event bus (contracts §1, D-13): sources publish through it,
// modules and services subscribe to it during Init. cmd/lexicode starts it after Init and stops
// it after the modules, so subscriptions exist before recovery and outlive module drains.
func (k *Kernel) Bus() *bus.Bus { return k.bus }

// Mux is the HTTP mux modules and services register routes on.
//
// Contracts §1 types this as *httpx.Mux. Story S06 introduces kernel/httpx with the middleware
// chain, problem+json errors and the SSE hub, and changes the return type at that point; until
// then the stdlib mux that cmd/lexicode already builds is the whole HTTP surface, and adding a
// wrapper now would be a second thing to migrate.
func (k *Kernel) Mux() *http.ServeMux { return k.mux }

// ---------------------------------------------------------------- registration -----
//
// Every Register* method is called from Module.Init and returns an error the module should return
// as-is. The error is also recorded on the kernel, so a module that ignores it still aborts boot.

// RegisterEventSource registers an event source. Duplicate IDs abort boot.
func (k *Kernel) RegisterEventSource(s ports.EventSource) error {
	return k.record(k.eventSources.register(k.registrar(), s))
}

// RegisterForge registers a forge provider. Duplicate IDs abort boot.
func (k *Kernel) RegisterForge(p ports.ForgeProvider) error {
	return k.record(k.forges.register(k.registrar(), p))
}

// RegisterSandbox registers a sandbox. Duplicate IDs abort boot.
func (k *Kernel) RegisterSandbox(s ports.Sandbox) error {
	return k.record(k.sandboxes.register(k.registrar(), s))
}

// RegisterRuntime registers an agent runtime. Duplicate IDs abort boot.
func (k *Kernel) RegisterRuntime(r ports.AgentRuntime) error {
	return k.record(k.runtimes.register(k.registrar(), r))
}

// RegisterAction registers a trigger action. Duplicate IDs abort boot.
func (k *Kernel) RegisterAction(a ports.TriggerAction) error {
	return k.record(k.actions.register(k.registrar(), a))
}

// RegisterContextProvider registers a context provider. Duplicate IDs abort boot.
func (k *Kernel) RegisterContextProvider(p ports.ContextProvider) error {
	return k.record(k.contextProviders.register(k.registrar(), p))
}

// RegisterNotifier registers a notifier. Duplicate IDs abort boot.
func (k *Kernel) RegisterNotifier(n ports.Notifier) error {
	return k.record(k.notifiers.register(k.registrar(), n))
}

// RegisterCredentialSource registers a credential source. Duplicate IDs abort boot.
func (k *Kernel) RegisterCredentialSource(c ports.CredentialSource) error {
	return k.record(k.credentialSources.register(k.registrar(), c))
}

// registrar names the module currently being initialised, for attribution in error messages.
func (k *Kernel) registrar() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.initializing == "" {
		return "(registered outside Module.Init)"
	}
	return k.initializing
}

// record remembers the first registration error so that boot aborts even if the module that
// caused it returns nil from Init.
func (k *Kernel) record(err error) error {
	if err == nil {
		return nil
	}
	k.mu.Lock()
	if k.registerErr == nil {
		k.registerErr = err
	}
	k.mu.Unlock()
	return err
}

// -------------------------------------------------------------------- lookups -----
//
// Single-instance lookups return a *NotFoundError naming the missing ID (errors.Is(err,
// ErrNotFound)). The plural accessors exist only for the ports the architecture uses as a set:
// every event source is started (§7), every action is offered in the THEN editor and every
// context provider contributes to a prompt (§8, contracts §2.6). They are ordered by ID.

// EventSource returns the event source with this ID.
func (k *Kernel) EventSource(id string) (ports.EventSource, error) { return k.eventSources.get(id) }

// EventSources returns every registered event source, ordered by ID.
func (k *Kernel) EventSources() []ports.EventSource { return k.eventSources.all() }

// Forge returns the forge provider with this ID.
func (k *Kernel) Forge(id string) (ports.ForgeProvider, error) { return k.forges.get(id) }

// Sandbox returns the sandbox with this ID.
func (k *Kernel) Sandbox(id string) (ports.Sandbox, error) { return k.sandboxes.get(id) }

// Runtime returns the agent runtime with this ID.
func (k *Kernel) Runtime(id string) (ports.AgentRuntime, error) { return k.runtimes.get(id) }

// Action returns the trigger action with this ID.
func (k *Kernel) Action(id string) (ports.TriggerAction, error) { return k.actions.get(id) }

// Actions returns every registered trigger action, ordered by ID.
func (k *Kernel) Actions() []ports.TriggerAction { return k.actions.all() }

// ContextProvider returns the context provider with this ID.
func (k *Kernel) ContextProvider(id string) (ports.ContextProvider, error) {
	return k.contextProviders.get(id)
}

// ContextProviders returns every registered context provider, ordered by ID. Prompt order is by
// ContextProvider.Priority and is applied by the resolver in story S34, not here.
func (k *Kernel) ContextProviders() []ports.ContextProvider { return k.contextProviders.all() }

// Notifier returns the notifier with this ID.
func (k *Kernel) Notifier(id string) (ports.Notifier, error) { return k.notifiers.get(id) }

// CredentialSource returns the credential source with this ID.
func (k *Kernel) CredentialSource(id string) (ports.CredentialSource, error) {
	return k.credentialSources.get(id)
}
