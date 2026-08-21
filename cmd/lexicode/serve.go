package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spruce/lexicode/internal/config"
	"github.com/spruce/lexicode/internal/logging"
	webui "github.com/spruce/lexicode/web"
)

// shutdownGrace bounds how long in-flight requests have to finish after a signal. The acceptance
// criterion for story S01 is "exits 0 within 2s", so this leaves headroom for the final flush.
const shutdownGrace = 1500 * time.Millisecond

func cmdServe(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("lexicode serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	config.Bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(config.Options{Flags: fs})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory %s: %w", cfg.DataDir, err)
	}

	level, err := logging.ParseLevel(cfg.LogLevel)
	if err != nil {
		return err
	}
	logger, closeLog, err := logging.Setup(logging.Options{
		Level:  level,
		File:   cfg.LogFile(),
		Stderr: stderr,
	})
	if err != nil {
		return err
	}
	defer func() { _ = closeLog.Close() }()
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return serve(ctx, cfg, logger, stdout)
}

func serve(ctx context.Context, cfg config.Config, logger *slog.Logger, stdout io.Writer) error {
	srv := &http.Server{
		Handler:           newHandler(logger),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	// Listen before announcing anything, so that "address in use" is reported as a start-up error
	// rather than as a browser tab that never loads.
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", cfg.Addr())
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr(), err)
	}

	logger.Info("lexicode starting",
		slog.String("version", versionString()),
		slog.String("addr", ln.Addr().String()),
		slog.String("data_dir", cfg.DataDir),
		slog.String("log_file", cfg.LogFile()),
		slog.String("config_file", cfg.FilePath()),
		slog.Bool("frontend_embedded", webui.Built()),
	)
	fmt.Fprintf(stdout, "Lexicode is listening on %s\n", cfg.URL())
	if !webui.Built() {
		fmt.Fprintln(stdout, "This binary has no embedded frontend; run \"make build\" for the dashboard.")
	}

	if cfg.OpenBrowser {
		if err := openBrowser(cfg.URL()); err != nil {
			logger.Warn("could not open a browser tab; open the URL yourself",
				slog.String("url", cfg.URL()), slog.String("error", err.Error()))
		}
	}

	errCh := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down", slog.Duration("grace", shutdownGrace))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		// A client that will not let go must not turn a clean stop into a failure exit.
		logger.Warn("graceful shutdown timed out; closing connections",
			slog.String("error", err.Error()))
		_ = srv.Close()
	}
	<-errCh
	logger.Info("stopped")
	return nil
}

// newHandler wires the two surfaces story S01 has: the JSON API namespace, which so far contains
// nothing, and the embedded SPA. Story S06 replaces this with kernel/httpx and its middleware
// chain; the /api/ prefix is registered first here so that the SPA fallback can never answer an
// API request with HTML.
func newHandler(logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, http.StatusNotFound, "not_found",
			"No such endpoint",
			fmt.Sprintf("%s %s is not an endpoint of this server.", r.Method, r.URL.Path))
	}))
	mux.Handle("/healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`+"\n")
	}))
	mux.Handle("/", webui.Handler())
	return requestLogger(logger, mux)
}

func requestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.Debug("http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Duration("duration", time.Since(start)),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

// problem is RFC 9457 application/problem+json. Architecture §14 requires a stable "type" slug the
// frontend can switch on, which is why type is a slug and not a URL.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func writeProblem(w http.ResponseWriter, status int, slug, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Type: slug, Title: title, Status: status, Detail: detail})
}
