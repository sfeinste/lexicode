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
	"github.com/spruce/lexicode/internal/kernel/sched"
	"github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/kernel/store/seed"
	"github.com/spruce/lexicode/internal/logging"
	claudecodemod "github.com/spruce/lexicode/internal/module/claudecode"
	credentialsmod "github.com/spruce/lexicode/internal/module/credentials"
	dockermod "github.com/spruce/lexicode/internal/module/docker"
	githubmod "github.com/spruce/lexicode/internal/module/github"
	agentsvc "github.com/spruce/lexicode/internal/service/agents"
	"github.com/spruce/lexicode/internal/service/board"
	"github.com/spruce/lexicode/internal/service/bootstrap"
	credsvc "github.com/spruce/lexicode/internal/service/credentials"
	"github.com/spruce/lexicode/internal/service/projects"
	secretsvc "github.com/spruce/lexicode/internal/service/secrets"
	"github.com/spruce/lexicode/internal/service/tickets"
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

	// The secret store opens right after the database: a master key file with lax
	// permissions, or one that is not a key at all, refuses boot here with an actionable
	// message (D-16) — before any listener exists.
	sec, err := secrets.Open(secrets.Options{
		Store: st, KeyPath: cfg.MasterKeyFile(), Logger: logger,
	})
	if err != nil {
		return err
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
		Logger: logger, Mux: mux, Store: st, Bus: b, Auth: authSvc, Audit: auditW,
		Secrets: sec, SSE: hub,
	})

	// Services (architecture §2.1: wiring happens here and only here). Each service registers
	// its own routes against the shared mux; auth guards are applied per route inside Routes.
	projectsSvc := projects.New(projects.Options{Store: st, Audit: auditW, Bus: b, Logger: logger})
	projectsSvc.Routes(mux, authSvc)
	boardSvc := board.New(board.Options{Store: st, Audit: auditW, Bus: b, Logger: logger})
	boardSvc.Routes(mux, authSvc)
	// The tickets service talks to the run scheduler only through the sched seam; until S22
	// lands, sched.Unscheduled is the documented no-op behind column auto-start and
	// archive-time run cancellation.
	ticketsSvc := tickets.New(tickets.Options{
		Store: st, Audit: auditW, Bus: b, Sched: sched.Unscheduled{}, Logger: logger,
	})
	ticketsSvc.Routes(mux, authSvc)
	secretsSvc := secretsvc.New(secretsvc.Options{
		Store: st, Secrets: sec, Audit: auditW, Logger: logger,
	})
	secretsSvc.Routes(mux, authSvc)
	agentsSvc := agentsvc.New(agentsvc.Options{Store: st, Audit: auditW, Bus: b, Logger: logger})
	agentsSvc.Routes(mux, authSvc)

	// Modules (architecture §3.1); each is one line here. actions, context and notify
	// arrive with the stories that build them. testkit is never wired here — it ships as a
	// plain package that only tests import.
	ghMod := githubmod.New(githubmod.Options{BaseURL: cfg.GitHubBaseURL})
	if err := k.RegisterModule(ghMod); err != nil {
		return err
	}
	dockerMod := dockermod.New(dockermod.Options{Host: cfg.DockerHost, ProxyPort: cfg.ProxyPort})
	if err := k.RegisterModule(dockerMod); err != nil {
		return err
	}
	// The Respond seam stays nil until the S21 MCP server registers itself through
	// claudecodeMod.Runtime(): elicitation answers travel as the MCP tool's result.
	claudecodeMod := claudecodemod.New(claudecodemod.Options{})
	if err := k.RegisterModule(claudecodeMod); err != nil {
		return err
	}
	credsMod := credentialsmod.New(credentialsmod.Options{Secrets: sec})
	if err := k.RegisterModule(credsMod); err != nil {
		return err
	}
	// The credentials settings service checks health through the module's concrete sources —
	// handed over here, at the one wiring site, like bootstrap's DocLister.
	credentialsSvc := credsvc.New(credsvc.Options{
		Secrets: sec, Audit: auditW, Logger: logger,
		OAuth: credsMod.OAuth(), Env: credsMod.Env(),
		SecretName: credentialsmod.OAuthSecretName,
	})
	credentialsSvc.Routes(mux, authSvc)

	// The bootstrap service resolves the forge port through the kernel at call time; its doc
	// detection uses the github adapter's extra ListDir/ReadFileIfExists methods, which the
	// frozen ForgeProvider port does not carry — the concrete value is handed over here, at
	// the one wiring site (see bootstrap.DocLister).
	bootstrapSvc := bootstrap.New(bootstrap.Options{
		Store: st, Secrets: sec, Forge: k.Forge, Docs: ghMod.Forge(),
		Audit: auditW, Bus: b, Logger: logger,
	})
	bootstrapSvc.Routes(mux, authSvc)

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
