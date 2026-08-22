// Package credentials is the credentials module (story S19, decision D-5): the two V1
// implementations of ports.CredentialSource.
//
//   - "oauth-token" — the user runs `claude setup-token` once and pastes the long-lived token
//     into workspace settings; it is stored in the encrypted secret store (workspace scope,
//     name CLAUDE_CODE_OAUTH_TOKEN) and injected into each run's container as
//     CLAUDE_CODE_OAUTH_TOKEN. Provisioned once, read per run — never per keystroke.
//   - "env" — a development fallback that forwards CLAUDE_CODE_OAUTH_TOKEN or
//     ANTHROPIC_API_KEY from the orchestrator's own environment.
//
// This package is one of the sanctioned in-process readers of secret values (D-16): the value
// flows from the secret store into SandboxSpec.Env and nowhere else. Health() never returns a
// value, only whether one is usable and how to fix it when not.
package credentials

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/secrets"
)

const (
	// OAuthSourceID is the CredentialSource ID of the pasted-token source.
	OAuthSourceID = "oauth-token"
	// EnvSourceID is the CredentialSource ID of the orchestrator-environment fallback.
	EnvSourceID = "env"

	// OAuthSecretName is the workspace-scope secret the pasted `claude setup-token` output is
	// stored under. It doubles as the env var name the container sees, which is why it is the
	// conventional one. The workspace prep builder (service/runs) excludes this name from
	// generic workspace-secret injection — the token reaches the container through the
	// credential source, exactly once.
	OAuthSecretName = "CLAUDE_CODE_OAUTH_TOKEN"

	// EnvAPIKeyName is the API-key variable the env source forwards when no OAuth token is
	// set in the orchestrator's environment.
	EnvAPIKeyName = "ANTHROPIC_API_KEY"

	// tokenPrefix is what `claude setup-token` output starts with. Health uses it to catch
	// pasting the wrong thing (an API key, a GitHub PAT, half a token) without ever echoing
	// the value.
	tokenPrefix = "sk-ant-"
)

// moduleName is the kernel module name.
const moduleName = "credentials"

// Options configures New.
type Options struct {
	// Secrets is the encrypted secret store. Nil means "wire from the kernel in Init".
	Secrets *secrets.Store
	// LookupEnv is the env source's reader; nil means os.LookupEnv. Tests inject a fake.
	LookupEnv func(string) (string, bool)
}

// Module registers the two credential sources. It has no background work.
type Module struct {
	oauth *OAuthSource
	env   *EnvSource
}

// New builds the module. Both sources exist immediately so the wiring site can hand them to
// the settings service before Init runs.
func New(opts Options) *Module {
	lookup := opts.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	return &Module{
		oauth: &OAuthSource{sec: opts.Secrets},
		env:   &EnvSource{lookup: lookup},
	}
}

// Name implements kernel.Module.
func (m *Module) Name() string { return moduleName }

// Init implements kernel.Module: wire the secret store if the wiring site did not, and
// register both sources.
func (m *Module) Init(k *kernel.Kernel) error {
	if m.oauth.sec == nil {
		m.oauth.sec = k.Secrets()
	}
	if m.oauth.sec == nil {
		return errors.New("credentials: no secret store; the kernel must be built with Secrets")
	}
	if err := k.RegisterCredentialSource(m.oauth); err != nil {
		return err
	}
	return k.RegisterCredentialSource(m.env)
}

// Start implements kernel.Module. Nothing runs in the background.
func (m *Module) Start(context.Context) error { return nil }

// Stop implements kernel.Module.
func (m *Module) Stop(context.Context) error { return nil }

// OAuth returns the "oauth-token" source, for the wiring site.
func (m *Module) OAuth() *OAuthSource { return m.oauth }

// Env returns the "env" source, for the wiring site.
func (m *Module) Env() *EnvSource { return m.env }

// ---------------------------------------------------------------- oauth-token -----

// OAuthSource is the D-5 credential source: the pasted `claude setup-token` output, stored in
// the encrypted secret store at workspace scope.
type OAuthSource struct {
	sec *secrets.Store
}

// ID implements ports.CredentialSource.
func (s *OAuthSource) ID() string { return OAuthSourceID }

// AgentEnv implements ports.CredentialSource: the stored token as CLAUDE_CODE_OAUTH_TOKEN.
// projectID is unused — the token is a workspace-level credential (D-5).
func (s *OAuthSource) AgentEnv(ctx context.Context, _ string) (map[string]string, error) {
	token, err := s.token(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{OAuthSecretName: token}, nil
}

// Health implements ports.CredentialSource. The error names the fix and never carries the
// value.
func (s *OAuthSource) Health(ctx context.Context) error {
	token, err := s.token(ctx)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(token, tokenPrefix) {
		return fmt.Errorf("the stored token does not look like `claude setup-token` output "+
			"(expected it to start with %q); run `claude setup-token` again and paste the "+
			"full result into Settings", tokenPrefix)
	}
	return nil
}

// token resolves the stored value: find the workspace-scope secret by name, then read it.
func (s *OAuthSource) token(ctx context.Context) (string, error) {
	infos, err := s.sec.List(ctx, domain.SecretScopeWorkspace, "")
	if err != nil {
		return "", fmt.Errorf("credentials: listing workspace secrets: %w", err)
	}
	for _, info := range infos {
		if info.Name != OAuthSecretName {
			continue
		}
		token, err := s.sec.Get(ctx, info.ID)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(token) == "" {
			break
		}
		return strings.TrimSpace(token), nil
	}
	return "", errNoToken
}

// errNoToken is the "not configured yet" state; the settings screen renders it verbatim.
var errNoToken = errors.New(
	"no Claude Code OAuth token is configured; run `claude setup-token` in a terminal and " +
		"paste the result into Settings → Credentials")

// ---------------------------------------------------------------- env -----

// EnvSource forwards credentials from the orchestrator's own environment: the documented
// fallback for development, where the operator already has CLAUDE_CODE_OAUTH_TOKEN or
// ANTHROPIC_API_KEY exported. The variables are read at run-preparation time, never cached.
type EnvSource struct {
	lookup func(string) (string, bool)
}

// ID implements ports.CredentialSource.
func (s *EnvSource) ID() string { return EnvSourceID }

// AgentEnv implements ports.CredentialSource. The OAuth token wins when both are set, so a
// container never receives two competing credentials.
func (s *EnvSource) AgentEnv(context.Context, string) (map[string]string, error) {
	if v, ok := s.lookup(OAuthSecretName); ok && strings.TrimSpace(v) != "" {
		return map[string]string{OAuthSecretName: strings.TrimSpace(v)}, nil
	}
	if v, ok := s.lookup(EnvAPIKeyName); ok && strings.TrimSpace(v) != "" {
		return map[string]string{EnvAPIKeyName: strings.TrimSpace(v)}, nil
	}
	return nil, errNoEnvCredential
}

// Health implements ports.CredentialSource.
func (s *EnvSource) Health(ctx context.Context) error {
	_, err := s.AgentEnv(ctx, "")
	return err
}

// errNoEnvCredential names both variables the env source reads.
var errNoEnvCredential = errors.New(
	"neither CLAUDE_CODE_OAUTH_TOKEN nor ANTHROPIC_API_KEY is set in the server's environment")

var (
	_ ports.CredentialSource = (*OAuthSource)(nil)
	_ ports.CredentialSource = (*EnvSource)(nil)
	_ kernel.Module          = (*Module)(nil)
)
