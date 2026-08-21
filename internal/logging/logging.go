// Package logging builds the process logger: structured JSON into a file under the data
// directory, and a human-readable stream on stderr, both at once (architecture §14).
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// ParseLevel maps a configuration string onto a slog level.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("log level %q is not recognised; use debug, info, warn or error", name)
	}
}

// Options configures Setup.
type Options struct {
	// Level is the minimum level both handlers emit.
	Level slog.Level
	// File is the JSON log file. Its parent directory is created if needed. Empty disables the
	// file handler.
	File string
	// Stderr is where the human-readable stream goes. Nil means os.Stderr.
	Stderr io.Writer
	// AddSource attaches file:line to every record.
	AddSource bool
}

// Setup returns a logger that writes every record to both handlers. The returned closer flushes
// and closes the log file; it is safe to call when no file was opened.
func Setup(opts Options) (*slog.Logger, io.Closer, error) {
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	handlerOpts := &slog.HandlerOptions{Level: opts.Level, AddSource: opts.AddSource}
	handlers := []slog.Handler{slog.NewTextHandler(stderr, handlerOpts)}

	var file *os.File
	if opts.File != "" {
		if err := os.MkdirAll(filepath.Dir(opts.File), 0o700); err != nil {
			return nil, nopCloser{}, fmt.Errorf("create log directory %s: %w", filepath.Dir(opts.File), err)
		}
		f, err := os.OpenFile(opts.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // operator-supplied path
		if err != nil {
			return nil, nopCloser{}, fmt.Errorf("open log file %s: %w", opts.File, err)
		}
		file = f
		handlers = append(handlers, slog.NewJSONHandler(f, handlerOpts))
	}

	logger := slog.New(&fanout{handlers: handlers})
	if file == nil {
		return logger, nopCloser{}, nil
	}
	return logger, file, nil
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// fanout sends every record to every underlying handler. It exists because slog has no built-in
// multi-handler and the product needs machine-readable history and readable console output at the
// same time.
type fanout struct {
	handlers []slog.Handler
}

func (f *fanout) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f *fanout) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range f.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (f *fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return &fanout{handlers: next}
}

func (f *fanout) WithGroup(name string) slog.Handler {
	if name == "" {
		return f
	}
	next := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		next[i] = h.WithGroup(name)
	}
	return &fanout{handlers: next}
}
