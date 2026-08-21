package ports

// EventSource turns something that happens in the world into a normalized domain event: the
// GitHub poller, the cron source, and later webhooks or a filesystem watcher (architecture §7).
//
// Method set: contracts §2.1, transcribed in story S25.
type EventSource interface {
	// ID is the stable identifier a trigger stores, e.g. "github.poll". It must be unique across
	// registered event sources.
	ID() string
}

// ForgeProvider is a git host: pull requests, reviews, comments, checks and clone URLs.
// It deliberately has no Merge, no ForcePush and no Approve — brief D6 is implemented as an
// absent capability, not as a permission check.
//
// Method set: contracts §2.2, transcribed in story S14.
type ForgeProvider interface {
	// ID is the stable identifier, e.g. "github". It must be unique across registered forges.
	ID() string
}

// Sandbox is an execution substrate for a run: it prepares an isolated workspace and hands back
// an instance that commands can be executed in.
//
// Method set: contracts §2.3, transcribed in story S17.
type Sandbox interface {
	// ID is the stable identifier, e.g. "docker". It must be unique across registered sandboxes.
	ID() string
}

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
// Method set: contracts §2.7, transcribed in story S19.
type CredentialSource interface {
	// ID is the stable identifier, e.g. "oauth-token". It must be unique across registered
	// credential sources.
	ID() string
}
