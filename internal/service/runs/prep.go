// prep.go is the S19 workspace-preparation builder: given a project, repo, agent, ticket and
// run, produce the complete ports.SandboxSpec that Sandbox.Prepare (S17) consumes. The S22
// scheduler is the caller; nothing here talks to Docker.
//
// Secret handling: this file is one of the sanctioned in-process readers of stored secret
// values (see internal/kernel/secrets — "container env building (S19)"). Values flow into
// SandboxSpec.Env and into Prep.SecretValues (for redactor registration) and nowhere else;
// no HTTP handler calls into this file with a response writer in hand.
package runs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	kernelsecrets "github.com/spruce/lexicode/internal/kernel/secrets"
)

// oauthEnvVar and apiKeyEnvVar are the two credential variables a CredentialSource may
// supply (contracts §2.7; the string doubles as module/credentials.OAuthSecretName — the
// packages must not import each other, the protocol is the variable name). The generic
// secret-injection pass skips both names: credentials reach the container through the
// credential source, exactly once, never twice via a same-named stored secret.
const (
	oauthEnvVar  = "CLAUDE_CODE_OAUTH_TOKEN"
	apiKeyEnvVar = "ANTHROPIC_API_KEY"
)

// workspacePath prefixes the in-container locations of the files the builder materializes
// (contracts §3.1: /workspace is the workspace root, and Files paths are relative to it).
const (
	settingsPath       = ".claude/settings.json"
	mcpConfigPath      = ".lexicode/mcp.json"
	promptPath         = ".lexicode/prompt.md"
	commitTemplatePath = ".lexicode/commit-template.txt"
	commitHookPath     = ".lexicode/hooks/commit-msg"
)

// PlaceholderRunToken is written into .lexicode/mcp.json when the caller supplies no token —
// pre-S22 callers only; the scheduler mints real tokens via the MCP server (S21). It is
// obviously fake on purpose.
const PlaceholderRunToken = "pending-s21-run-token"

// The MCP endpoint's origin as seen from a container (S21's reachability decision; the full
// story is internal/service/mcp/doc.go). The endpoint is served on the S18 egress-proxy
// listener, so:
//
//   - none/allowlist runs, which live on the internal network and cannot reach the host at
//     all, dial the relay container by name — every byte it forwards lands on that listener,
//     and the proxy serves /mcp/* itself instead of dialing upstream. The two constants
//     mirror module/docker's relayContainerName/relayPort; the packages must not import each
//     other (service → module is a forbidden edge), so, like the OAuth env-var name in this
//     file, the string IS the protocol.
//   - open runs ride the default bridge and dial host.docker.internal:<proxy port> — the
//     same listener from the other side. The port is config, so the wiring site (S22)
//     supplies Builder.MCPBaseURL; the portless default below keeps fixture tests honest.
const (
	egressMCPBaseURL  = "http://lexicode-egress:3128"
	defaultMCPBaseURL = "http://host.docker.internal"
)

// Builder assembles SandboxSpecs. Every dependency is a narrow function so the S22 scheduler,
// the docker-tagged tests and the unit tests wire exactly what they mean.
type Builder struct {
	// Secrets reads stored secret values — repo token, project and workspace secrets. May be
	// nil when the repo has no stored token and no secrets are expected (fixture tests).
	Secrets *kernelsecrets.Store
	// Forge resolves the repo's forge provider by ID (kernel.Forge).
	Forge func(id string) (ports.ForgeProvider, error)
	// Credential resolves a credential source by ID (kernel.CredentialSource).
	Credential func(id string) (ports.CredentialSource, error)
	// SourceOrder is the credential preference order; empty means oauth-token then env
	// (D-5: the pasted setup-token is the product path, the environment is the fallback).
	SourceOrder []string
	// ProxyEnv returns the S18 egress-proxy environment for a registered run. Nil, or a
	// false second return, means no proxy vars are merged (an `open` run never calls it).
	ProxyEnv func(runID string) (map[string]string, bool)
	// BranchTaken reports whether a branch name is already claimed for this project — the
	// store's Runs().BranchInUse, optionally composed with a remote check. Nil skips
	// collision checking.
	BranchTaken func(ctx context.Context, projectID, branch string) (bool, error)
	// MCPBaseURL is the MCP endpoint origin for `open` runs (the bridge network):
	// "http://host.docker.internal:<proxy port>", supplied by the wiring site because the
	// port is config. Empty means defaultMCPBaseURL. Proxied runs (none/allowlist) ignore
	// it — their origin is the egress relay, which is fixed (see egressMCPBaseURL).
	MCPBaseURL string
}

// PrepInput is everything Build reads. The caller (S22) loads the rows; the builder never
// touches the database except through the funcs on Builder.
type PrepInput struct {
	Workspace domain.WorkspaceSettings
	Project   domain.Project
	Repo      domain.Repo
	Agent     domain.Agent
	Ticket    *domain.Ticket // nil for a ticketless run
	Run       domain.Run
	// RunToken is the MCP run token for .lexicode/mcp.json; empty writes the S21
	// placeholder.
	RunToken string
}

// Prep is Build's result.
type Prep struct {
	Spec ports.SandboxSpec
	// Branch is the run branch the spec creates; the caller persists it to runs.branch —
	// that row is what makes the collision check see it.
	Branch string
	// SecretValues is every secret that went into the spec — tokens, injected secret
	// values, the credential-bearing clone URL. The caller registers them with the run's
	// Redactor (and the forge module's log redactor) before anything is executed. They must
	// never be logged or serialized.
	SecretValues []string
}

// Build assembles the SandboxSpec for one run.
func (b *Builder) Build(ctx context.Context, in PrepInput) (Prep, error) {
	var prep Prep
	var secretValues []string

	// ---- branch name: deterministic template render, then collision suffix ----
	template := stringOr(in.Repo.BranchTemplate, in.Workspace.DefaultBranchTemplate)
	var ticketKey, ticketTitle string
	if in.Ticket != nil {
		ticketKey, ticketTitle = in.Ticket.Key, in.Ticket.Title
	}
	base := branchName(template, in.Agent.Name, ticketKey, ticketTitle, in.Run.Seq)
	if base == "" {
		return prep, fmt.Errorf("branch template %q rendered an empty branch name", template)
	}
	var taken func(context.Context, string) (bool, error)
	if b.BranchTaken != nil {
		projectID := in.Project.ID
		taken = func(ctx context.Context, name string) (bool, error) {
			return b.BranchTaken(ctx, projectID, name)
		}
	}
	branch, err := uniqueBranch(ctx, base, taken)
	if err != nil {
		return prep, err
	}

	// ---- clone spec: authenticated URL from the forge, base branch, agent identity ----
	baseBranch := stringOr(in.Repo.DefaultBranch, in.Workspace.DefaultBranch)
	forge, err := b.Forge(in.Repo.Provider)
	if err != nil {
		return prep, err
	}
	var creds ports.Creds
	if in.Repo.TokenSecretID != nil {
		token, err := b.Secrets.Get(ctx, *in.Repo.TokenSecretID)
		if err != nil {
			return prep, err
		}
		creds.Token = token
		secretValues = append(secretValues, token)
	}
	cloneURL, err := forge.CloneURL(ctx, creds, in.Repo.Ref())
	if err != nil {
		return prep, err
	}
	secretValues = append(secretValues, cloneURL)

	// ---- env assembly ----
	env := map[string]string{}
	// Stored secrets, workspace scope first so a project-scope name overrides it. The two
	// credential variable names are reserved (see oauthEnvVar above).
	if b.Secrets != nil {
		for _, scope := range []struct {
			scope     domain.SecretScope
			projectID string
		}{
			{domain.SecretScopeWorkspace, ""},
			{domain.SecretScopeProject, in.Project.ID},
		} {
			infos, err := b.Secrets.List(ctx, scope.scope, scope.projectID)
			if err != nil {
				return prep, err
			}
			for _, info := range infos {
				if info.Name == oauthEnvVar || info.Name == apiKeyEnvVar {
					continue
				}
				value, err := b.Secrets.Get(ctx, info.ID)
				if err != nil {
					return prep, err
				}
				env[info.Name] = value
				secretValues = append(secretValues, value)
			}
		}
	}

	// Git identity (D-9): commits attribute to the agent whichever way git resolves it —
	// the repository-local config from CloneSpec, or these variables for tools that bypass
	// it.
	env["GIT_AUTHOR_NAME"] = in.Agent.GitAuthorName
	env["GIT_AUTHOR_EMAIL"] = in.Agent.GitAuthorEmail
	env["GIT_COMMITTER_NAME"] = in.Agent.GitAuthorName
	env["GIT_COMMITTER_EMAIL"] = in.Agent.GitAuthorEmail

	// Commit trailer (D-9): a commit-msg hook appends `Lexicode-Run: <id>` mechanically —
	// `git commit -m` included — and commit.template carries it into editor-composed
	// messages. Both are wired through git's environment-config, which every git process in
	// the container sees. Note: this pins core.hooksPath for the whole container, which is
	// deliberate — repo-managed hook managers (husky et al.) do not run inside the sandbox.
	env["GIT_CONFIG_COUNT"] = "2"
	env["GIT_CONFIG_KEY_0"] = "commit.template"
	env["GIT_CONFIG_VALUE_0"] = "/workspace/" + commitTemplatePath
	env["GIT_CONFIG_KEY_1"] = "core.hooksPath"
	env["GIT_CONFIG_VALUE_1"] = "/workspace/" + strings.TrimSuffix(commitHookPath, "/commit-msg")

	env["LEXICODE_RUN_ID"] = in.Run.ID

	// Model credentials: first healthy source wins, and its variables override any stored
	// secret — the credential source is the single authority for them.
	credEnv, err := b.credentialEnv(ctx, in.Project.ID)
	if err != nil {
		return prep, err
	}
	for k, v := range credEnv {
		env[k] = v
		secretValues = append(secretValues, v)
	}

	// ---- network policy and proxy env (S18): restrictive modes egress via the proxy ----
	mode := ports.NetworkMode(stringOr(in.Repo.NetworkPolicy, in.Workspace.DefaultNetworkPolicy))
	network := ports.NetworkPolicy{Mode: mode}
	if mode == ports.NetworkAllowlist {
		network.Allow = append(network.Allow, in.Repo.NetworkAllowlist...)
	}
	if mode != ports.NetworkOpen && b.ProxyEnv != nil {
		if proxyEnv, ok := b.ProxyEnv(in.Run.ID); ok {
			for k, v := range proxyEnv {
				env[k] = v
			}
		}
	}

	// ---- files ----
	settings, err := claudeSettings(in.Agent.Permissions)
	if err != nil {
		return prep, err
	}
	runToken := in.RunToken
	if runToken == "" {
		runToken = PlaceholderRunToken
	}
	// The MCP origin depends on which network the container will be on (see the constants'
	// comment): proxied runs dial the relay, open runs dial the host alias.
	mcpBase := egressMCPBaseURL
	if mode == ports.NetworkOpen || mode == "" {
		mcpBase = b.MCPBaseURL
		if mcpBase == "" {
			mcpBase = defaultMCPBaseURL
		}
	}
	trailer := "Lexicode-Run: " + in.Run.ID
	files := map[string][]byte{
		settingsPath:       settings,
		mcpConfigPath:      mcpJSON(strings.TrimRight(mcpBase, "/") + "/mcp/" + runToken),
		promptPath:         promptFile(in.Run.Prompt),
		commitTemplatePath: []byte("\n\n" + trailer + "\n"),
		commitHookPath:     commitMsgHook(trailer),
	}

	prep = Prep{
		Spec: ports.SandboxSpec{
			RunID:     in.Run.ID,
			ProjectID: in.Project.ID,
			Image:     stringOr(in.Repo.ImageRef, ""),
			Clone: ports.CloneSpec{
				URL:       cloneURL,
				Ref:       baseBranch,
				Branch:    branch,
				UserName:  in.Agent.GitAuthorName,
				UserEmail: in.Agent.GitAuthorEmail,
			},
			SetupScript: in.Repo.SetupScript,
			Env:         env,
			Files:       files,
			Network:     network,
			Labels:      map[string]string{"lexicode.agent": in.Agent.ID},
			Limits: ports.ResourceLimits{
				WallClock: time.Duration(in.Agent.MaxWallClockSeconds) * time.Second,
			},
		},
		Branch:       branch,
		SecretValues: secretValues,
	}
	return prep, nil
}

// credentialEnv walks the source order and returns the first healthy source's variables. When
// none is healthy the error names every source's reason — the run fails before a container
// exists, with the fix on the surface.
func (b *Builder) credentialEnv(ctx context.Context, projectID string) (map[string]string, error) {
	order := b.SourceOrder
	if len(order) == 0 {
		order = []string{"oauth-token", "env"}
	}
	var reasons []string
	for _, id := range order {
		src, err := b.Credential(id)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("%s: not registered", id))
			continue
		}
		if err := src.Health(ctx); err != nil {
			reasons = append(reasons, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		env, err := src.AgentEnv(ctx, projectID)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		return env, nil
	}
	return nil, errors.New("no credential source can supply this run: " + strings.Join(reasons, "; "))
}

// mcpJSON is the container's MCP client config (contracts §3.1: --mcp-config
// /workspace/.lexicode/mcp.json). One server, reached over HTTP through the egress proxy.
func mcpJSON(url string) []byte {
	// Built by hand rather than json.Marshal so the file is byte-stable and readable in the
	// container. The URL has no characters needing JSON escaping by construction.
	return []byte(`{
  "mcpServers": {
    "lexicode": {
      "type": "http",
      "url": "` + url + `"
    }
  }
}
`)
}

// promptFile is /workspace/.lexicode/prompt.md. Prompt assembly is S22's; until then the run's
// snapshot prompt (possibly empty) rides in a placeholder document so the container protocol
// path (contracts §3.1) is stable now.
func promptFile(prompt string) []byte {
	if strings.TrimSpace(prompt) != "" {
		return []byte(prompt)
	}
	return []byte("# Lexicode run\n\nPrompt assembly arrives with story S22; this placeholder keeps\nthe container protocol path stable (contracts §3.1).\n")
}

// commitMsgHook appends the run trailer to every commit message, `git commit -m` included.
// git skips hooks without the executable bit; the docker adapter marks shebang files 0755
// when it materializes them.
func commitMsgHook(trailer string) []byte {
	return []byte(`#!/bin/sh
# Lexicode (D-9): every commit made in this workspace carries the run trailer.
exec git interpret-trailers --if-exists doNothing --trailer "` + trailer + `" --in-place "$1"
`)
}

// stringOr resolves the settings-inheritance pattern: a nil or empty project-level value
// falls back to the workspace default.
func stringOr(override *string, fallback string) string {
	if override != nil && *override != "" {
		return *override
	}
	return fallback
}
