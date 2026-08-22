// Package credentials is the workspace credentials API service (story S19, decision D-5):
// the settings surface for the Claude Code OAuth token.
//
// The token is a workspace-scope secret and follows D-16: a request body may carry a value
// in, no response ever carries one out. This service works entirely on secret metadata and
// on CredentialSource.Health — the value itself is read only by the credentials module
// (internal/module/credentials) when a run's environment is assembled.
package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/ports"
	kernelsecrets "github.com/spruce/lexicode/internal/kernel/secrets"
)

// defaultSecretName mirrors module/credentials.OAuthSecretName; cmd/lexicode passes the
// module's constant so the two can never drift silently in production wiring.
const defaultSecretName = "CLAUDE_CODE_OAUTH_TOKEN"

// credentialsFile is where the Claude Code CLI stores its login on Linux. On macOS the CLI
// uses the system Keychain instead — there is no file to read — which is why the import
// endpoint is Linux-only (D-5).
const credentialsFile = ".claude/.credentials.json"

// maxTokenBytes bounds a pasted token. setup-token output is ~100 bytes; this is headroom.
const maxTokenBytes = 4096

// Service is the credentials service. Construct with New.
type Service struct {
	sec        *kernelsecrets.Store
	audit      *audit.Writer
	logger     *slog.Logger
	oauth      ports.CredentialSource
	env        ports.CredentialSource
	secretName string
	goos       string
	home       func() (string, error)
}

// Options configures New.
type Options struct {
	// Secrets is the encrypted secret store (metadata operations and value-in writes only).
	// Required.
	Secrets *kernelsecrets.Store
	// Audit is the audit-log writer. Required — every mutation writes an entry.
	Audit *audit.Writer
	// Logger receives failure lines. Nil means slog.Default().
	Logger *slog.Logger
	// OAuth is the "oauth-token" credential source, for health checks. Required.
	OAuth ports.CredentialSource
	// Env is the "env" fallback source, for health checks. Required.
	Env ports.CredentialSource
	// SecretName overrides the workspace secret the token is stored under. Empty means the
	// conventional CLAUDE_CODE_OAUTH_TOKEN; cmd/lexicode passes
	// module/credentials.OAuthSecretName.
	SecretName string
	// GOOS overrides runtime.GOOS in tests. The import endpoint checks it at request time.
	GOOS string
	// Home overrides os.UserHomeDir in tests.
	Home func() (string, error)
}

// New builds the service.
func New(opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	name := opts.SecretName
	if name == "" {
		name = defaultSecretName
	}
	goos := opts.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	home := opts.Home
	if home == nil {
		home = os.UserHomeDir
	}
	return &Service{
		sec: opts.Secrets, audit: opts.Audit, logger: logger,
		oauth: opts.OAuth, env: opts.Env,
		secretName: name, goos: goos, home: home,
	}
}

// Status is what the settings screen renders: configuration and health, never a value.
type Status struct {
	OAuthToken SourceStatus `json:"oauth_token"`
	Env        SourceStatus `json:"env"`
	Import     ImportStatus `json:"import"`
}

// SourceStatus is one credential source's health.
type SourceStatus struct {
	Configured bool `json:"configured"`
	Healthy    bool `json:"healthy"`
	// Message is the source's Health error, rendered verbatim in settings. Empty when
	// healthy.
	Message string `json:"message"`
}

// ImportStatus says whether the Linux import path is offered.
type ImportStatus struct {
	Available bool   `json:"available"`
	Path      string `json:"path"` // "~/.claude/.credentials.json", for the button label
}

// Status reports both sources' health. Never errors on an unhealthy source — unhealthy is a
// state to render, not a failure.
func (s *Service) Status(ctx context.Context) (Status, error) {
	configured, _, err := s.findSecret(ctx)
	if err != nil {
		return Status{}, err
	}
	st := Status{
		OAuthToken: SourceStatus{Configured: configured, Healthy: true},
		Env:        SourceStatus{Healthy: true},
		Import:     ImportStatus{Available: s.goos == "linux", Path: "~/" + credentialsFile},
	}
	if err := s.oauth.Health(ctx); err != nil {
		st.OAuthToken.Healthy = false
		st.OAuthToken.Message = err.Error()
	}
	if err := s.env.Health(ctx); err != nil {
		st.Env.Healthy = false
		st.Env.Message = err.Error()
	}
	// The env source has no "configured" act of its own; configured follows healthy.
	st.Env.Configured = st.Env.Healthy
	return st, nil
}

// SetToken stores the pasted `claude setup-token` output as the workspace secret.
func (s *Service) SetToken(ctx context.Context, token, userID string) (Status, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Status{}, &ValidationError{Field: "token", Message: "Paste the output of `claude setup-token`."}
	}
	if len(token) > maxTokenBytes {
		return Status{}, &ValidationError{Field: "token", Message: "That is too long to be a token."}
	}
	if strings.ContainsAny(token, " \t\r\n") {
		return Status{}, &ValidationError{Field: "token",
			Message: "A token has no spaces — paste exactly what `claude setup-token` printed."}
	}
	if _, _, err := s.sec.Set(ctx, kernelsecrets.SetInput{
		Scope: domain.SecretScopeWorkspace, Name: s.secretName, Value: token, CreatedBy: userID,
	}); err != nil {
		return Status{}, err
	}
	if err := s.audit.Write(ctx, "credentials.oauth_token.set",
		audit.Target{Kind: "workspace", ID: "workspace"}, nil,
		map[string]any{"secret_name": s.secretName}); err != nil {
		return Status{}, err
	}
	return s.Status(ctx)
}

// ClearToken removes the stored token.
func (s *Service) ClearToken(ctx context.Context) (Status, error) {
	found, id, err := s.findSecret(ctx)
	if err != nil {
		return Status{}, err
	}
	if !found {
		return Status{}, ErrNotConfigured
	}
	if _, err := s.sec.Delete(ctx, id); err != nil {
		return Status{}, err
	}
	if err := s.audit.Write(ctx, "credentials.oauth_token.clear",
		audit.Target{Kind: "workspace", ID: "workspace"},
		map[string]any{"secret_name": s.secretName}, nil); err != nil {
		return Status{}, err
	}
	return s.Status(ctx)
}

// ImportToken reads the Claude Code CLI's own stored login and imports its access token. The
// file is read here, at click time, and never again — runs always use the stored secret
// (D-5: "read at setup time only, never per run"). Linux only: on macOS the CLI keeps the
// login in the system Keychain, and there is no file to read.
func (s *Service) ImportToken(ctx context.Context, userID string) (Status, error) {
	if s.goos != "linux" {
		return Status{}, ErrImportUnsupported
	}
	home, err := s.home()
	if err != nil {
		return Status{}, fmt.Errorf("resolving the home directory: %w", err)
	}
	path := filepath.Join(home, filepath.FromSlash(credentialsFile))
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, ErrNoCredentialsFile
	}
	if err != nil {
		return Status{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var doc struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || strings.TrimSpace(doc.ClaudeAiOauth.AccessToken) == "" {
		return Status{}, ErrNoCredentialsFile
	}
	if _, _, err := s.sec.Set(ctx, kernelsecrets.SetInput{
		Scope: domain.SecretScopeWorkspace, Name: s.secretName,
		Value: strings.TrimSpace(doc.ClaudeAiOauth.AccessToken), CreatedBy: userID,
	}); err != nil {
		return Status{}, err
	}
	if err := s.audit.Write(ctx, "credentials.oauth_token.import",
		audit.Target{Kind: "workspace", ID: "workspace"}, nil,
		map[string]any{"secret_name": s.secretName, "source": "~/" + credentialsFile}); err != nil {
		return Status{}, err
	}
	return s.Status(ctx)
}

// findSecret locates the workspace secret by name; metadata only.
func (s *Service) findSecret(ctx context.Context) (found bool, id string, err error) {
	infos, err := s.sec.List(ctx, domain.SecretScopeWorkspace, "")
	if err != nil {
		return false, "", err
	}
	for _, info := range infos {
		if info.Name == s.secretName {
			return true, info.ID, nil
		}
	}
	return false, "", nil
}

// ValidationError carries a field-level problem up to the HTTP layer as a 400.
type ValidationError struct{ Field, Message string }

// Error implements error.
func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// FieldError is the httpx shape of this validation failure.
func (e *ValidationError) FieldError() httpx.FieldError {
	return httpx.FieldError{Field: e.Field, Message: e.Message}
}

// ErrNotConfigured is ClearToken with nothing stored.
var ErrNotConfigured = errors.New("no Claude Code OAuth token is configured")

// ErrImportUnsupported is the import endpoint on a non-Linux host, checked at request time.
var ErrImportUnsupported = errors.New(
	"importing from ~/" + credentialsFile + " is only available when the Lexicode server " +
		"runs on Linux; macOS stores the Claude Code login in the system Keychain, which " +
		"has no file to read — run `claude setup-token` and paste the result instead")

// ErrNoCredentialsFile is a missing or unreadable CLI login file.
var ErrNoCredentialsFile = errors.New(
	"no usable ~/" + credentialsFile + " was found on the server; log in with `claude` " +
		"there first, or run `claude setup-token` and paste the result instead")
