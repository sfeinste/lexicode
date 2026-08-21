package main

import (
	"context"
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
	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/kernel/store/seed"
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
	demo := fs.Bool("demo", false, "seed an empty database with demo fixtures before serving")
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

	return serve(ctx, cfg, logger, stdout, *demo)
}

func serve(ctx context.Context, cfg config.Config, logger *slog.Logger, stdout io.Writer, demo bool) error {
	// cmd/lexicode is the only wiring site (architecture §2.1): the kernel is built here, the
	// store is opened and migrated here, the modules are registered here, and nothing below
	// this line knows which modules exist.
	st, err := store.Open(store.Options{Path: cfg.DBFile(), Logger: logger})
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	if _, err := st.Migrate(ctx); err != nil {
		return err
	}

	// --demo seeds only an empty database: rerunning with the flag against real data is a no-op
	// with a note, never a merge.
	if demo {
		empty, err := seed.IsEmpty(ctx, st)
		if err != nil {
			return err
		}
		if !empty {
			fmt.Fprintln(stdout, "--demo: database is not empty; leaving it untouched")
		} else if _, err := seed.Apply(ctx, st); err != nil {
			return err
		} else {
			fmt.Fprintln(stdout, "--demo: seeded demo workspace")
		}
	}

	mux := httpx.NewMux(httpx.Options{Logger: logger})
	b := bus.New(bus.Options{Store: st, Logger: logger})
	authSvc := auth.New(auth.Options{Store: st, Logger: logger})
	authSvc.Routes(mux)
	auditW := audit.New(audit.Options{Store: st, Logger: logger})
	hub := httpx.NewHub(httpx.HubOptions{Logger: logger, Store: st})
	// The hub subscribes before b.Start so that boot-recovered events reach open tabs too.
	if err := hub.Attach(b); err != nil {
		return err
	}
	k := kernel.New(kernel.Options{
		Logger: logger, Mux: mux, Store: st, Bus: b, Auth: authSvc, Audit: auditW, SSE: hub,
	})

	// No modules yet. github, docker, claudecode, actions, context and notify arrive with the
	// stories that build them (architecture §3.1); each is one line here.
	if err := k.RegisterModule(); err != nil {
		return err
	}

	// Init all → (migrate, story S03) → Start all → serve → Stop all in reverse.
	if err := k.Init(); err != nil {
		return err
	}
	// Modules stop after everything else this function does, in reverse registration order, with
	// their own deadline (kernel.StopTimeout). A deferred call is what makes that true on the
	// serve-error path as well as on the signal path. The signal context is cancelled by then, so
	// shutdown runs on a fresh one. The bus stops after the modules: publishers drain before the
	// queues close, and anything still pending is recovered on the next boot (D-13).
	defer func() {
		k.Stop(context.Background())
		busCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := b.Stop(busCtx); err != nil {
			logger.Warn("event bus did not stop cleanly", slog.String("error", err.Error()))
		}
		logger.Info("stopped")
	}()

	// Boot recovery: re-dispatch events a previous process persisted but never finished
	// dispatching (D-13). After Init, so that every module's subscriptions exist.
	if err := b.Start(ctx); err != nil {
		return err
	}

	srv := &http.Server{
		Handler:           newHandler(mux, authSvc),
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

	// Modules start before the listener is served so that a module which needs to be ready for
	// the first request is. A Start failure marks the module degraded and boot continues.
	k.Start(ctx)
	for _, m := range k.Modules() {
		if m.State == kernel.StateDegraded {
			fmt.Fprintf(stdout, "Module %s is degraded: %s\n", m.Name, m.Reason)
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
	// SSE streams never end on their own; close them first or Shutdown would wait its whole
	// grace period on them.
	hub.Close()
	// The signal context is already cancelled here, so shutdown runs on a fresh one.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		// A client that will not let go must not turn a clean stop into a failure exit.
		logger.Warn("graceful shutdown timed out; closing connections",
			slog.String("error", err.Error()))
		_ = srv.Close()
	}
	<-errCh
	return nil
}

// newHandler assembles the process's HTTP surface on the kernel's httpx mux: the routes the
// kernel, auth and the modules registered, the JSON API namespace fallback, /healthz and the
// embedded SPA. The mux itself carries the S06 middleware chain (request id, one log line per
// request, panic recovery to a 500 problem); this function adds only routes and the /api/
// namespace wrapping. The "/api/" pattern is a catch-all for the namespace; Go's mux resolves
// by specificity, so a real endpoint such as /api/v1/system/modules always wins over it.
//
// The whole /api/ namespace runs behind two auth-owned checks (S05): CSRF on unsafe methods,
// and the first-run setup gate — with zero users, everything but POST /api/v1/auth/setup is a
// 401 "setup_required". Applying them as prefix middleware means a route added by a later
// story cannot forget them. The SPA and /healthz stay outside both.
func newHandler(mux *httpx.Mux, authSvc *auth.Service) http.Handler {
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"No such endpoint",
			fmt.Sprintf("%s %s is not an endpoint of this server.", r.Method, r.URL.Path))
	}))
	mux.Handle("/healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`+"\n")
	}))
	mux.Handle("/", webui.Handler())

	mux.UsePrefix("/api/", auth.CSRF, authSvc.SetupGate)
	return mux
}
