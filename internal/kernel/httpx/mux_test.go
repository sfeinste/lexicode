package httpx_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/spruce/lexicode/internal/kernel/httpx"
)

// logRecorder captures slog lines so tests can assert on the one-line-per-request contract.
type logRecorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logRecorder) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&syncWriter{l: l}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (l *logRecorder) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

type syncWriter struct{ l *logRecorder }

func (w *syncWriter) Write(p []byte) (int, error) {
	w.l.mu.Lock()
	defer w.l.mu.Unlock()
	return w.l.buf.Write(p)
}

func newServer(t *testing.T, m *httpx.Mux) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url) //nolint:noctx // test client
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, string(body)
}

func TestEveryRequestGetsAnIDAndALogLine(t *testing.T) {
	rec := &logRecorder{}
	m := httpx.NewMux(httpx.Options{Logger: rec.logger()})
	m.HandleFunc("GET /hello", func(w http.ResponseWriter, r *http.Request) {
		if httpx.RequestID(r.Context()) == "" {
			t.Error("handler saw no request id on the context")
		}
		w.WriteHeader(http.StatusTeapot)
	})
	srv := newServer(t, m)

	resp, _ := get(t, srv.URL+"/hello")
	id := resp.Header.Get("X-Request-ID")
	if id == "" {
		t.Fatal("no X-Request-ID response header")
	}

	line := rec.String()
	for _, want := range []string{
		"http request", "request_id=" + id, "method=GET", "path=/hello", "status=418", "duration=",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("log = %q, want it to contain %q", line, want)
		}
	}
}

// TestPanicBecomesA500ProblemAndTheServerLivesOn is S06 acceptance: panic in a handler → 500
// problem+json, request logged, server alive.
func TestPanicBecomesA500ProblemAndTheServerLivesOn(t *testing.T) {
	rec := &logRecorder{}
	m := httpx.NewMux(httpx.Options{Logger: rec.logger()})
	m.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) {
		panic("wired wrong")
	})
	m.HandleFunc("GET /fine", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	srv := newServer(t, m)

	resp, body := get(t, srv.URL+"/boom")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("content-type = %q, want application/problem+json", ct)
	}
	var p httpx.Problem
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("body %q is not a problem: %v", body, err)
	}
	if p.Type != httpx.TypeInternal || p.Status != 500 {
		t.Errorf("problem = %+v, want type internal status 500", p)
	}

	log := rec.String()
	if !strings.Contains(log, "panic in http handler") || !strings.Contains(log, "wired wrong") {
		t.Errorf("log = %q, want the panic logged with its value", log)
	}
	if !strings.Contains(log, "status=500") {
		t.Errorf("log = %q, want the request logged with status 500", log)
	}

	// The server lives on.
	resp2, body2 := get(t, srv.URL+"/fine")
	if resp2.StatusCode != http.StatusOK || body2 != "ok" {
		t.Errorf("after a panic, GET /fine = %d %q, want 200 ok", resp2.StatusCode, body2)
	}
}

// TestUsePrefixWrapsOnlyItsNamespace pins the mechanism serve.go uses for CSRF + setup gate:
// middleware on /api/ never touches the SPA routes.
func TestUsePrefixWrapsOnlyItsNamespace(t *testing.T) {
	m := httpx.NewMux(httpx.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	m.HandleFunc("GET /api/thing", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "api")
	})
	m.HandleFunc("GET /page", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "page")
	})
	tag := func(name string) httpx.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Add("X-Chain", name)
				next.ServeHTTP(w, r)
			})
		}
	}
	m.UsePrefix("/api/", tag("outer"), tag("inner"))
	srv := newServer(t, m)

	resp, _ := get(t, srv.URL+"/api/thing")
	if got := strings.Join(resp.Header.Values("X-Chain"), ","); got != "outer,inner" {
		t.Errorf("chain on /api/thing = %q, want outer,inner (first listed outermost)", got)
	}
	resp, _ = get(t, srv.URL+"/page")
	if got := resp.Header.Get("X-Chain"); got != "" {
		t.Errorf("chain on /page = %q, want none", got)
	}
}

// corsProbe checks CORS is off by default and opt-in per origin.
func TestCORSIsOffByDefault(t *testing.T) {
	m := httpx.NewMux(httpx.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	m.HandleFunc("GET /x", func(w http.ResponseWriter, _ *http.Request) {})
	srv := newServer(t, m)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/x", nil) //nolint:noctx // test client
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if h := resp.Header.Get("Access-Control-Allow-Origin"); h != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want none — CORS is off by default", h)
	}
}

type signupBody struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

func (b signupBody) Validate() []httpx.FieldError {
	var errs []httpx.FieldError
	if b.Email == "" {
		errs = append(errs, httpx.FieldError{Field: "email", Message: "Email is required."})
	}
	if b.Name == "" {
		errs = append(errs, httpx.FieldError{Field: "name", Message: "Name is required."})
	}
	return errs
}

func TestDecodeJSON(t *testing.T) {
	post := func(body string) (*httptest.ResponseRecorder, signupBody, bool) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
		v, ok := httpx.DecodeJSON[signupBody](w, r)
		return w, v, ok
	}

	// A valid body decodes and continues.
	if _, v, ok := post(`{"email":"a@b.c","name":"Ada"}`); !ok || v.Email != "a@b.c" {
		t.Errorf("valid body: ok=%v v=%+v", ok, v)
	}

	// Not JSON → 400 invalid_request, response already written.
	w, _, ok := post(`{nope`)
	if ok || w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"type":"invalid_request"`) {
		t.Errorf("garbage body: ok=%v code=%d body=%s", ok, w.Code, w.Body.String())
	}

	// Well-formed but invalid → 400 validation_failed with per-field errors.
	w, _, ok = post(`{"email":"a@b.c"}`)
	if ok || w.Code != http.StatusBadRequest {
		t.Fatalf("invalid body: ok=%v code=%d", ok, w.Code)
	}
	var p httpx.Problem
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Type != httpx.TypeValidationFailed || len(p.Errors) != 1 || p.Errors[0].Field != "name" {
		t.Errorf("problem = %+v, want validation_failed naming the name field", p)
	}
}
