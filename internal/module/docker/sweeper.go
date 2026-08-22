package docker

import (
	"context"
	"fmt"
	"log/slog"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// Sweep removes orphaned containers (architecture §10.6): every container carrying the
// lexicode.instance label whose run no longer exists or is terminal is force-removed together
// with its volumes. Containers whose runs are non-terminal are left alone — those are the ones
// boot reconciliation reattaches. Returns how many containers were removed.
//
// Sweep runs on module Start and hourly after that; tests call it directly.
func (s *Sandbox) Sweep(ctx context.Context) (int, error) {
	if s.runState == nil {
		return 0, fmt.Errorf("docker: sweeper has no run-state lookup; refusing to guess")
	}

	args := filters.NewArgs(filters.Arg("label", labelInstance))
	list, err := s.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return 0, fmt.Errorf("docker: listing labelled containers: %w", err)
	}

	removed := 0
	for _, c := range list {
		runID := c.Labels[labelRun]
		orphan := false
		if runID == "" {
			// Labelled as ours but with no run at all: nothing can ever own it again.
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
