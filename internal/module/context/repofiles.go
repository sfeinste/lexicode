// repofiles.go is the `repofiles` ContextProvider (architecture §11: priority 40; decision
// D-11): it enumerates the repo's own instruction files — AGENTS.md, CLAUDE.md,
// .cursor/rules/*, .github/copilot-instructions.md — and LISTS them in the context stack
// without injecting them (Injected=false, empty Body, reason exactly "repo file"). Claude
// Code inside the container reads these files off the checkout itself; the listing makes
// that steering visible in the Context panel instead of invisible.
//
// The enumeration goes through the forge API against the repo's default branch — the same
// DocLister path the S15 bootstrap detection uses — because resolution happens at enqueue,
// before any container or checkout exists, and the dry preview never gets a checkout at all.
// One path for both keeps preview/run parity structural.
//
// The provider is deliberately non-fatal: a project with no connected repo, a repo with no
// stored token, or a forge that is down yields an empty listing (logged), never a failed
// enqueue — these items are advisory, and a GitHub outage must not stop runs.
package contextmod

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// DocLister is the same seam the S15 bootstrap detection uses (bootstrap.DocLister),
// declared here because a module may not import internal/service: probing well-known files
// and listing well-known directories through the forge API. The concrete github adapter
// (github.Forge) satisfies it; cmd/lexicode wires the concrete value in.
type DocLister interface {
	ReadFileIfExists(ctx context.Context, c ports.Creds, r domain.RepoRef, ref, path string) ([]byte, bool, error)
	ListDir(ctx context.Context, c ports.Creds, r domain.RepoRef, ref, path string) ([]domain.DirEntry, error)
}

// instructionProbes are the well-known single-file instruction paths, probed in this order.
var instructionProbes = []string{
	"AGENTS.md",
	"CLAUDE.md",
	".github/copilot-instructions.md",
}

// RepoFilesProvider lists the repo's instruction files without injecting them (D-11).
type RepoFilesProvider struct {
	st     *store.Store
	sec    *secrets.Store
	docs   DocLister
	logger *slog.Logger
}

// NewRepoFilesProvider builds the provider. docs may be nil (no forge wired — tests, or a
// build without the github module); the provider then always yields nothing.
func NewRepoFilesProvider(st *store.Store, sec *secrets.Store, docs DocLister, logger *slog.Logger) *RepoFilesProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return &RepoFilesProvider{st: st, sec: sec, docs: docs, logger: logger}
}

// ID implements ports.ContextProvider.
func (p *RepoFilesProvider) ID() string { return "repofiles" }

// Priority implements ports.ContextProvider.
func (p *RepoFilesProvider) Priority() int { return 40 }

// Resolve implements ports.ContextProvider. Every item: SourceKind "repo_file", reason
// "repo file", Injected=false with an empty body (D-11); Tokens estimates the file's size so
// the panel can show what the agent will read on its own.
func (p *RepoFilesProvider) Resolve(ctx context.Context, req ports.ContextRequest) ([]ports.ContextItem, error) {
	if p.docs == nil || p.sec == nil {
		return nil, nil
	}
	repo, err := p.st.Repos().ByProject(ctx, req.ProjectID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil // no repo connected: an empty listing, not an error
		}
		return nil, err
	}
	if repo.TokenSecretID == nil {
		return nil, nil
	}
	token, err := p.sec.Get(ctx, *repo.TokenSecretID)
	if err != nil {
		p.logger.Warn("contextmod: repofiles token read failed",
			slog.String("project", req.ProjectID), slog.String("error", err.Error()))
		return nil, nil
	}
	creds := ports.Creds{Token: token}
	ref := domain.RepoRef{Owner: repo.Owner, Name: repo.Name}
	branch := p.defaultBranch(ctx, repo)

	var out []ports.ContextItem
	for _, probe := range instructionProbes {
		content, ok, err := p.docs.ReadFileIfExists(ctx, creds, ref, branch, probe)
		if err != nil {
			p.logger.Warn("contextmod: repofiles probe failed",
				slog.String("path", probe), slog.String("error", err.Error()))
			continue // advisory listing: a forge hiccup never fails the enqueue
		}
		if ok {
			out = append(out, repoFileItem(probe, content))
		}
	}
	rules, err := p.docs.ListDir(ctx, creds, ref, branch, ".cursor/rules")
	if err != nil {
		p.logger.Warn("contextmod: repofiles .cursor/rules listing failed",
			slog.String("error", err.Error()))
		return out, nil
	}
	for _, e := range rules {
		if e.Type != "file" {
			continue
		}
		content, ok, err := p.docs.ReadFileIfExists(ctx, creds, ref, branch, e.Path)
		if err != nil || !ok {
			if err != nil {
				p.logger.Warn("contextmod: repofiles rule read failed",
					slog.String("path", e.Path), slog.String("error", err.Error()))
			}
			continue
		}
		out = append(out, repoFileItem(e.Path, content))
	}
	return out, nil
}

// repoFileItem shapes one listed (never injected) instruction file.
func repoFileItem(path string, content []byte) ports.ContextItem {
	return ports.ContextItem{
		SourceKind: "repo_file",
		SourceRef:  path,
		Title:      path,
		Reason:     "repo file",
		Body:       "", // listed, not injected (D-11)
		Tokens:     estimateTokens(string(content)),
		Injected:   false,
	}
}

// defaultBranch is the branch the enumeration reads: the repo's own default, else the
// workspace default, else "main".
func (p *RepoFilesProvider) defaultBranch(ctx context.Context, repo domain.Repo) string {
	if repo.DefaultBranch != nil && strings.TrimSpace(*repo.DefaultBranch) != "" {
		return *repo.DefaultBranch
	}
	if ws, err := p.st.Workspace().Get(ctx); err == nil && strings.TrimSpace(ws.DefaultBranch) != "" {
		return ws.DefaultBranch
	}
	return "main"
}
