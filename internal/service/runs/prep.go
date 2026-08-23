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
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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

// Container resource ceilings (§10.3: "resource limits (cpu, memory, pids)"; the sandbox
// applies them to the container's HostConfig — see internal/module/docker's createAndStart).
// There is no workspace setting for them: workspace_settings carries MaxConcurrentContainers
// but no per-container ceiling, so these named constants are the single place the numbers
// live, and a schema field can later override them without moving the policy.
//
// The numbers are sized for the product V1 ships as: one binary on the user's own machine,
// running up to MaxConcurrentContainers of these at once, each holding one Claude Code process
// tree doing git, an install and a build.
//
//   - 2 cores: enough for a `npm ci`/compile step, small enough that the default three
//     concurrent runs cannot monopolize a 4-8 core laptop.
//   - 4 GiB: a Node toolchain plus a test run fits; a leak is stopped by the OOM killer inside
//     the container instead of swapping the host to death.
//   - 512 pids: a fork bomb dies, `make -j` does not.
//
// Zero would mean "no limit" (ports.ResourceLimits) — which is what shipped before this was
// filled in, and is exactly what lets a runaway agent exhaust the host.
const (
	defaultCPUs        = 2.0
	defaultMemoryBytes = 4 << 30 // 4 GiB
	defaultPidsLimit   = 512
)

// minMCPToolTimeout floors MCP_TOOL_TIMEOUT. Claude Code's per-request timer for an HTTP
// MCP server is 60 seconds unless MCP_TOOL_TIMEOUT raises it, and a value that merely equals
// 60s buys nothing; agents may set max_wall_clock_seconds as low as 60, so the floor keeps
// even the smallest budget clear of the timer it exists to defeat.
const minMCPToolTimeout = 5 * time.Minute

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
	// CauseEvent is the event that spawned this run (runs.cause_event_id), when it had one.
	// nil for a manual delegation. It is what tells the builder that the run's subject is a
	// live pull request and which branch that pull request's head is on — see workspaceRefs.
	CauseEvent *domain.Event
	// RunToken is the MCP run token for .lexicode/mcp.json; empty writes the S21
	// placeholder.
	RunToken string
}

// Prep is Build's result.
type Prep struct {
	Spec ports.SandboxSpec
	// Branch is the run branch the spec creates; the caller persists it to runs.branch —
	// that row is what makes the collision check see it. Empty when the run creates no
	// branch of its own: a run reviewing a pull request works on the fetched head and has
	// nothing to push (workspaceRefs).
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

	// ---- what the workspace is cut from ----
	//
	// Two shapes, and the run's SUBJECT decides which. A run about a ticket (or about
	// nothing) gets a fresh branch off the default branch — the S19 default. A run whose
	// subject is a pull request gets that pull request's head branch instead, because the
	// code it was spawned to look at is only on that branch: a reviewer sent to
	// `reviewer/run-12`, cut from `main`, reviews a workspace that does not contain the
	// change. See prCheckout for the branch half of the decision.
	baseBranch := stringOr(in.Repo.DefaultBranch, in.Workspace.DefaultBranch)
	cloneRef, branch, err := b.workspaceRefs(ctx, in, baseBranch)
	if err != nil {
		return prep, err
	}

	// ---- clone spec: authenticated URL from the forge, base branch, agent identity ----
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

	// MCP client timeouts. Claude Code is the MCP *client* here, and its own limits — not
	// the server's — decide how long a blocking tool call may take. Left at their defaults
	// they make ask_human unusable: our server is an HTTP MCP server, and for HTTP servers
	// "each request also times out after 60 seconds by default; set this variable, or the
	// per-server timeout, above 60000 to raise that per-request limit"
	// (https://code.claude.com/docs/en/env-vars, MCP_TOOL_TIMEOUT). A question therefore
	// died at 60s no matter what ceiling the server was willing to wait — which is exactly
	// when S24's escalation notification first tells a human the question exists.
	//
	//   - MCP_TOOL_TIMEOUT — milliseconds; the wall-clock limit on one tool call, and the
	//     value that raises the 60-second per-request timer for HTTP servers.
	//   - CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT — milliseconds; a call whose server sends "no
	//     response and no progress notification for this long" aborts. The default for a
	//     network server is five minutes. The MCP server does now send progress
	//     notifications while an elicitation is pending (see internal/service/mcp), so the
	//     idle window would not trip; raising it as well means a question survives even
	//     against a client that never asked for progress (no progressToken, no
	//     notifications — the spec makes them opt-in).
	//
	// Both are derived from the run's own wall-clock limit rather than hardcoded: the
	// agent's budget is the honest upper bound on how long anything about this run can
	// take, and it is what the MCP server uses as its own elicitation ceiling
	// (mcp.Server.ceilingFor), so client and server abandon a question at the same moment
	// instead of one silently outliving the other.
	toolTimeout := mcpToolTimeout(in.Agent.MaxWallClockSeconds)
	env["MCP_TOOL_TIMEOUT"] = strconv.FormatInt(toolTimeout.Milliseconds(), 10)
	env["CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT"] = strconv.FormatInt(toolTimeout.Milliseconds(), 10)

	// The container holds no repository credential (the sandbox's clone step points `origin`
	// at a tokenless URL as soon as the fetch is done), so any git command that needs one has
	// to fail rather than sit waiting for a username nobody will type. Without this, an
	// agent's `git push` ends in "could not read Username" only because there is no terminal;
	// with it, git says plainly that authentication is unavailable.
	env["GIT_TERMINAL_PROMPT"] = "0"

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
				Ref:       cloneRef,
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
				CPUs:        defaultCPUs,
				MemoryBytes: defaultMemoryBytes,
				Pids:        defaultPidsLimit,
				WallClock:   time.Duration(in.Agent.MaxWallClockSeconds) * time.Second,
			},
		},
		Branch:       branch,
		SecretValues: secretValues,
	}
	return prep, nil
}

// prSubjectPrefix is the loop-protection subject key a pull-request run carries
// ("pr:219"). The trigger engine derives it from the event catalog's SubjectKey template
// (internal/module/github's catalog: every PR-shaped event kind — pull_request,
// pull_request_review, review comments, issue comments on a PR, and check suites attached to
// one — uses "pr:{{pr.number}}"). Service → module is a forbidden import edge, so, as with the
// OAuth variable name above, the string IS the protocol.
const prSubjectPrefix = "pr:"

// workspaceRefs decides what the clone checks out and what branch, if any, it creates.
//
// The default (a ticket run, a manual delegation, anything with no pull-request subject) is
// unchanged from S19: fetch the repository's default branch, then cut a fresh branch from the
// template, collision-suffixed.
//
// When the run's subject is a pull request, the workspace is that pull request's head branch:
//
//   - Ref becomes the head branch, so the code under review IS the workspace. This is the
//     whole defect: a reviewer whose workspace was cut from `main` reviewed `main`.
//   - Branch — the branch the clone step CREATES — is set only for an agent that holds
//     push_branches. That agent's job on a pull request is to change it ("CI failed → fix
//     it", "changes requested → address them"), so it works on a local branch of the same
//     name and the orchestrator's teardown push carries the commits back to the pull
//     request. An agent without the grant (a reviewer) gets no branch at all: the workspace
//     stays on the fetched head, detached, and Prep.Branch is empty.
//
// That empty Branch is a second, independent lock on the thing that must not happen — a
// review run pushing to the pull request author's branch. The first lock is the grant:
// preserveAndPush returns before it does anything when push_branches is false. The second is
// this: with no branch on the run row, the push path has nothing to push to and returns
// early even if the grant is later flipped on. (A third, further down, is the preserve
// script's own refusal to push the default branch.)
//
// No port change was needed: ports.CloneSpec already carries Ref and Branch as separate
// fields, and the docker adapter already treats an empty Branch as "stay on what was
// fetched".
func (b *Builder) workspaceRefs(ctx context.Context, in PrepInput, baseBranch string) (ref, branch string, err error) {
	if head := prCheckout(in.Run, in.CauseEvent); head != "" {
		if in.Agent.Permissions.PushBranches {
			return head, head, nil
		}
		return head, "", nil
	}

	template := stringOr(in.Repo.BranchTemplate, in.Workspace.DefaultBranchTemplate)
	var ticketKey, ticketTitle string
	if in.Ticket != nil {
		ticketKey, ticketTitle = in.Ticket.Key, in.Ticket.Title
	}
	base := branchName(template, in.Agent.Name, ticketKey, ticketTitle, in.Run.Seq)
	if base == "" {
		return "", "", fmt.Errorf("branch template %q rendered an empty branch name", template)
	}
	var taken func(context.Context, string) (bool, error)
	if b.BranchTaken != nil {
		projectID := in.Project.ID
		taken = func(ctx context.Context, name string) (bool, error) {
			return b.BranchTaken(ctx, projectID, name)
		}
	}
	fresh, err := uniqueBranch(ctx, base, taken)
	if err != nil {
		return "", "", err
	}
	return baseBranch, fresh, nil
}

// prCheckout returns the head branch of the pull request this run is about, or "" when the
// run has no pull-request subject or the branch cannot be established.
//
// Both halves have to hold. The subject key says the run is ABOUT a pull request; the causing
// event says WHICH branch that pull request's head is. The event row carries it in a typed
// column (the poller stamps subject_branch on every PR-shaped event), and the normalized
// payload's `pr.branch` is the fallback for an event source that fills only the payload.
//
// A pull-request run whose branch cannot be established falls back to the fresh-branch
// default rather than failing: a workspace on the wrong branch is a bad review, but no
// workspace at all is a failed run, and the `event` context section still tells the agent
// which pull request it is looking at.
func prCheckout(run domain.Run, ev *domain.Event) string {
	if !strings.HasPrefix(run.SubjectKey, prSubjectPrefix) || ev == nil {
		return ""
	}
	if ev.SubjectBranch != nil && strings.TrimSpace(*ev.SubjectBranch) != "" {
		return strings.TrimSpace(*ev.SubjectBranch)
	}
	var payload struct {
		PR struct {
			Branch string `json:"branch"`
		} `json:"pr"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err == nil {
		return strings.TrimSpace(payload.PR.Branch)
	}
	return ""
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

// mcpToolTimeout is the MCP client's per-call limit for a run whose agent allows
// maxWallClockSeconds. Two floors apply. A missing or nonsensical limit falls back to the
// schema default of an hour (the same fallback the scheduler's wallDeadline uses), and the
// result is never below minMCPToolTimeout — a value at or under 60s leaves Claude Code's
// 60-second per-request timer in place, which is the bug this exists to prevent.
func mcpToolTimeout(maxWallClockSeconds int64) time.Duration {
	d := time.Duration(maxWallClockSeconds) * time.Second
	if d <= 0 {
		d = time.Hour
	}
	if d < minMCPToolTimeout {
		d = minMCPToolTimeout
	}
	return d
}

// stringOr resolves the settings-inheritance pattern: a nil or empty project-level value
// falls back to the workspace default.
func stringOr(override *string, fallback string) string {
	if override != nil && *override != "" {
		return *override
	}
	return fallback
}
