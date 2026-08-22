package kernel_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// stubPort stands in for any of the eight ports. Every port interface currently declares only
// ID(), so one stub satisfies all of them; when a story fills in a port's real method set it also
// replaces the stub it needs with a real fake.
// stubPort satisfies every port interface for registry tests. The embedded ForgeProvider
// supplies the transcribed method set (S14) by promotion — calling any of those methods panics,
// which is fine: these tests exercise registration and lookup, never the port behaviour. As
// later stories transcribe the other ports, embed them here the same way.
type stubPort struct {
	ports.EventSource
	ports.ForgeProvider
	ports.Sandbox
	ports.CredentialSource
	ports.AgentRuntime
	ports.ContextProvider
	id string
}

func (s stubPort) ID() string { return s.id }

var (
	_ ports.EventSource      = stubPort{}
	_ ports.ForgeProvider    = stubPort{}
	_ ports.Sandbox          = stubPort{}
	_ ports.AgentRuntime     = stubPort{}
	_ ports.TriggerAction    = stubPort{}
	_ ports.ContextProvider  = stubPort{}
	_ ports.Notifier         = stubPort{}
	_ ports.CredentialSource = stubPort{}
)

// recorder is the shared lifecycle log the noop modules write to, so that ordering assertions
// read as a single sequence rather than as per-module counters.
type recorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, s)
}

func (r *recorder) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

// noop is the demo module: it does nothing useful, and it can be told to fail at any phase. It is
// the fixture the whole lifecycle is tested through.
type noop struct {
	name string
	log  *recorder

	// register runs inside Init, before initErr is considered.
	register func(k *kernel.Kernel) error
	// swallowRegisterErr makes the module ignore what Register* returned. Boot must still abort:
	// a module does not get to opt out of duplicate-ID detection.
	swallowRegisterErr bool

	initErr   error
	startErr  error
	stopErr   error
	stopDelay time.Duration
}

func (m *noop) Name() string { return m.name }

func (m *noop) Init(k *kernel.Kernel) error {
	m.log.add("init:" + m.name)
	if m.register != nil {
		if err := m.register(k); err != nil && !m.swallowRegisterErr {
			return err
		}
	}
	return m.initErr
}

func (m *noop) Start(context.Context) error {
	m.log.add("start:" + m.name)
	return m.startErr
}

func (m *noop) Stop(ctx context.Context) error {
	m.log.add("stop:" + m.name)
	if m.stopDelay > 0 {
		select {
		case <-time.After(m.stopDelay):
		case <-ctx.Done():
			// Deliberately ignore the cancellation: a module that overruns its share must not be
			// able to take the deadline away from the modules below it.
			<-time.After(m.stopDelay)
		}
	}
	return m.stopErr
}

func newKernel(t *testing.T, opts kernel.Options) *kernel.Kernel {
	t.Helper()
	if opts.Logger == nil {
		opts.Logger = discardLogger()
	}
	return kernel.New(opts)
}

// TestLifecycleRunsEveryPhaseInOrder is the noop demo module exercising the full lifecycle:
// Init all in registration order, Start all in registration order, Stop all in reverse.
func TestLifecycleRunsEveryPhaseInOrder(t *testing.T) {
	log := &recorder{}
	k := newKernel(t, kernel.Options{})
	if err := k.RegisterModule(
		&noop{name: "alpha", log: log},
		&noop{name: "beta", log: log},
		&noop{name: "gamma", log: log},
	); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}

	if err := k.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	k.Start(context.Background())
	k.Stop(context.Background())

	want := []string{
		"init:alpha", "init:beta", "init:gamma",
		"start:alpha", "start:beta", "start:gamma",
		"stop:gamma", "stop:beta", "stop:alpha",
	}
	got := log.events()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("lifecycle order\n got: %v\nwant: %v", got, want)
	}
}

// TestStopRunsInReverseRegistrationOrder pins the reverse-order rule on its own, because it is
// the one ordering guarantee a module may depend on for its drain to be correct.
func TestStopRunsInReverseRegistrationOrder(t *testing.T) {
	log := &recorder{}
	k := newKernel(t, kernel.Options{})
	for _, name := range []string{"store-ish", "bus-ish", "poller-ish"} {
		if err := k.RegisterModule(&noop{name: name, log: log}); err != nil {
			t.Fatalf("RegisterModule: %v", err)
		}
	}
	if err := k.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	k.Stop(context.Background())

	want := []string{"init:store-ish", "init:bus-ish", "init:poller-ish",
		"stop:poller-ish", "stop:bus-ish", "stop:store-ish"}
	if got := log.events(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("stop order\n got: %v\nwant: %v", got, want)
	}
}

// TestInitErrorAbortsBootNamingTheModule covers architecture §3: Init failing aborts boot, and
// the error says which module failed.
func TestInitErrorAbortsBootNamingTheModule(t *testing.T) {
	log := &recorder{}
	k := newKernel(t, kernel.Options{})
	boom := errors.New("no database file")
	if err := k.RegisterModule(
		&noop{name: "first", log: log},
		&noop{name: "broken-module", log: log, initErr: boom},
		&noop{name: "third", log: log},
	); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}

	err := k.Init()
	if err == nil {
		t.Fatal("Init returned nil, want an error that aborts boot")
	}
	if !strings.Contains(err.Error(), "broken-module") {
		t.Errorf("error = %q, want it to name the failing module", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("error = %q, want it to wrap the module's own error", err)
	}
	// Boot aborted: the module after the failure was never initialised.
	for _, e := range log.events() {
		if e == "init:third" {
			t.Error("Init continued past the failing module")
		}
	}
}

// TestStopErrorIsNotFatal: a module that fails to drain is logged, not escalated, and the modules
// below it still stop.
func TestStopErrorIsNotFatal(t *testing.T) {
	log := &recorder{}
	k := newKernel(t, kernel.Options{})
	if err := k.RegisterModule(
		&noop{name: "under", log: log},
		&noop{name: "over", log: log, stopErr: errors.New("connection reset")},
	); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}
	if err := k.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	k.Stop(context.Background())

	want := []string{"init:under", "init:over", "stop:over", "stop:under"}
	if got := log.events(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("stop order\n got: %v\nwant: %v", got, want)
	}
}

// TestSlowStopDoesNotStarveTheOthers: the first module to be stopped hangs well past the whole
// deadline; the remaining two must still get their Stop called, because each module gets an equal
// share of the time that is left rather than "whatever the module before it did not use".
func TestSlowStopDoesNotStarveTheOthers(t *testing.T) {
	log := &recorder{}
	k := newKernel(t, kernel.Options{StopTimeout: 600 * time.Millisecond})
	if err := k.RegisterModule(
		&noop{name: "quick-a", log: log},
		&noop{name: "quick-b", log: log},
		&noop{name: "hangs", log: log, stopDelay: 10 * time.Second},
	); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}
	if err := k.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	start := time.Now()
	k.Stop(context.Background())
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("Stop took %s; the deadline was 600ms and must bound the whole sequence", elapsed)
	}
	got := strings.Join(log.events(), ",")
	for _, want := range []string{"stop:hangs", "stop:quick-b", "stop:quick-a"} {
		if !strings.Contains(got, want) {
			t.Errorf("events = %v, want %q — a hung module must not starve the rest", log.events(), want)
		}
	}
}
