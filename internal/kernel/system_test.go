package kernel_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/httpx"
)

type modulesBody struct {
	Modules []kernel.ModuleStatus `json:"modules"`
}

func getModules(t *testing.T, base string) (int, string, modulesBody) {
	t.Helper()
	resp, err := http.Get(base + "/api/v1/system/modules") //nolint:noctx // test client
	if err != nil {
		t.Fatalf("GET /api/v1/system/modules: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var body modulesBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return resp.StatusCode, strings.TrimSpace(string(raw)), body
}

// TestStartFailureLeavesTheServerRunningAndReportsDegraded is the second S02 acceptance
// criterion, and the reason the degraded state exists at all: a broken GitHub token must not stop
// the dashboard from loading (architecture §3).
func TestStartFailureLeavesTheServerRunningAndReportsDegraded(t *testing.T) {
	log := &recorder{}
	mux := httpx.NewMux(httpx.Options{Logger: discardLogger()})
	k := newKernel(t, kernel.Options{Mux: mux})
	if err := k.RegisterModule(
		&noop{name: "docker", log: log},
		&noop{name: "github", log: log, startErr: errors.New("bad credentials: the stored token was revoked")},
		&noop{name: "notify", log: log},
	); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}
	if err := k.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	k.Start(context.Background())

	// The modules registered after the failing one still started: Start does not abort.
	events := strings.Join(log.events(), ",")
	if !strings.Contains(events, "start:notify") {
		t.Errorf("events = %v, want boot to have continued past the failing module", log.events())
	}

	status, raw, body := getModules(t, srv.URL)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the server must still be serving", status)
	}
	t.Logf("GET /api/v1/system/modules → %s", raw)

	byName := map[string]kernel.ModuleStatus{}
	for _, m := range body.Modules {
		byName[m.Name] = m
	}
	if len(body.Modules) != 3 {
		t.Fatalf("modules = %v, want three", body.Modules)
	}
	if got := byName["github"]; got.State != kernel.StateDegraded {
		t.Errorf("github state = %q, want %q", got.State, kernel.StateDegraded)
	}
	if got := byName["github"]; !strings.Contains(got.Reason, "token was revoked") {
		t.Errorf("github reason = %q, want the Start error recorded verbatim", got.Reason)
	}
	for _, name := range []string{"docker", "notify"} {
		if got := byName[name]; got.State != kernel.StateReady {
			t.Errorf("%s state = %q, want %q", name, got.State, kernel.StateReady)
		}
		if got := byName[name]; got.Reason != "" {
			t.Errorf("%s reason = %q, want it omitted for a healthy module", name, got.Reason)
		}
	}
}

// TestModulesEndpointIsRegistrationOrdered keeps the list stable for a UI that renders it.
func TestModulesEndpointIsRegistrationOrdered(t *testing.T) {
	mux := httpx.NewMux(httpx.Options{Logger: discardLogger()})
	k := newKernel(t, kernel.Options{Mux: mux})
	if err := k.RegisterModule(
		&noop{name: "github", log: &recorder{}},
		&noop{name: "docker", log: &recorder{}},
		&noop{name: "actions", log: &recorder{}},
	); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, _, body := getModules(t, srv.URL)
	var names []string
	for _, m := range body.Modules {
		names = append(names, m.Name)
	}
	if strings.Join(names, ",") != "github,docker,actions" {
		t.Errorf("names = %v, want registration order", names)
	}
}

// TestModulesEndpointWithNoModulesIsAnEmptyArray: a build with no modules registered answers with
// an empty list, never null.
func TestModulesEndpointWithNoModulesIsAnEmptyArray(t *testing.T) {
	mux := httpx.NewMux(httpx.Options{Logger: discardLogger()})
	newKernel(t, kernel.Options{Mux: mux})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	status, raw, _ := getModules(t, srv.URL)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if raw != `{"modules":[]}` {
		t.Errorf("body = %s, want {\"modules\":[]}", raw)
	}
}
