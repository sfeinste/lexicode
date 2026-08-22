// push.go supplies the credential the ORCHESTRATOR uses to push a run's branch at teardown
// (§10.5, D-9 amendment). It is the mirror image of propen.go: the agent's container holds no
// repository credential at all — the sandbox's clone step points `origin` at a tokenless URL
// as soon as the fetch is done — so the token comes back here, for one command, in one exec's
// environment.
//
// Secret handling: like prep.go, this file is a sanctioned in-process reader of stored secret
// values. The token reaches ports.ExecOpts.Env and PushAuth.Secrets (for the scheduler's
// redactor) and nowhere else. In particular it never reaches argv — `/proc/<pid>/cmdline` is
// readable by anything else running in the container — and never `.git/config`, which would
// outlive the command that needed it.
package runs

import (
	"context"
	"errors"
	"fmt"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/sched"
	kernelsecrets "github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// tokenUser is the username half of GitHub's tokenized basic credential. It pairs with the
// same constant the forge's CloneURL uses; the token is the password.
const tokenUser = "x-access-token"

// Pusher implements sched.PushCredentials. Store and Secrets are required.
type Pusher struct {
	Store   *store.Store
	Secrets *kernelsecrets.Store
}

// ForRun resolves the repository credential for one run's push.
//
// A run whose project has no connected repository, or a repository with no stored token, is
// not an error: the remote may need no credential (the docker-tagged fixtures clone from a
// bind-mounted `file://` repository), and if it does, the push fails and the run says so.
// That is strictly better than failing teardown over a credential that might not be needed.
func (p *Pusher) ForRun(ctx context.Context, run domain.Run) (sched.PushAuth, error) {
	repo, err := p.Store.Repos().ByProject(ctx, run.ProjectID)
	if errors.Is(err, store.ErrNotFound) {
		return sched.PushAuth{}, nil
	}
	if err != nil {
		return sched.PushAuth{}, err
	}

	auth := sched.PushAuth{}
	if repo.DefaultBranch != nil {
		auth.BaseBranch = *repo.DefaultBranch
	}
	if auth.BaseBranch == "" {
		ws, err := p.Store.Workspace().Get(ctx)
		if err != nil {
			return sched.PushAuth{}, err
		}
		auth.BaseBranch = ws.DefaultBranch
	}

	if repo.TokenSecretID == nil {
		return auth, nil
	}
	token, err := p.Secrets.Get(ctx, *repo.TokenSecretID)
	if err != nil {
		return sched.PushAuth{}, fmt.Errorf("reading the repository token for the push: %w", err)
	}
	if token == "" {
		return auth, nil
	}

	// git's config-via-environment. `http.extraheader` is the form GitHub documents for
	// tokenized HTTPS, and the only one that keeps the credential out of both the command
	// line and the repository config — the same mechanism prep.go already uses to carry
	// `commit.template` and `core.hooksPath` into the container.
	header := sched.BasicAuthHeader(tokenUser, token)
	auth.Env = map[string]string{
		"GIT_CONFIG_KEY_0":   "http.extraheader",
		"GIT_CONFIG_VALUE_0": header,
	}
	auth.Secrets = []string{token, header}
	return auth, nil
}

var _ sched.PushCredentials = (*Pusher)(nil)
