package docker

import (
	"context"
	"fmt"
	"log/slog"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// Sweep removes orphaned containers (architecture §10.6): every container carrying *this
// instance's* lexicode.owner label whose run no longer exists or is terminal is force-removed
// together with its volumes. Containers whose runs are non-terminal are left alone — those are
// the ones boot reconciliation reattaches. Returns how many containers were removed.
//
// The owner filter is load-bearing, not hygiene. A machine can hold several Lexicodes — the
// product, an acceptance run, a demo workspace — and a run belonging to another one is unknown
// to this database by definition. Keying the sweep on anything less specific than the owner
// (the lexicode.instance label's presence, say) makes "not my run" mean "not anyone's run", and
// the sweep kills live containers other instances are working in. Unknown run plus foreign
// owner is never an orphan; it is someone else's work.
//
// Sweep runs on module Start and hourly after that; tests call it directly.
func (s *Sandbox) Sweep(ctx context.Context) (int, error) {
	if s.runState == nil {
		return 0, fmt.Errorf("docker: sweeper has no run-state lookup; refusing to guess")
	}
	if s.owner == "" {
		return 0, fmt.Errorf("docker: sweeper has no owner identity; refusing to guess whose containers these are")
	}

	args := filters.NewArgs(filters.Arg("label", labelOwner+"="+s.owner))
	list, err := s.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return 0, fmt.Errorf("docker: listing labelled containers: %w", err)
	}

	removed := 0
	for _, c := range list {
		runID := c.Labels[labelRun]
		orphan := false
		if runID == "" {
			// Ours by owner, but with no run at all: nothing can ever claim it again.
			orphan = true
		} else {
			state, found, err := s.runState(ctx, runID)
			if err != nil {
				// A lookup error is not a license to delete; skip and let the next sweep
				// retry.
				s.logger.Warn("docker: sweep could not resolve run; leaving container",
					slog.String("container", shortID(c.ID)), slog.String("run", runID),
					slog.String("error", err.Error()))
				continue
			}
			orphan = !found || state.Terminal()
		}
		if !orphan {
			continue
		}
		err := s.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{
			Force:         true,
			RemoveVolumes: true,
		})
		if err != nil && !cerrdefs.IsNotFound(err) {
			s.logger.Warn("docker: sweep could not remove orphaned container",
				slog.String("container", shortID(c.ID)), slog.String("error", err.Error()))
			continue
		}
		s.logger.Info("docker: removed orphaned container",
			slog.String("container", shortID(c.ID)), slog.String("run", runID))
		removed++
	}
	return removed, nil
}
