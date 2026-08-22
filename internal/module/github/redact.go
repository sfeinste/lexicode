package github

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// redactedPlaceholder replaces every registered secret in log output.
const redactedPlaceholder = "[REDACTED]"

// minSecretLength guards against registering a string so short that redaction would shred
// ordinary log text ("a" would blank half the alphabet).
const minSecretLength = 4

// Redactor holds the secrets that must never appear in a log line. The forge registers every
// token it is handed (there is exactly one hot spot: CloneURL embeds the token in its result,
// and a clone URL is exactly the kind of thing someone logs while debugging), and the module's
// slog handler runs every message and attribute through Clean.
type Redactor struct {
	mu      sync.RWMutex
	secrets []string
}

// Add registers a secret. Empty and very short strings are ignored; duplicates are collapsed.
func (r *Redactor) Add(secret string) {
	if len(secret) < minSecretLength {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.secrets {
		if s == secret {
			return
		}
	}
	r.secrets = append(r.secrets, secret)
}

// Clean replaces every registered secret in s with the redaction placeholder.
func (r *Redactor) Clean(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, redactedPlaceholder)
	}
	return s
}

// redactingHandler is a slog.Handler that cleans the message and every attribute value before
// delegating. It is the module-wide guarantee behind "the clone URL must never be logged":
// even a log call that does include a token emits the placeholder instead.
type redactingHandler struct {
	inner    slog.Handler
	redactor *Redactor
}

// newRedactingHandler wraps inner so that every record passes through the redactor.
func newRedactingHandler(inner slog.Handler, r *Redactor) slog.Handler {
	return &redactingHandler{inner: inner, redactor: r}
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, rec slog.Record) error {
	clean := slog.NewRecord(rec.Time, rec.Level, h.redactor.Clean(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(h.cleanAttr(a))
		return true
	})
	return h.inner.Handle(ctx, clean)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		cleaned[i] = h.cleanAttr(a)
	}
	return &redactingHandler{inner: h.inner.WithAttrs(cleaned), redactor: h.redactor}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name), redactor: h.redactor}
}

// cleanAttr redacts one attribute. Strings are cleaned in place; groups recurse; every other
// kind that could carry a secret in its rendering (errors, stringers, any) is rendered to a
// string and cleaned — losing the value's type in the log is a fair price for never leaking it.
func (h *redactingHandler) cleanAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.redactor.Clean(v.String()))
	case slog.KindGroup:
		group := v.Group()
		cleaned := make([]any, 0, len(group))
		for _, ga := range group {
			cleaned = append(cleaned, h.cleanAttr(ga))
		}
		return slog.Group(a.Key, cleaned...)
	case slog.KindAny:
		return slog.String(a.Key, h.redactor.Clean(fmt.Sprint(v.Any())))
	default:
		// Numbers, bools, times, durations cannot contain a token substring.
		return slog.Attr{Key: a.Key, Value: v}
	}
}
