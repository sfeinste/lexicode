package agents

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// This file is the ONE definition of the starter roster (a Dev that implements and a Reviewer
// that structurally cannot edit or push — permissions, not prompt: brief D7). Both consumers —
// the S15 bootstrap checklist and the S16 "Use a starter roster" action — build their agents
// from these candidates, so the two paths can never drift.

// Candidate is one suggested starter agent. The JSON shape is the bootstrap preview's
// AgentCandidate (contracts §5 / openapi).
type Candidate struct {
	Name           string `json:"name"`
	Role           string `json:"role"`
	Model          string `json:"model"`
	Directive      string `json:"directive"`
	Checked        bool   `json:"checked"`
	AlreadyCreated bool   `json:"already_created"`
}

// StarterCandidates is the starter pair, with directives that mention the detected stack when
// one is known.
func StarterCandidates(stacks []string) []Candidate {
	stackLine := "this repository's stack"
	if len(stacks) > 0 {
		stackLine = strings.Join(stacks, " and ")
	}
	devDirective := fmt.Sprintf(`You are Dev, the implementation agent for this project.

You work in %s. For each ticket you take on:

1. Read the ticket's description and acceptance criteria before writing any code.
2. Make the smallest change that satisfies the acceptance criteria. Do not refactor
   surrounding code unless the ticket asks for it.
3. Follow the project's existing conventions — match the style of the files you touch, and
   read the repository's instruction docs and wiki pages provided in your context.
4. Run the project's tests and linters before opening a pull request; fix what they find.
5. Open a focused pull request that names the ticket and explains what changed and why.
6. When the ticket is ambiguous, ask the delegating human instead of guessing.`, stackLine)

	reviewerDirective := fmt.Sprintf(`You are Reviewer, the code-review agent for this project.

You review pull requests in %s. You cannot edit files or push branches — your output is the
review itself.

1. Read the linked ticket first: review against its acceptance criteria, not against taste.
2. Tag every finding with a severity: [blocker], [major], [minor] or [nit].
3. Check correctness first, then tests, then clarity. Point at exact lines.
4. Distinguish "this is wrong" from "I would have done this differently" — only the former
   blocks.
5. Keep the summary short: what the change does, whether it meets the criteria, and the list
   of findings. Never restate the diff.`, stackLine)

	return []Candidate{
		{
			Name: "Dev", Role: "Implementation", Model: "claude-sonnet-5",
			Directive: devDirective, Checked: true,
		},
		{
			Name: "Reviewer", Role: "Review", Model: "claude-opus-5",
			Directive: reviewerDirective, Checked: true,
		},
	}
}

// StarterAgent turns a candidate into the agent row to persist. The Reviewer's inability to
// edit or push is a permission, not a sentence in the directive (brief D7).
func StarterAgent(cand Candidate, projectID, now string) domain.Agent {
	a := domain.Agent{
		ID: domain.NewID(), ProjectID: projectID, Name: cand.Name, Role: cand.Role,
		RuntimeID: "claude-code", Model: cand.Model,
		GitAuthorName:  cand.Name,
		GitAuthorEmail: GitEmailFor(cand.Name),
		ConcurrencyCap: 1, MaxWallClockSeconds: 3600, MaxSteps: 200, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	switch cand.Name {
	case "Reviewer":
		a.Color = "#d16ba5"
		a.Effort = "high"
		a.Autonomy = domain.AutonomyApproveEach
		a.Permissions = domain.AgentPermissions{
			ReadFiles: true, EditFiles: false, RunCommands: true,
			PushBranches: false, OpenPRs: false, CommentPRs: true,
			SubmitReviews: true, CreateWikiPages: false,
		}
	default: // Dev
		a.Color = "#5b8def"
		a.Effort = "medium"
		a.Autonomy = domain.AutonomyAutoGates
		a.Permissions = domain.AgentPermissions{
			ReadFiles: true, EditFiles: true, RunCommands: true,
			PushBranches: true, OpenPRs: true, CommentPRs: true,
			SubmitReviews: false, CreateWikiPages: true,
		}
	}
	return a
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// GitEmailFor derives the default git author email from an agent name (D-9's
// `Reviewer <reviewer@agents.lexicode.local>` pattern): lowercase slug at the local domain.
func GitEmailFor(name string) string {
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if slug == "" {
		slug = "agent"
	}
	return slug + "@agents.lexicode.local"
}

// CreateWithDirective inserts an agent and its version-1 directive in one transaction, linking
// the agent to that version, then writes the agent.create audit entry — every path that mints
// an agent (CRUD, starter roster, bootstrap) records it identically. authorID "" means no
// author (system-created).
func CreateWithDirective(ctx context.Context, st *store.Store, aw *audit.Writer, a *domain.Agent, body, note, authorID, now string) error {
	d := domain.AgentDirective{
		ID: domain.NewID(), AgentID: a.ID, Version: 1, Body: body,
		TokenEstimate: EstimateTokens(body),
		Note:          note, CreatedAt: now,
	}
	if authorID != "" {
		uid := authorID
		d.AuthorID = &uid
	}
	err := st.Tx(ctx, func(tx *store.Tx) error {
		if err := tx.Agents().Create(ctx, a); err != nil {
			return err
		}
		if err := tx.Directives().Create(ctx, &d); err != nil {
			return err
		}
		a.DirectiveVersionID = &d.ID
		return tx.Agents().Update(ctx, a)
	})
	if err != nil {
		return err
	}
	return aw.Write(ctx, "agent.create",
		audit.Target{Kind: "agent", ID: a.ID, ProjectID: a.ProjectID}, nil, *a)
}

// EstimateTokens is the documented directive token heuristic: characters / 4, floor 1 for a
// non-empty body. Good enough for a live counter; the runtime never budgets off it.
func EstimateTokens(body string) int64 {
	if body == "" {
		return 0
	}
	n := int64(len(body)) / 4
	if n == 0 {
		n = 1
	}
	return n
}
