package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
)

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// Registrar is the "somewhere to register routes" half of a mux: what auth, the kernel and the
// modules need to add their endpoints without caring whether it is a *Mux or a bare
// *http.ServeMux (tests use the latter).
type Registrar interface {
	Handle(pattern string, handler http.Handler)
}

// Mux is a thin wrapper over net/http's 1.22 pattern mux (contracts §1: Kernel.Mux() returns
// one of these). It adds exactly one thing: the middleware chain every request passes through —
// request id, one slog line per request, panic recovery to a 500 problem — plus per-prefix
// middleware for wrapping a namespace (cmd/lexicode wraps /api/ in CSRF and the setup gate).
// There is deliberately no third-party router and no feature the stdlib mux already has.
//
// CORS is off by default: a same-origin single binary needs none, and absent headers deny by
// browser default. Set Options.AllowedOrigins to opt specific origins in.
type Mux struct {
	logger *slog.Logger
	mux    *http.ServeMux
	cors   []string

	mu       sync.Mutex
	prefixMW map[string][]Middleware
	prefixes []string // longest-first, so the most specific namespace wins
}

// Options configures NewMux. The zero value is usable.
type Options struct {
	// Logger receives the per-request lines. Nil means slog.Default().
	Logger *slog.Logger
	// AllowedOrigins enables CORS for exactly these origins. Empty (the default) adds no CORS
	// headers at all — cross-origin browser requests are refused by the browser itself.
	AllowedOrigins []string
}

// NewMux builds a mux.
func NewMux(opts Options) *Mux {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Mux{
		logger:   logger,
		mux:      http.NewServeMux(),
		cors:     opts.AllowedOrigins,
		prefixMW: map[string][]Middleware{},
	}
}

// Handle registers a handler for a net/http 1.22 pattern ("GET /api/v1/projects/{key}").
func (m *Mux) Handle(pattern string, handler http.Handler) { m.mux.Handle(pattern, handler) }

// HandleFunc registers a handler function for a pattern.
func (m *Mux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	m.mux.HandleFunc(pattern, handler)
}

// UsePrefix wraps every request whose path starts with prefix in the given middleware, first
// listed outermost. cmd/lexicode uses it to put CSRF and the setup gate around the whole /api/
// namespace, so a route added by a later story cannot forget them. Requests outside every
// prefix reach the underlying mux directly. Calling UsePrefix twice for one prefix appends.
func (m *Mux) UsePrefix(prefix string, mw ...Middleware) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.prefixMW[prefix]; !ok {
		m.prefixes = append(m.prefixes, prefix)
		sort.Slice(m.prefixes, func(i, j int) bool { return len(m.prefixes[i]) > len(m.prefixes[j]) })
	}
	m.prefixMW[prefix] = append(m.prefixMW[prefix], mw...)
}

// ServeHTTP runs the chain: request id → log line → panic recovery → CORS (when enabled) →
// prefix middleware → route. Recovery sits inside logging so a panicked request still logs with
// its status; prefix middleware sits inside recovery so a panicking gate cannot escape it.
func (m *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := newRequestID()
	w.Header().Set("X-Request-ID", id)
	r = r.WithContext(withRequestID(r.Context(), id))

	rec := &statusWriter{ResponseWriter: w}
	start := time.Now()
	defer func() {
		m.logger.Info("http request",
			slog.String("request_id", id),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.Status()),
			slog.Duration("duration", time.Since(start)),
		)
	}()

	m.recovered(rec, r)
}

// recovered is the panic barrier: a panicking handler becomes a 500 problem (when the response
// has not started), an error line with the stack, and nothing else — the server lives on.
func (m *Mux) recovered(w *statusWriter, r *http.Request) {
	defer func() {
		v := recover()
		if v == nil {
			return
		}
		if v == http.ErrAbortHandler { //nolint:errorlint // sentinel comparison per net/http docs
			panic(v) // deliberate connection abort; net/http handles it
		}
		m.logger.Error("panic in http handler",
			slog.String("request_id", RequestID(r.Context())),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("panic", fmt.Sprint(v)),
			slog.String("stack", string(debug.Stack())),
		)
		if !w.wroteHeader {
			WriteProblem(w, http.StatusInternalServerError, TypeInternal,
				"Internal error", "Something went wrong on the server. The error has been logged.")
		}
	}()

	if len(m.cors) > 0 && m.serveCORS(w, r) {
		return
	}
	m.routed(w, r)
}

// routed applies the prefix middleware for the longest matching prefix, then routes.
func (m *Mux) routed(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	var chain []Middleware
	for _, p := range m.prefixes {
		if strings.HasPrefix(r.URL.Path, p) {
			chain = m.prefixMW[p]
			break
		}
	}
	m.mu.Unlock()

	var h http.Handler = m.mux
	for i := len(chain) - 1; i >= 0; i-- {
		h = chain[i](h)
	}
	h.ServeHTTP(w, r)
}

// serveCORS adds the CORS headers for an allowed origin and answers preflights itself. It
// reports whether the request was fully handled (true only for an allowed preflight).
func (m *Mux) serveCORS(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	allowed := false
	for _, o := range m.cors {
		if o == origin {
			allowed = true
			break
		}
	}
	if !allowed {
		return false
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Credentials", "true")
	h.Add("Vary", "Origin")
	if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
		h.Set("Access-Control-Allow-Headers", "Content-Type, Last-Event-ID")
		h.Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}

// ---------------------------------------------------------------- request id -----

type requestIDKey struct{}

// newRequestID is 8 random bytes, hex — short enough to read in a log line, unique enough to
// grep for. Incoming X-Request-ID headers are deliberately ignored: this server is the edge,
// and a client-chosen id would let one request impersonate another in the logs.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "rid-rand-failed"
	}
	return hex.EncodeToString(b[:])
}

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the request id the mux assigned, or "" outside a request.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// ---------------------------------------------------------------- status writer -----

// statusWriter records the status for the log line. Unwrap keeps http.ResponseController (and
// with it flushing, which SSE needs) working through the wrapper.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.status = http.StatusOK
		s.wroteHeader = true
	}
	return s.ResponseWriter.Write(b)
}

// Flush lets SSE and other streaming handlers flush through the wrapper on Go versions and
// handlers that type-assert http.Flusher directly rather than using http.ResponseController.
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		if !s.wroteHeader {
			s.status = http.StatusOK
			s.wroteHeader = true
		}
		f.Flush()
	}
}

// Unwrap is the http.ResponseController escape hatch.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Status is the status the handler wrote, defaulting to 200 like net/http itself.
func (s *statusWriter) Status() int {
	if !s.wroteHeader {
		return http.StatusOK
	}
	return s.status
}
