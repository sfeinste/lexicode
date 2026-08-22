package credentials

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// newSecrets is a migrated store plus an open secret store on a temp dir, with one owner user
// for created_by.
func newSecrets(t *testing.T) (*secrets.Store, string) {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "s19.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := domain.Now()
	u := domain.User{ID: domain.NewID(), Email: "ada@example.com", DisplayName: "Ada",
		PasswordHash: "x", Role: domain.RoleOwner, AvatarColor: "#7c5cff", CreatedAt: now}
	if err := st.Users().Create(ctx, &u); err != nil {
		t.Fatal(err)
	}
	sec, err := secrets.Open(secrets.Options{
		Store: st, KeyPath: filepath.Join(dir, "master.key"), Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sec, u.ID
}

func TestOAuthSourceLifecycle(t *testing.T) {
	sec, userID := newSecrets(t)
	ctx := context.Background()
	src := &OAuthSource{sec: sec}

	if src.ID() != "oauth-token" {
		t.Errorf("ID = %q, want oauth-token", src.ID())
	}

	// Unconfigured: Health and AgentEnv both fail with the actionable message.
	if err := src.Health(ctx); err == nil || !strings.Contains(err.Error(), "claude setup-token") {
		t.Errorf("unconfigured Health = %v, want the setup-token instruction", err)
	}
	if _, err := src.AgentEnv(ctx, "proj"); err == nil {
		t.Error("unconfigured AgentEnv succeeded")
	}

	// A malformed value: Health names the expected shape without echoing the value.
	if _, _, err := sec.Set(ctx, secrets.SetInput{
		Scope: domain.SecretScopeWorkspace, Name: OAuthSecretName,
		Value: "ghp_this-is-a-github-pat", CreatedBy: userID,
	}); err != nil {
		t.Fatal(err)
	}
	err := src.Health(ctx)
	if err == nil || !strings.Contains(err.Error(), "sk-ant-") {
		t.Errorf("malformed Health = %v, want a shape hint", err)
	}
	if err != nil && strings.Contains(err.Error(), "ghp_") {
		t.Errorf("Health leaked the stored value: %v", err)
	}

	// A real-shaped token: healthy, and AgentEnv yields exactly the one variable.
	if _, _, err := sec.Set(ctx, secrets.SetInput{
		Scope: domain.SecretScopeWorkspace, Name: OAuthSecretName,
		Value: "sk-ant-oat01-t0ken-value\n", CreatedBy: userID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := src.Health(ctx); err != nil {
		t.Errorf("configured Health = %v, want nil", err)
	}
	env, err := src.AgentEnv(ctx, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 1 || env[OAuthSecretName] != "sk-ant-oat01-t0ken-value" {
		t.Errorf("AgentEnv = %v, want the trimmed token under %s", env, OAuthSecretName)
	}
}

func TestEnvSourceFallbackOrder(t *testing.T) {
	ctx := context.Background()
	fake := map[string]string{}
	src := &EnvSource{lookup: func(k string) (string, bool) { v, ok := fake[k]; return v, ok }}

	if src.ID() != "env" {
		t.Errorf("ID = %q, want env", src.ID())
	}
	if err := src.Health(ctx); err == nil || !errors.Is(err, errNoEnvCredential) {
		t.Errorf("empty env Health = %v, want errNoEnvCredential", err)
	}

	fake[EnvAPIKeyName] = "sk-ant-api03-key"
	env, err := src.AgentEnv(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 1 || env[EnvAPIKeyName] != "sk-ant-api03-key" {
		t.Errorf("API-key-only env = %v", env)
	}

	// The OAuth token wins when both are set — never two competing credentials.
	fake[OAuthSecretName] = "sk-ant-oat01-tok"
	env, err = src.AgentEnv(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 1 || env[OAuthSecretName] != "sk-ant-oat01-tok" {
		t.Errorf("both-set env = %v, want only the OAuth token", env)
	}
	if err := src.Health(ctx); err != nil {
		t.Errorf("Health with credentials = %v", err)
	}
}
