// Package bootstrap is the repo-connect and project-bootstrap service (story S15, brief §6.3):
// the flow that turns the empty state into a loading state.
//
// Connect verifies an owner/name + PAT against the registered forge, stores the token in the
// secret store (the repos row references it; the token itself is never persisted in the clear
// and never re-readable through the API, D-16), and records head sha/message for the About
// card. Preview assembles, in one payload and without writing anything: open issues as
// checked-by-default importable tickets, detected instruction docs with proposed agent_scope
// values (D-11), detected CI as two pre-filled triggers whose toggles are OFF, suggested
// starter agents, and an Overview draft from the README. Apply creates only the checked
// subset — nothing is created silently.
//
// Idempotency (the acceptance's "re-running the scan does not duplicate"): issues match on
// issue number via the machine-readable marker Apply appends to each imported ticket's
// description (the tickets table has no issue-number column, so the marker in the description
// is the persisted mapping); docs match on wiki_pages.imported_from = the exact repo path;
// triggers and agents match on name. Previously imported items are offered unchecked and
// labeled, and Apply skips them even if a stale client re-sends them.
//
// Access: every route here is project-member guarded, matching the S13 secrets endpoints — the
// closest existing surface that also handles tokens. There is no finer "settings access" role
// in V1's model to gate on.
//
// Imported tickets go straight to the board's backlog column, not to triage: triage exists for
// tickets created by triggers and agents (brief §6.4); a bootstrap import is a human clicking
// a checkbox per ticket, which is exactly the review triage would otherwise provide.
package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// tokenSecretName is the project-scope secret the connect flow writes the PAT into. It is the
// env var name the container will see (data model §2), so it is the conventional one.
const tokenSecretName = "GITHUB_TOKEN"

// DocLister is the bootstrap service's seam onto the capabilities the frozen ForgeProvider
// port does not carry: probing well-known files and listing well-known directories. The
// concrete github adapter satisfies it with extra exported methods (github.Forge.ListDir /
// ReadFileIfExists); cmd/lexicode wires the concrete value in — no type assertion anywhere.
// This is a flagged escalation candidate: a future port revision should fold these into
// ports.ForgeProvider.
type DocLister interface {
	ReadFileIfExists(ctx context.Context, c ports.Creds, r domain.RepoRef, ref, path string) ([]byte, bool, error)
	ListDir(ctx context.Context, c ports.Creds, r domain.RepoRef, ref, path string) ([]domain.DirEntry, error)
}

// ForgeResolver resolves a ForgeProvider by ID at call time — kernel.Forge as a function
// value, so the service never holds the kernel.
type ForgeResolver func(id string) (ports.ForgeProvider, error)

// Service is the bootstrap service. Construct with New.
type Service struct {
	st     *store.Store
	sec    *secrets.Store
	forge  ForgeResolver
	docs   DocLister
	audit  *audit.Writer
	bus    *bus.Bus
	logger *slog.Logger
	now    func() string
}

// Options configures New.
type Options struct {
	// Store is the open, migrated database. Required.
	Store *store.Store
	// Secrets is the kernel secret store the PAT is written into. Required.
	Secrets *secrets.Store
	// Forge resolves the provider ("github") at call time. Required.
	Forge ForgeResolver
	// Docs is the doc-detection seam. Required for Preview/Apply; Connect works without it.
	Docs DocLister
	// Audit is the audit-log writer. Required — every mutation writes an entry.
	Audit *audit.Writer
	// Bus emits internal events for mutations. Nil (tests) skips emission.
	Bus *bus.Bus
	// Logger receives failure lines. Nil means slog.Default().
	Logger *slog.Logger
	// Now overrides the clock in tests. Nil means domain.Now.
	Now func() string
}

// New builds the service.
func New(opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = domain.Now
	}
	return &Service{
		st: opts.Store, sec: opts.Secrets, forge: opts.Forge, docs: opts.Docs,
		audit: opts.Audit, bus: opts.Bus, logger: logger, now: now,
	}
}

// ValidationError carries field-level problems up to the HTTP layer as a 400.
type ValidationError struct{ Fields []httpx.FieldError }

// Error names the invalid fields.
func (e *ValidationError) Error() string {
	names := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		names[i] = f.Field
	}
	return "invalid fields: " + strings.Join(names, ", ")
}

func fieldErr(field, msg string) error {
	return &ValidationError{Fields: []httpx.FieldError{{Field: field, Message: msg}}}
}

// ---------------------------------------------------------------- connect -----

// ConnectInput is POST /projects/{key}/repo.
type ConnectInput struct {
	Owner string
	Name  string
	Token string
}

// Connect verifies the credentials against the forge, persists the PAT as the project's
// GITHUB_TOKEN secret and upserts the repos row with what Verify learned (default branch, head
// sha and message for the About card). Reconnecting an already-connected project replaces the
// token and refreshes the row.
func (s *Service) Connect(ctx context.Context, projectKey string, in ConnectInput) (domain.Repo, error) {
	in.Owner = strings.TrimSpace(in.Owner)
	in.Name = strings.TrimSpace(in.Name)
	in.Token = strings.TrimSpace(in.Token)

	var errs []httpx.FieldError
	if in.Owner == "" {
		errs = append(errs, httpx.FieldError{Field: "owner", Message: "Owner is required."})
	}
	if in.Name == "" {
		errs = append(errs, httpx.FieldError{Field: "name", Message: "Repository name is required."})
	}
	if in.Token == "" {
		errs = append(errs, httpx.FieldError{Field: "token", Message: "A personal access token is required."})
	}
	if len(errs) > 0 {
		return domain.Repo{}, &ValidationError{Fields: errs}
	}

	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return domain.Repo{}, err
	}

	forge, err := s.forge("github")
	if err != nil {
		return domain.Repo{}, err
	}
	ref := domain.RepoRef{Owner: in.Owner, Name: in.Name}
	info, err := forge.Verify(ctx, ports.Creds{Token: in.Token}, ref)
	if err != nil {
		// Verification failures are the user's to fix: surface them on the token field
		// (missing scope) or the repo fields (not found / no access), not as a 500.
		if errors.Is(err, ports.ErrMissingScope) {
			return domain.Repo{}, fieldErr("token", err.Error())
		}
		return domain.Repo{}, fieldErr("name",
			fmt.Sprintf("Could not verify %s: %v", ref, err))
	}

	var creator string
	if u, ok := auth.UserFrom(ctx); ok {
		creator = u.ID
	}
	secInfo, _, err := s.sec.Set(ctx, secrets.SetInput{
		Scope: domain.SecretScopeProject, ProjectID: p.ID,
		Name: tokenSecretName, Value: in.Token, CreatedBy: creator,
	})
	if err != nil {
		return domain.Repo{}, err
	}

	now := s.now()
	branch := info.DefaultBranch
	sha, msg, secretID := info.HeadSHA, info.HeadMessage, secInfo.ID
	rp := domain.Repo{
		ProjectID: p.ID, Provider: forge.ID(), Owner: info.Owner, Name: info.Name,
		DefaultBranch: &branch, TokenSecretID: &secretID,
		ConnectedAt: &now, LastSyncedAt: &now, HeadSHA: &sha, HeadMessage: &msg,
		CreatedAt: now, UpdatedAt: now,
	}

	before, err := s.st.Repos().ByProject(ctx, p.ID)
	action := "repo.connect"
	switch {
	case errors.Is(err, store.ErrNotFound):
		if err := s.st.Repos().Create(ctx, &rp); err != nil {
			return domain.Repo{}, err
		}
	case err != nil:
		return domain.Repo{}, err
	default: // reconnect: keep settings columns, refresh the connection fields
		action = "repo.reconnect"
		rp.BranchTemplate = before.BranchTemplate
		rp.SetupScript = before.SetupScript
		rp.ImageRef = before.ImageRef
		rp.NetworkPolicy = before.NetworkPolicy
		rp.NetworkAllowlist = before.NetworkAllowlist
		rp.CreatedAt = before.CreatedAt
		if err := s.st.Repos().Update(ctx, &rp); err != nil {
			return domain.Repo{}, err
		}
	}

	if err := s.audit.Write(ctx, action,
		audit.Target{Kind: "repo", ID: p.ID, ProjectID: p.ID}, nil, redactRepo(rp)); err != nil {
		return domain.Repo{}, err
	}
	s.emit(ctx, "repo.connected", p, map[string]any{
		"owner": rp.Owner, "name": rp.Name, "default_branch": branch,
	})
	return rp, nil
}

// Status returns the project's repo row, or store.ErrNotFound when none is connected.
func (s *Service) Status(ctx context.Context, projectKey string) (domain.Repo, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return domain.Repo{}, err
	}
	return s.st.Repos().ByProject(ctx, p.ID)
}

// RotateToken replaces the stored repository token with a new one (S37 danger zone), keeping
// everything else about the connection. Verify-then-replace, in that order: the new token is
// proven against the connected repo before the secret is touched, so a bad token is a 400 and
// the old token keeps working — the repo is never left with a broken credential.
func (s *Service) RotateToken(ctx context.Context, projectKey, token string) (domain.Repo, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return domain.Repo{}, fieldErr("token", "A personal access token is required.")
	}
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return domain.Repo{}, err
	}
	rp, err := s.st.Repos().ByProject(ctx, p.ID)
	if err != nil {
		return domain.Repo{}, err
	}
	forge, err := s.forge(rp.Provider)
	if err != nil {
		return domain.Repo{}, err
	}
	ref := domain.RepoRef{Owner: rp.Owner, Name: rp.Name}
	info, err := forge.Verify(ctx, ports.Creds{Token: token}, ref)
	if err != nil {
		if errors.Is(err, ports.ErrMissingScope) {
			return domain.Repo{}, fieldErr("token", err.Error())
		}
		return domain.Repo{}, fieldErr("token",
			fmt.Sprintf("The new token could not access %s: %v — the old token was kept.", ref, err))
	}

	var creator string
	if u, ok := auth.UserFrom(ctx); ok {
		creator = u.ID
	}
	// secrets.Set upserts by (scope, project, name), so this replaces the GITHUB_TOKEN value
	// in place; the repos row's token_secret_id stays valid either way.
	secInfo, _, err := s.sec.Set(ctx, secrets.SetInput{
		Scope: domain.SecretScopeProject, ProjectID: p.ID,
		Name: tokenSecretName, Value: token, CreatedBy: creator,
	})
	if err != nil {
		return domain.Repo{}, err
	}
	now := s.now()
	secretID := secInfo.ID
	sha, msg := info.HeadSHA, info.HeadMessage
	rp.TokenSecretID = &secretID
	rp.HeadSHA, rp.HeadMessage = &sha, &msg
	rp.LastSyncedAt = &now
	rp.UpdatedAt = now
	if err := s.st.Repos().Update(ctx, &rp); err != nil {
		return domain.Repo{}, err
	}
	if err := s.audit.Write(ctx, "repo.token.rotate",
		audit.Target{Kind: "repo", ID: p.ID, ProjectID: p.ID,
			Note: "repository token rotated after verification"},
		nil, redactRepo(rp)); err != nil {
		return domain.Repo{}, err
	}
	s.emit(ctx, "repo.token.rotated", p, map[string]any{"owner": rp.Owner, "name": rp.Name})
	return rp, nil
}

// Disconnect deletes the repos row and the stored token. Everything the bootstrap imported —
// tickets, wiki pages, triggers, agents — stays: they belong to the project, not to the
// connection.
func (s *Service) Disconnect(ctx context.Context, projectKey string) error {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return err
	}
	rp, err := s.st.Repos().ByProject(ctx, p.ID)
	if err != nil {
		return err
	}
	if err := s.st.Repos().Delete(ctx, p.ID); err != nil {
		return err
	}
	if rp.TokenSecretID != nil {
		if _, err := s.sec.Delete(ctx, *rp.TokenSecretID); err != nil &&
			!errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	if err := s.audit.Write(ctx, "repo.disconnect",
		audit.Target{Kind: "repo", ID: p.ID, ProjectID: p.ID}, redactRepo(rp), nil); err != nil {
		return err
	}
	s.emit(ctx, "repo.disconnected", p, map[string]any{"owner": rp.Owner, "name": rp.Name})
	return nil
}

// creds resolves the stored token for a connected project. The plaintext lives only for the
// duration of the forge calls.
func (s *Service) creds(ctx context.Context, rp domain.Repo) (ports.Creds, error) {
	if rp.TokenSecretID == nil {
		return ports.Creds{}, errors.New("bootstrap: the connected repo has no stored token; reconnect the repository")
	}
	token, err := s.sec.Get(ctx, *rp.TokenSecretID)
	if err != nil {
		return ports.Creds{}, err
	}
	return ports.Creds{Token: token}, nil
}

// redactRepo is the audit shape of a repos row: everything except the secret reference, so an
// audit reader learns what changed without a pointer into the secret store.
func redactRepo(rp domain.Repo) map[string]any {
	return map[string]any{
		"project_id": rp.ProjectID, "provider": rp.Provider,
		"owner": rp.Owner, "name": rp.Name,
		"default_branch": rp.DefaultBranch, "head_sha": rp.HeadSHA,
	}
}

// emit publishes an internal event; best-effort after commit, like every service.
func (s *Service) emit(ctx context.Context, kind string, p domain.Project, extra map[string]any) {
	if s.bus == nil {
		return
	}
	payload := map[string]any{
		"project": map[string]string{"id": p.ID, "key": p.Key, "name": p.Name},
	}
	for k, v := range extra {
		payload[k] = v
	}
	raw, _ := json.Marshal(payload)
	pid := p.ID
	e := domain.Event{
		ProjectID: &pid, Kind: kind, SubjectKind: "repo", SubjectID: &pid,
		Payload: raw, OccurredAt: s.now(),
	}
	if a, ok := auth.ActorFrom(ctx); ok {
		e.ActorKind = a.Kind
		if a.ID != "" {
			id := a.ID
			e.ActorID = &id
		}
	}
	if err := s.bus.Emit(ctx, e); err != nil {
		s.logger.Error("bootstrap: emit failed",
			slog.String("kind", kind), slog.String("error", err.Error()))
	}
}
