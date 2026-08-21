package auth

import (
	"context"

	"github.com/spruce/lexicode/internal/domain"
)

// Actor is who is acting in a request: the value the audit writer (S06) reads from context to
// attribute a mutation. RequireAuth sets a human actor; later stories set agent, trigger and
// system actors on their own contexts through the same helpers.
type Actor struct {
	Kind domain.ActorKind
	ID   string // user ID for humans, agent ID for agents, trigger ID for triggers
}

// ctxKey is unexported so nothing outside this package can collide with the context values.
type ctxKey int

const (
	actorKey ctxKey = iota
	userKey
)

// WithActor returns a context carrying this actor.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey, a)
}

// ActorFrom returns the actor on the context, if one was set.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey).(Actor)
	return a, ok
}

// withUser returns a context carrying the authenticated user. RequireAuth sets it alongside the
// actor so that handlers and RequireOwner need no second database read.
func withUser(ctx context.Context, u domain.User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// UserFrom returns the authenticated user on the context, if RequireAuth ran.
func UserFrom(ctx context.Context) (domain.User, bool) {
	u, ok := ctx.Value(userKey).(domain.User)
	return u, ok
}
