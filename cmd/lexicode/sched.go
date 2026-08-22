// sched.go is the S22 wiring glue: the small adapters that connect the kernel scheduler's
// seams to the layers above it (the S19 spec builder in internal/service/runs, the docker
// module's egress proxy, the tickets service's category mover). They live here because
// cmd/lexicode is the only wiring site (architecture §2.1) — the kernel cannot import the
// service or module packages these forward to.
package main

import (
	"context"
	"errors"
	"net/url"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/sched"
	dockermod "github.com/spruce/lexicode/internal/module/docker"
	runsvc "github.com/spruce/lexicode/internal/service/runs"
)

// specBuilderAdapter satisfies sched.SpecBuilder over the S19 Builder.
type specBuilderAdapter struct{ b *runsvc.Builder }

func (a specBuilderAdapter) Build(ctx context.Context, in sched.SpecInput) (sched.SpecResult, error) {
	prep, err := a.b.Build(ctx, runsvc.PrepInput{
		Workspace: in.Workspace, Project: in.Project, Repo: in.Repo,
		Agent: in.Agent, Ticket: in.Ticket, Run: in.Run, RunToken: in.RunToken,
	})
	if err != nil {
		return sched.SpecResult{}, err
	}
	return sched.SpecResult{Spec: prep.Spec, Branch: prep.Branch, SecretValues: prep.SecretValues}, nil
}

// proxyAdapter satisfies sched.ProxyRegistrar over the docker module's proxy, tolerating a
// module that started without one (no proxy port configured).
type proxyAdapter struct{ proxy func() *dockermod.Proxy }

func (a proxyAdapter) Register(runID, token string, policy ports.NetworkPolicy, gitHosts ...string) {
	if p := a.proxy(); p != nil {
		p.Register(runID, token, policy, gitHosts...)
	}
}

func (a proxyAdapter) Unregister(runID string) {
	if p := a.proxy(); p != nil {
		p.Unregister(runID)
	}
}

// lateRequester forwards the tickets service's run requests to a scheduler that is
// constructed after the service (the two reference each other; the seam breaks the cycle).
type lateRequester struct{ s **sched.Scheduler }

func (l lateRequester) RequestRun(ctx context.Context, req sched.RunRequest) (string, error) {
	if *l.s == nil {
		return "", sched.ErrNotImplemented
	}
	return (*l.s).RequestRun(ctx, req)
}

func (l lateRequester) CancelTicketRuns(ctx context.Context, ticketID, reason string) (int64, error) {
	if *l.s == nil {
		return 0, sched.ErrNotImplemented
	}
	return (*l.s).CancelTicketRuns(ctx, ticketID, reason)
}

// lateRunControl is the same trick for the runs service's stop/steer surface.
type lateRunControl struct{ s **sched.Scheduler }

func (l lateRunControl) StopRun(ctx context.Context, runID, reason string) error {
	if *l.s == nil {
		return errors.New("the run scheduler is not running")
	}
	return (*l.s).StopRun(ctx, runID, reason)
}

func (l lateRunControl) NotifySteering(runID string) {
	if *l.s != nil {
		(*l.s).NotifySteering(runID)
	}
}

// ticketMoverFunc adapts a closure to sched.TicketMover.
type ticketMoverFunc func(ctx context.Context, ticketID string, cat domain.ColumnCategory, note string) error

func (f ticketMoverFunc) MoveTicketToCategory(ctx context.Context, ticketID string, cat domain.ColumnCategory, note string) error {
	return f(ctx, ticketID, cat, note)
}

// gitHostsFor names the git remote hosts a run's egress policy must allow. GitHub's default
// host unless the adapter was pointed elsewhere (tests, GHE).
func gitHostsFor(gitHubBaseURL string) func(domain.Repo) []string {
	host := "github.com"
	if gitHubBaseURL != "" {
		if u, err := url.Parse(gitHubBaseURL); err == nil && u.Host != "" {
			host = u.Hostname()
		}
	}
	return func(repo domain.Repo) []string {
		if repo.Provider == "github" || repo.Provider == "" {
			return []string{host}
		}
		return nil
	}
}
