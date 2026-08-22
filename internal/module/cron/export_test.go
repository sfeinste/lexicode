package cron

import (
	"context"

	"github.com/spruce/lexicode/internal/kernel/ports"
)

// ScanForTest exposes one deterministic scan pass to the external pipeline test, which
// drives the source with a fake clock instead of the minute loop.
func (s *Source) ScanForTest(ctx context.Context, emit ports.Emit) { s.scan(ctx, emit) }
