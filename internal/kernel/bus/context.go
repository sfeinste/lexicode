package bus

import "context"

// causeRunKey is the context key WithCauseRun stores under. An unexported struct type cannot
// collide with any other package's context keys.
type causeRunKey struct{}

// WithCauseRun marks ctx as executing on behalf of an agent run: every internal event emitted
// under it (Bus.Emit) carries the run as its cause, which is the events.cause_run_id half of the
// causal graph (architecture §6.3). The run service wraps an action's context once; every
// service the action then calls inherits the attribution for free.
func WithCauseRun(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, causeRunKey{}, runID)
}

// CauseRun returns the run ID WithCauseRun stored, if any.
func CauseRun(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(causeRunKey{}).(string)
	return id, ok && id != ""
}
