package ports

import "context"

// EventSource turns something that happens in the world into a normalized domain event: the
// GitHub poller, the cron source, and later webhooks or a filesystem watcher (architecture §7).
//
// Method set: contracts §2.1, transcribed in story S25.
type EventSource interface {
	// ID is the stable identifier a trigger stores, e.g. "github.poll". It must be unique across
	// registered event sources.
	ID() string
}

// ForgeProvider lives in forge.go (contracts §2.2, transcribed in story S14).

// Sandbox lives in sandbox.go (contracts §2.3, transcribed in story S17).

// AgentRuntime is an agent CLI or SDK that can be launched inside a sandbox instance and streams
// its work back as activities.
//
// Method set: contracts §2.4, transcribed in story S20.
type AgentRuntime interface {
	// ID is the stable identifier, e.g. "claude-code". It must be unique across registered
	// runtimes.
	ID() string
}

// TriggerAction is one THEN verb of the trigger engine: run_agent, create_ticket, move_ticket,
// post_comment, notify (architecture §8).
//
// Method set: contracts §2.5, transcribed in story S28.
type TriggerAction interface {
	// ID is the stable identifier a trigger stores, e.g. "run_agent". It must be unique across
	// registered actions.
	ID() string
}

// ContextProvider contributes context to an agent run — project facts, wiki pages, repository
// files, the ticket — and is the reason the Context panel can explain every run.
//
// Method set: contracts §2.6, transcribed in story S34.
type ContextProvider interface {
	// ID is the stable identifier, e.g. "wiki". It must be unique across registered providers.
	ID() string
}

// Notifier delivers a notification through one channel. V1 ships in-app only; Slack, email and
// push are the extension this port buys.
//
// Method set: contracts §2.7, transcribed in story S36.
type Notifier interface {
	// ID is the stable identifier, e.g. "inapp". It must be unique across registered notifiers.
	ID() string
}

// CredentialSource supplies the environment an agent run needs to authenticate — an OAuth token,
// an API key from the secret store, or the operator's own environment.
//
// Method set: contracts §2.7, transcribed in story S19. V1 ships two implementations in
// internal/module/credentials: "oauth-token" (D-5: the pasted `claude setup-token` output,
// stored in the encrypted secret store) and "env" (the orchestrator's own environment, as a
// fallback for development setups).
type CredentialSource interface {
	// ID is the stable identifier, e.g. "oauth-token". It must be unique across registered
	// credential sources.
	ID() string
	// AgentEnv returns the environment variables a run's container needs to authenticate
	// against the model provider, e.g. {"CLAUDE_CODE_OAUTH_TOKEN": "..."}. The values are
	// secrets: callers merge them into SandboxSpec.Env and register them with their
	// redactor; they must never be logged or serialized anywhere else.
	AgentEnv(ctx context.Context, projectID string) (map[string]string, error)
	// Health reports whether this source can currently supply credentials. The error is
	// surfaced verbatim in the settings UI, so it must name the fix (e.g. "run `claude
	// setup-token` and paste the result into Settings"), never contain a credential value.
	Health(ctx context.Context) error
}
