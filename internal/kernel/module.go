package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Module is an adapter that implements one or more ports. All modules are compiled in and
// registered in one place; "pluggable" means a new module is one file plus one line in
// cmd/lexicode, not dynamic loading (architecture §3).
//
// This interface is frozen (contracts §1).
type Module interface {
	Name() string
	Init(k *Kernel) error            // register ports, routes, subscriptions. No I/O.
	Start(ctx context.Context) error // begin background work. Must return promptly.
	Stop(ctx context.Context) error  // drain. Called in reverse registration order.
}

// ModuleState is the state a module is in after boot. There are exactly two: a module either did
// what it was asked or it did not, and the second case has a reason a human can read.
type ModuleState string

const (
	// StateReady means Init and Start both succeeded.
	StateReady ModuleState = "ready"
	// StateDegraded means Start failed. The process keeps running: a broken GitHub token must not
	// prevent the dashboard from loading (architecture §3).
	StateDegraded ModuleState = "degraded"
)

// ModuleStatus is one module's state, as reported by GET /api/v1/system/modules.
type ModuleStatus struct {
	Name   string      `json:"name"`
	State  ModuleState `json:"state"`
	Reason string      `json:"reason,omitempty"`
}

// StopTimeout is the default bound on the whole shutdown sequence, not on one module
// (architecture §3). Options.StopTimeout overrides it.
const StopTimeout = 30 * time.Second

type moduleEntry struct {
	module Module
	state  ModuleState
	reason string
}

// RegisterModule adds modules in the order they will be initialised and started, and the reverse
// of which they will be stopped. Call it before Init. Module names must be unique: they are what
// the settings UI and the degradation reason refer to.
func (k *Kernel) RegisterModule(mods ...Module) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, m := range mods {
		if m == nil {
			return errors.New("cannot register a nil module")
		}
		name := m.Name()
		if name == "" {
			return fmt.Errorf("module %T has an empty Name(); every module needs a stable name", m)
		}
		for _, existing := range k.modules {
			if existing.module.Name() == name {
				return fmt.Errorf("module %q is registered twice; module names must be unique", name)
			}
		}
		k.modules = append(k.modules, &moduleEntry{module: m, state: StateReady})
	}
	return nil
}

// Init initialises every module in registration order. A module whose Init returns an error, or
// which registers a port ID another module already claimed, aborts boot; the returned error names
// the module. Nothing has started at this point, so there is nothing to unwind.
func (k *Kernel) Init() error {
	for _, e := range k.moduleEntries() {
		name := e.module.Name()

		k.setInitializing(name)
		err := e.module.Init(k)
		k.setInitializing("")

		if err == nil {
			err = k.takeRegisterErr()
		}
		if err != nil {
			return fmt.Errorf("module %q failed to initialise: %w", name, err)
		}
		k.logger.Debug("module initialised", slog.String("module", name))
	}
	k.logger.Info("modules initialised", slog.Int("modules", len(k.moduleEntries())))
	return nil
}

// Start starts every module in registration order. A module whose Start returns an error is
// marked degraded with the error as its reason and boot continues — that is the whole point of
// the degraded state, and it is why Start reports nothing to its caller. Inspect Modules, or
// GET /api/v1/system/modules, for what happened.
func (k *Kernel) Start(ctx context.Context) {
	for _, e := range k.moduleEntries() {
		name := e.module.Name()
		if err := e.module.Start(ctx); err != nil {
			k.degrade(e, err.Error())
			k.logger.Error("module degraded",
				slog.String("module", name), slog.String("error", err.Error()))
			continue
		}
		k.logger.Debug("module started", slog.String("module", name))
	}
}

// Stop stops every module in reverse registration order, within StopTimeout overall. Each module
// gets an equal share of whatever time is left, so one module that will not drain cannot consume
// the deadline the others need. A Stop error is logged, not fatal: the process is on its way out
// and there is nothing left to fail.
//
// Pass a context that is not already cancelled — during a signal-driven shutdown that means a
// fresh context, not the one the signal cancelled.
func (k *Kernel) Stop(ctx context.Context) {
	entries := k.moduleEntries()
	if len(entries) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, k.stopTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()

	for i := len(entries) - 1; i >= 0; i-- {
		name := entries[i].module.Name()
		remaining := time.Until(deadline)
		if remaining <= 0 {
			k.logger.Warn("shutdown deadline exceeded; module was not stopped",
				slog.String("module", name), slog.Duration("deadline", k.stopTimeout))
			continue
		}

		// i+1 modules are still to be stopped, this one included.
		share := remaining / time.Duration(i+1)
		k.stopOne(ctx, entries[i].module, share)
	}
}

func (k *Kernel) stopOne(parent context.Context, m Module, share time.Duration) {
	ctx, cancel := context.WithTimeout(parent, share)
	defer cancel()

	// Stop runs on its own goroutine so that a module which ignores its context cannot block the
	// modules below it. If it never returns, the goroutine leaks for the few milliseconds the
	// process has left.
	done := make(chan error, 1)
	go func() { done <- m.Stop(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			k.logger.Error("module did not stop cleanly",
				slog.String("module", m.Name()), slog.String("error", err.Error()))
			return
		}
		k.logger.Debug("module stopped", slog.String("module", m.Name()))
	case <-ctx.Done():
		k.logger.Warn("module did not stop within its share of the shutdown deadline",
			slog.String("module", m.Name()), slog.Duration("share", share))
	}
}

// Modules reports every module's state, in registration order.
func (k *Kernel) Modules() []ModuleStatus {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]ModuleStatus, 0, len(k.modules))
	for _, e := range k.modules {
		out = append(out, ModuleStatus{Name: e.module.Name(), State: e.state, Reason: e.reason})
	}
	return out
}

// SetModuleState lets a module report a health transition after boot, so that a condition
// discovered at runtime — a forge rate limit exhausted, a Docker daemon that went away — shows
// up in GET /api/v1/system/modules just like a Start failure does (story S14). It is additive
// to the frozen contracts-§1 surface: Start-time degradation still works exactly as before.
// The name must be the module's own Name(); reporting for an unregistered module is an error.
func (k *Kernel) SetModuleState(name string, state ModuleState, reason string) error {
	if state != StateReady && state != StateDegraded {
		return fmt.Errorf("module %q reported unknown state %q", name, state)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, e := range k.modules {
		if e.module.Name() != name {
			continue
		}
		if e.state == state && e.reason == reason {
			return nil
		}
		e.state = state
		e.reason = reason
		k.logger.Info("module state changed",
			slog.String("module", name), slog.String("state", string(state)),
			slog.String("reason", reason))
		return nil
	}
	return fmt.Errorf("no module named %q is registered", name)
}

func (k *Kernel) moduleEntries() []*moduleEntry {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]*moduleEntry(nil), k.modules...)
}

func (k *Kernel) degrade(e *moduleEntry, reason string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	e.state = StateDegraded
	e.reason = reason
}

func (k *Kernel) setInitializing(name string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.initializing = name
}

// takeRegisterErr returns and clears the sticky registration error.
func (k *Kernel) takeRegisterErr() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	err := k.registerErr
	k.registerErr = nil
	return err
}
