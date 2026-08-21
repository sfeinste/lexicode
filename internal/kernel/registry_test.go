package kernel_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestDuplicatePortIDAbortsBootNamingBothModules is the S02 acceptance criterion: two modules
// claiming the same port ID must abort boot, and the error must name the port kind, the ID and
// both modules — otherwise the reader has to go looking for the other claimant.
func TestDuplicatePortIDAbortsBootNamingBothModules(t *testing.T) {
	log := &recorder{}
	k := newKernel(t, kernel.Options{})
	registerForge := func(k *kernel.Kernel) error { return k.RegisterForge(stubPort{id: "github"}) }

	if err := k.RegisterModule(
		&noop{name: "github", log: log, register: registerForge},
		&noop{name: "github-enterprise", log: log, register: registerForge},
	); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}

	err := k.Init()
	if err == nil {
		t.Fatal("Init returned nil, want boot to abort on the duplicate ID")
	}
	if !errors.Is(err, kernel.ErrDuplicateID) {
		t.Errorf("error is not ErrDuplicateID: %v", err)
	}
	for _, want := range []string{"github-enterprise", `"github"`, "forge"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
	var dup *kernel.DuplicateIDError
	if !errors.As(err, &dup) {
		t.Fatalf("error = %v, want a *kernel.DuplicateIDError", err)
	}
	if dup.ExistingModule != "github" || dup.Module != "github-enterprise" {
		t.Errorf("DuplicateIDError names %q and %q, want both module names",
			dup.ExistingModule, dup.Module)
	}
	t.Logf("boot aborted with: %v", err)
}

// TestDuplicatePortIDAbortsEvenIfTheModuleIgnoresIt: the registration error is recorded on the
// kernel too, so a module that drops the error on the floor still cannot boot the process into a
// state where one of two adapters silently wins.
func TestDuplicatePortIDAbortsEvenIfTheModuleIgnoresIt(t *testing.T) {
	log := &recorder{}
	k := newKernel(t, kernel.Options{})
	registerAction := func(k *kernel.Kernel) error { return k.RegisterAction(stubPort{id: "run_agent"}) }

	if err := k.RegisterModule(
		&noop{name: "actions", log: log, register: registerAction},
		&noop{name: "careless", log: log, register: registerAction, swallowRegisterErr: true},
	); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}

	err := k.Init()
	if err == nil {
		t.Fatal("Init returned nil, want boot to abort even though the module ignored the error")
	}
	if !strings.Contains(err.Error(), "careless") || !strings.Contains(err.Error(), "actions") {
		t.Errorf("error = %q, want both module names", err)
	}
}

// TestSameIDInDifferentPortsIsFine: duplicate detection is per port kind. "github" as a forge and
// "github" as an event source is the V1 module set, not a clash.
func TestSameIDInDifferentPortsIsFine(t *testing.T) {
	k := newKernel(t, kernel.Options{})
	if err := k.RegisterModule(&noop{name: "github", log: &recorder{}, register: func(k *kernel.Kernel) error {
		if err := k.RegisterForge(stubPort{id: "github"}); err != nil {
			return err
		}
		return k.RegisterEventSource(stubPort{id: "github"})
	}}); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}
	if err := k.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
}

// TestLookupsReturnWhatWasRegistered walks all eight ports through register → look up, which is
// what freezes the registration API for the stories that fill the ports in.
func TestLookupsReturnWhatWasRegistered(t *testing.T) {
	k := newKernel(t, kernel.Options{})
	if err := k.RegisterModule(&noop{name: "everything", log: &recorder{}, register: func(k *kernel.Kernel) error {
		return errors.Join(
			k.RegisterEventSource(stubPort{id: "github.poll"}),
			k.RegisterForge(stubPort{id: "github"}),
			k.RegisterSandbox(stubPort{id: "docker"}),
			k.RegisterRuntime(stubPort{id: "claude-code"}),
			k.RegisterAction(stubPort{id: "run_agent"}),
			k.RegisterContextProvider(stubPort{id: "wiki"}),
			k.RegisterNotifier(stubPort{id: "inapp"}),
			k.RegisterCredentialSource(stubPort{id: "oauth-token"}),
		)
	}}); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}
	if err := k.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	lookups := map[string]func() (string, error){
		"event source": func() (string, error) { return idOf(k.EventSource("github.poll")) },
		"forge":        func() (string, error) { return idOf(k.Forge("github")) },
		"sandbox":      func() (string, error) { return idOf(k.Sandbox("docker")) },
		"runtime":      func() (string, error) { return idOf(k.Runtime("claude-code")) },
		"action":       func() (string, error) { return idOf(k.Action("run_agent")) },
		"context":      func() (string, error) { return idOf(k.ContextProvider("wiki")) },
		"notifier":     func() (string, error) { return idOf(k.Notifier("inapp")) },
		"credential":   func() (string, error) { return idOf(k.CredentialSource("oauth-token")) },
	}
	want := map[string]string{
		"event source": "github.poll", "forge": "github", "sandbox": "docker",
		"runtime": "claude-code", "action": "run_agent", "context": "wiki",
		"notifier": "inapp", "credential": "oauth-token",
	}
	for kind, lookup := range lookups {
		id, err := lookup()
		if err != nil {
			t.Errorf("%s lookup: %v", kind, err)
			continue
		}
		if id != want[kind] {
			t.Errorf("%s lookup returned %q, want %q", kind, id, want[kind])
		}
	}
}

func idOf[T ports.EventSource](v T, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return v.ID(), nil
}

// TestMissingIDIsATypedNotFoundError: a stored configuration referring to an adapter this build
// does not have must say so by name.
func TestMissingIDIsATypedNotFoundError(t *testing.T) {
	k := newKernel(t, kernel.Options{})
	if err := k.RegisterModule(&noop{name: "github", log: &recorder{}, register: func(k *kernel.Kernel) error {
		return k.RegisterForge(stubPort{id: "github"})
	}}); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}
	if err := k.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	_, err := k.Forge("gitlab")
	if err == nil {
		t.Fatal("Forge returned nil error for an unregistered ID")
	}
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("error is not ErrNotFound: %v", err)
	}
	var nf *kernel.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("error = %v, want a *kernel.NotFoundError", err)
	}
	if nf.ID != "gitlab" || !strings.Contains(err.Error(), "gitlab") {
		t.Errorf("error = %q, want it to name the missing ID", err)
	}
	t.Logf("lookup failed with: %v", err)
}

// TestSetAccessorsAreDeterministicallyOrdered: the THEN editor and the context resolver iterate
// these, so the order may not depend on the order of lines in cmd/lexicode.
func TestSetAccessorsAreDeterministicallyOrdered(t *testing.T) {
	build := func(ids ...string) []string {
		k := newKernel(t, kernel.Options{})
		if err := k.RegisterModule(&noop{name: "m", log: &recorder{}, register: func(k *kernel.Kernel) error {
			var errs []error
			for _, id := range ids {
				errs = append(errs, k.RegisterAction(stubPort{id: id}), k.RegisterContextProvider(stubPort{id: id}))
			}
			return errors.Join(errs...)
		}}); err != nil {
			t.Fatalf("RegisterModule: %v", err)
		}
		if err := k.Init(); err != nil {
			t.Fatalf("Init: %v", err)
		}
		var got []string
		for _, a := range k.Actions() {
			got = append(got, a.ID())
		}
		for _, p := range k.ContextProviders() {
			got = append(got, p.ID())
		}
		return got
	}

	forward := build("notify", "create_ticket", "run_agent")
	backward := build("run_agent", "create_ticket", "notify")
	if strings.Join(forward, ",") != strings.Join(backward, ",") {
		t.Errorf("order depends on registration order: %v vs %v", forward, backward)
	}
	want := "create_ticket,notify,run_agent,create_ticket,notify,run_agent"
	if got := strings.Join(forward, ","); got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
}

// TestEventSourcesReturnsEveryRegisteredSource: architecture §7 starts one goroutine per source,
// so the plural accessor is part of the contract, not a convenience.
func TestEventSourcesReturnsEveryRegisteredSource(t *testing.T) {
	k := newKernel(t, kernel.Options{})
	if err := k.RegisterModule(&noop{name: "sources", log: &recorder{}, register: func(k *kernel.Kernel) error {
		return errors.Join(
			k.RegisterEventSource(stubPort{id: "schedule.cron"}),
			k.RegisterEventSource(stubPort{id: "github.poll"}),
		)
	}}); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}
	if err := k.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	var got []string
	for _, s := range k.EventSources() {
		got = append(got, s.ID())
	}
	if strings.Join(got, ",") != "github.poll,schedule.cron" {
		t.Errorf("EventSources() = %v", got)
	}
}

// TestRegisteringOutsideInitIsAttributedHonestly: ports are registered from Module.Init. A stray
// registration elsewhere must not produce an error message that blames an innocent module.
func TestRegisteringOutsideInitIsAttributedHonestly(t *testing.T) {
	k := newKernel(t, kernel.Options{})
	if err := k.RegisterForge(stubPort{id: "github"}); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	err := k.RegisterForge(stubPort{id: "github"})
	if err == nil {
		t.Fatal("second registration returned nil")
	}
	if !strings.Contains(err.Error(), "outside Module.Init") {
		t.Errorf("error = %q, want it to say the registration did not come from a module", err)
	}
}

// TestDuplicateModuleNameIsRejected: names are what the settings UI and every log line key on.
func TestDuplicateModuleNameIsRejected(t *testing.T) {
	k := newKernel(t, kernel.Options{})
	err := k.RegisterModule(&noop{name: "github", log: &recorder{}}, &noop{name: "github", log: &recorder{}})
	if err == nil || !strings.Contains(err.Error(), "github") {
		t.Fatalf("RegisterModule error = %v, want it to reject the duplicate name", err)
	}
}

// TestKernelExposesTheSubsystemsThatExist: the accessors S02 ships. Store, Bus, Scheduler,
// Secrets, Audit and SSE arrive in S03–S06 and are deliberately absent until then.
func TestKernelExposesTheSubsystemsThatExist(t *testing.T) {
	logger := discardLogger()
	k := kernel.New(kernel.Options{Logger: logger})
	if k.Logger() != logger {
		t.Error("Logger() did not return the logger it was built with")
	}
	if k.Mux() == nil {
		t.Error("Mux() is nil; New must build one when none is supplied")
	}
	k.Start(context.Background()) // no modules: must be a no-op, not a panic
	k.Stop(context.Background())
}
