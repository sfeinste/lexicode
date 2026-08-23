package bootstrap

// The general repo-settings PATCH: the repos columns a project owns that are neither the
// connection itself (owner/name/token) nor the network stance (that is UpdateNetworkSettings,
// its sibling in network.go, which this file follows field for field).
//
// setup_script is the one that matters most. It runs during provisioning on every run, in the
// workspace, after the clone and before the agent process starts (docker.Sandbox.Prepare); an
// empty script is skipped entirely, and a non-zero exit fails the run with the script's output
// in the failure. It is how a project declares "install Go / chromium / python3" once, visibly,
// instead of the agent improvising it mid-task.
//
// image_ref is deliberately NOT here: it has its own story to be reported separately.

import (
	"context"
	"fmt"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
)

// maxSetupScriptBytes bounds the script. It is a shell script, not a payload: 64 KiB is far
// past anything legible and still small enough that a stray paste cannot bloat every row the
// provisioning path reads.
const maxSetupScriptBytes = 64 * 1024

// RepoSettingsInput is the PATCH /projects/{key}/repo body. Every field is optional; an absent
// field is left unchanged. The tri-state matches NetworkSettingsInput's:
//
//   - setup_script: a string sets it, "" or null clears it. The column is not nullable and
//     defaults to empty: "no script" is empty, not inherited.
//   - branch_template: a string overrides, null reverts to inheriting the workspace default.
type RepoSettingsInput struct {
	SetupScript    OptString `json:"setup_script"`
	BranchTemplate OptString `json:"branch_template"`
}

// UpdateRepoSettings validates and persists the repo's non-network settings, auditing the
// honest before/after and emitting the mutation on the bus.
func (s *Service) UpdateRepoSettings(ctx context.Context, projectKey string, in RepoSettingsInput) (domain.Repo, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return domain.Repo{}, err
	}
	rp, err := s.st.Repos().ByProject(ctx, p.ID)
	if err != nil {
		return domain.Repo{}, err
	}
	before := rp

	if in.SetupScript.Set {
		// A textarea sends CRLF on Windows; the script runs under /bin/sh, where a stray
		// \r is a syntax error on every line. Normalize rather than hand the user a
		// failure they cannot see in their own input.
		script := strings.ReplaceAll(in.SetupScript.Value, "\r\n", "\n")
		if in.SetupScript.Null {
			script = ""
		}
		if len(script) > maxSetupScriptBytes {
			return domain.Repo{}, fieldErr("setup_script",
				fmt.Sprintf("The setup script is %d bytes; the limit is %d. Move the work into a script in the repository and call it from here.",
					len(script), maxSetupScriptBytes))
		}
		if strings.ContainsRune(script, '\x00') {
			return domain.Repo{}, fieldErr("setup_script",
				"The setup script contains a NUL byte; it must be text a shell can read.")
		}
		rp.SetupScript = script
	}

	if in.BranchTemplate.Set {
		switch {
		case in.BranchTemplate.Null:
			rp.BranchTemplate = nil
		default:
			t := strings.TrimSpace(in.BranchTemplate.Value)
			if t == "" {
				return domain.Repo{}, fieldErr("branch_template",
					"A branch template is required; send null to inherit the workspace default.")
			}
			rp.BranchTemplate = &t
		}
	}

	rp.UpdatedAt = s.now()
	if err := s.st.Repos().Update(ctx, &rp); err != nil {
		return domain.Repo{}, err
	}

	if err := s.audit.Write(ctx, "repo.settings.update",
		audit.Target{Kind: "repo", ID: p.ID, ProjectID: p.ID},
		settingsAudit(before), settingsAudit(rp)); err != nil {
		return domain.Repo{}, err
	}
	s.emit(ctx, "repo.settings_updated", p, map[string]any{
		"setup_script_bytes": len(rp.SetupScript),
		"branch_template":    rp.BranchTemplate,
	})
	return rp, nil
}

// settingsAudit is the audit shape of the columns this mutation can touch, nothing else. The
// script body is recorded in full, as an agent directive's body is (agents/directives.go): the
// before/after is the point of the entry, and the same text already lives in the repos row.
func settingsAudit(rp domain.Repo) map[string]any {
	return map[string]any{
		"setup_script":    rp.SetupScript,
		"branch_template": rp.BranchTemplate,
	}
}
