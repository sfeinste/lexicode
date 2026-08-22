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
	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/guard"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/sched"
	"github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/kernel/store/seed"
	"github.com/spruce/lexicode/internal/logging"
	actionsmod "github.com/spruce/lexicode/internal/module/actions"
	claudecodemod "github.com/spruce/lexicode/internal/module/claudecode"
	contextmod "github.com/spruce/lexicode/internal/module/context"
	credentialsmod "github.com/spruce/lexicode/internal/module/credentials"
	cronmod "github.com/spruce/lexicode/internal/module/cron"
	dockermod "github.com/spruce/lexicode/internal/module/docker"
	githubmod "github.com/spruce/lexicode/internal/module/github"
	notifymod "github.com/spruce/lexicode/internal/module/notify"
	agentsvc "github.com/spruce/lexicode/internal/service/agents"
	"github.com/spruce/lexicode/internal/service/board"
	"github.com/spruce/lexicode/internal/service/bootstrap"
	"github.com/spruce/lexicode/internal/service/contextres"
	credsvc "github.com/spruce/lexicode/internal/service/credentials"
	mcpsvc "github.com/spruce/lexicode/internal/service/mcp"
	notifysvc "github.com/spruce/lexicode/internal/service/notify"
	"github.com/spruce/lexicode/internal/service/projects"
	runsvc "github.com/spruce/lexicode/internal/service/runs"
	secretsvc "github.com/spruce/lexicode/internal/service/secrets"
	"github.com/spruce/lexicode/internal/service/tickets"
	triggersvc "github.com/spruce/lexicode/internal/service/triggers"
	wikisvc "github.com/spruce/lexicode/internal/service/wiki"
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
	// The tickets service talks to the run scheduler only through the sched seam. The
	// scheduler is constructed further down (it needs the MCP server and the modules), so
	// the seam is late-bound through a pointer the wiring fills in before serving.
	var scheduler *sched.Scheduler
	ticketsSvc := tickets.New(tickets.Options{
		Store: st, Audit: auditW, Bus: b, Sched: lateRequester{s: &scheduler}, Logger: logger,
	})
	ticketsSvc.Routes(mux, authSvc)
	// The S31 snoozed-until-activity waker subscribes before the bus starts, like every
	// subscriber: an event whose subject matches a snoozed ticket flips it back to pending.
	if err := ticketsSvc.SubscribeTriageWake(b); err != nil {
		return err
	}
	runsSvc := runsvc.New(runsvc.Options{
		Store: st, Audit: auditW, Sched: lateRunControl{s: &scheduler}, Logger: logger,
	})
	runsSvc.Routes(mux, authSvc)
	secretsSvc := secretsvc.New(secretsvc.Options{
		Store: st, Secrets: sec, Audit: auditW, Logger: logger,
	})
	secretsSvc.Routes(mux, authSvc)
	agentsSvc := agentsvc.New(agentsvc.Options{Store: st, Audit: auditW, Bus: b, Logger: logger})
	agentsSvc.Routes(mux, authSvc)
	// The wiki service (S33): pages, versions, FTS search, mentions/backlinks.
	wikiSvc := wikisvc.New(wikisvc.Options{Store: st, Audit: auditW, Bus: b, Logger: logger})
	wikiSvc.Routes(mux, authSvc)
	// The Lexicode MCP server (S21, D-12): elicitations, approvals, step reporting, wiki
	// proposals and criterion checks, blocking on humans. SetRunState routes into the S22
	// scheduler — the only writer of runs.state — through the same late-bound pointer. The
	// MCP endpoint is mounted twice (see mcp doc.go): here on the main mux, and below on the
	// egress-proxy listener for containers.
	mcpSvc := mcpsvc.New(mcpsvc.Options{
		Store: st, Bus: b, Audit: auditW, Logger: logger,
		SetRunState: func(ctx context.Context, runID string, state domain.RunState, reason string) error {
			if scheduler == nil {
				return fmt.Errorf("sched: the scheduler is not running")
			}
			return scheduler.SetRunState(ctx, runID, state, reason)
		},
	})
	mcpSvc.Routes(mux, authSvc)
	mux.Handle("/mcp/{token}", mcpSvc.Handler())
	// The notify service (S24, architecture §12): the escalation ticker that turns an
	// unanswered elicitation into the delegating human's notification row, and the
	// endpoints the inbox badge reads. The ticker starts alongside the bus, below.
	notifySvc := notifysvc.New(notifysvc.Options{Store: st, Bus: b, Logger: logger})
	notifySvc.Routes(mux, authSvc)
	// The context-resolution surfaces (S34, architecture §11): the agent detail's dry
	// preview and the wiki context-budget endpoint, plus the daily verified_until demotion
	// job (boot + every 24h; started below with the other tickers). The resolver itself is
	// the scheduler's — reached through the same late-bound pointer as every other
	// scheduler seam, so the preview and a real enqueue share one code path.
	contextresSvc := contextres.New(contextres.Options{
		Store: st, Resolver: lateResolver{s: &scheduler}, Audit: auditW, Bus: b,
		Notify: notifySvc.DeliverInApp, Logger: logger,
	})
	contextresSvc.Routes(mux, authSvc)
	// The trigger CRUD surface (S26). Validation resolves event catalogs and registered
	// actions through the kernel registries lazily, at request time — the modules register
	// during k.Init below.
	triggersSvc := triggersvc.New(triggersvc.Options{
		Store: st, Audit: auditW, Bus: b, Logger: logger,
		Sources: k.EventSources, Action: k.Action, Actions: k.Actions,
	})
	triggersSvc.Routes(mux, authSvc)
	// The trigger engine (S26, architecture §8): match → conditions → guard → actions.
	// Stage 3 is the S27 loop guard — the five layers of architecture §9, each defaulting
	// on, configured per trigger. Its loop-stopped run seam reaches the scheduler through
	// the same late-bound pointer as everything else (only the scheduler writes runs.state).
	// Stage 4 resolves actions from the registry, which is empty until S28 — a stored
	// action fires as `errored` naming the missing ID, with no side effects.
	loopGuard := guard.New(guard.Options{
		Store: st, Loop: lateLoopStopper{s: &scheduler}, Logger: logger,
	})
	triggerEngine := triggersvc.NewEngine(triggersvc.EngineOptions{
		Store: st, Bus: b, Logger: logger,
		Guard: loopGuard, Sources: k.EventSources,
		Action: k.Action, Kernel: k,
	})
	if err := triggerEngine.Subscribe(b); err != nil {
		return err
	}

	// Modules (architecture §3.1); each is one line here. actions, context and notify
	// arrive with the stories that build them. testkit is never wired here — it ships as a
	// plain package that only tests import.
	ghMod := githubmod.New(githubmod.Options{BaseURL: cfg.GitHubBaseURL})
	if err := k.RegisterModule(ghMod); err != nil {
		return err
	}
	// The cron event source (S32): `schedule` · `cron` events for the triggers that store an
	// expression. Store, logger and the bus emit are wired from the kernel in its Init.
	if err := k.RegisterModule(cronmod.New(cronmod.Options{})); err != nil {
		return err
	}
	dockerMod := dockermod.New(dockermod.Options{Host: cfg.DockerHost, ProxyPort: cfg.ProxyPort})
	if err := k.RegisterModule(dockerMod); err != nil {
		return err
	}
	// The Respond seam routes Handle.Respond into the MCP server, which holds the blocking
	// tool call: answers travel back as the MCP tool's result, never stdin (contracts §3.4).
	claudecodeMod := claudecodemod.New(claudecodemod.Options{
		Respond: func(ctx context.Context, runID, elicitationID string, r ports.Response) error {
			_ = runID // the elicitation row carries its run; the id is authoritative
			_, err := mcpSvc.Resolve(ctx, elicitationID, r, nil)
			return err
		},
	})
	if err := k.RegisterModule(claudecodeMod); err != nil {
		return err
	}
	credsMod := credentialsmod.New(credentialsmod.Options{Secrets: sec})
	if err := k.RegisterModule(credsMod); err != nil {
		return err
	}
	// The context-provider module (contracts §2.6): all four of architecture §11's
	// providers. The scheduler resolves them at enqueue for prompt assembly; repofiles
	// enumerates instruction files through the github adapter's DocLister methods, the
	// same seam bootstrap doc detection uses (there is no checkout at resolve time).
	if err := k.RegisterModule(contextmod.New(contextmod.Options{
		Store: st, Secrets: sec, Docs: ghMod.Forge(), Logger: logger,
	})); err != nil {
		return err
	}
	// The notify module (S28): the "inapp" Notifier port impl. Delivery is injected from the
	// S24 notify service — the module may not import internal/service, so the seam inverts
	// here, at the one wiring site.
	if err := k.RegisterModule(notifymod.New(notifymod.Options{
		Deliver: notifySvc.DeliverInApp,
	})); err != nil {
		return err
	}
	// The actions module (S28): the five TriggerActions behind the THEN column. The tickets
	// funcs (create-into-triage, category move) and the S24 routing rule are the same seam
	// inversion; everything kernel-owned the module reads from k at Init.
	if err := k.RegisterModule(actionsmod.New(actionsmod.Options{
		Tickets: actionsmod.TicketSeam{
			CreateInTriage: func(ctx context.Context, in actionsmod.TriageCreate) (domain.Ticket, error) {
				return ticketsSvc.CreateFromTrigger(ctx, tickets.TriggerCreateInput{
					ProjectID: in.ProjectID, Title: in.Title, Description: in.Description,
					LabelNames: in.LabelNames, Provenance: in.Provenance,
					SourceTriggerID: in.TriggerID, SourceRunID: in.RunID,
				})
			},
			MoveToCategory: ticketsSvc.TriggerMoveToCategory,
		},
		Notify: actionsmod.NotifySeam{RouteRun: notifySvc.RouteTo},
	})); err != nil {
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

	// Containers reach the MCP endpoint through the egress-proxy listener (S21 reachability;
	// internal/service/mcp/doc.go). The proxy exists only after the docker module's Init and
	// only when a proxy port is configured; without it, host-side MCP still serves on the
	// main mux.
	if proxy := dockerMod.Proxy(); proxy != nil {
		proxy.SetMCPHandler(mcpSvc.Handler())
	}

	// The run scheduler (S22, D-14): the kernel-owned centre. Built after Init so the port
	// registries are populated; its seams — the S19 spec builder, the MCP token authority,
	// the egress proxy, the tickets category mover — are wired here and nowhere else.
	builder := &runsvc.Builder{
		Secrets:    sec,
		Forge:      k.Forge,
		Credential: k.CredentialSource,
		ProxyEnv: func(runID string) (map[string]string, bool) {
			if p := dockerMod.Proxy(); p != nil {
				return p.ProxyEnv(runID)
			}
			return nil, false
		},
		BranchTaken: func(ctx context.Context, projectID, branch string) (bool, error) {
			return st.Runs().BranchInUse(ctx, projectID, branch)
		},
		MCPBaseURL: fmt.Sprintf("http://host.docker.internal:%d", cfg.ProxyPort),
	}
	scheduler = sched.New(sched.Options{
		Store: st, Bus: b, Audit: auditW, Logger: logger,
		Sandbox:   k.Sandbox,
		Runtime:   k.Runtime,
		Providers: k.ContextProviders,
		Specs:     specBuilderAdapter{b: builder},
		Tokens:    mcpSvc,
		Proxy:     proxyAdapter{proxy: dockerMod.Proxy},
		Tickets: ticketMoverFunc(func(ctx context.Context, ticketID string, cat domain.ColumnCategory, note string) error {
			return ticketsSvc.MoveTicketToCategory(ctx, ticketID, cat, note)
		}),
		// §10.4 step 6 (S24): the orchestrator opens a completed run's PR from its pushed
		// branch — through the forge port, so the open_prs grant and the D-9 marker are
		// enforced in the adapter.
		PRs: &runsvc.PROpener{
			Store: st, Secrets: sec, Forge: k.Forge, Logger: logger,
		},
		GitHosts: gitHostsFor(cfg.GitHubBaseURL),
	})
	k.AttachScheduler(scheduler)
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
		// The engine stops after the bus: no deliveries are in flight once the bus has
		// drained, so the workers only have their own queues left to abandon.
		engCtx, cancelEng := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelEng()
		if err := triggerEngine.Stop(engCtx); err != nil {
			logger.Warn("trigger engine did not stop cleanly", slog.String("error", err.Error()))
		}
		logger.Info("stopped")
	}()

	// Boot recovery: re-dispatch events a previous process persisted but never finished
	// dispatching (D-13). After Init, so that every module's subscriptions exist.
	if err := b.Start(ctx); err != nil {
		return err
	}
	notifySvc.Start(ctx)
	defer notifySvc.Wait()
	// The S34 verified_until demotion job: on boot and every 24h.
	contextresSvc.Start(ctx)
	defer contextresSvc.Wait()
	// The S31 triage ticker: time-snoozed items past snooze_until wake back to pending.
	ticketsSvc.StartTriageTicker(ctx)
	defer ticketsSvc.WaitTriageTicker()

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

	// The scheduler starts after the modules: crash reconciliation (§10.6) needs the sandbox
	// registered and its daemon preflighted. On SIGINT the scheduler drains WITHOUT
	// destroying running containers — they keep working and the next boot reattaches them.
	if err := scheduler.Start(ctx); err != nil {
		return err
	}
	defer scheduler.Stop(context.Background())
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
