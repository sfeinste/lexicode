package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/service/agents"
)

// importMarker is the machine-readable marker Apply appends to every imported ticket's
// description. It is the persisted issue-number mapping (the tickets table has no issue-number
// column), so the format is load-bearing: a re-scan matches on exactly this.
const importMarker = "<!-- lexicode:import issue=%d -->"

var importMarkerRe = regexp.MustCompile(`<!-- lexicode:import issue=(\d+) -->`)

// The §6.3 / plan detection list, in the order the checklist shows single files.
var probeFiles = []struct {
	path  string
	scope domain.AgentScope
}{
	{"AGENTS.md", domain.ScopeAlways},
	{"CLAUDE.md", domain.ScopeAlways},
	{".github/copilot-instructions.md", domain.ScopeAlways},
	{"README.md", domain.ScopeAuto},
}

// stackProbes maps well-known manifest files to a stack name for the starter directives.
var stackProbes = []struct{ path, stack string }{
	{"go.mod", "Go"},
	{"package.json", "TypeScript/JavaScript"},
	{"Cargo.toml", "Rust"},
	{"pyproject.toml", "Python"},
	{"requirements.txt", "Python"},
}

// ---------------------------------------------------------------- payload types -----

// IssueCandidate is one open issue offered as an importable ticket. Checked defaults to true
// (brief: 12 open issues → 12 checked-by-default tickets); an already-imported issue is
// offered unchecked and labeled instead.
type IssueCandidate struct {
	Number          int      `json:"number"`
	Title           string   `json:"title"`
	Body            string   `json:"body"`
	AuthorLogin     string   `json:"author_login"`
	Labels          []string `json:"labels"`
	URL             string   `json:"url"`
	Checked         bool     `json:"checked"`
	AlreadyImported bool     `json:"already_imported"`
	TicketKey       string   `json:"ticket_key,omitempty"` // set when already imported
}

// DocCandidate is one detected instruction doc with its proposed agent_scope (D-11, plan
// mapping: AGENTS.md/CLAUDE.md → always, .cursor/rules with globs → paths, docs/** and README
// → auto).
type DocCandidate struct {
	Path            string   `json:"path"`
	Title           string   `json:"title"`
	ProposedScope   string   `json:"proposed_scope"`
	ScopePaths      []string `json:"scope_paths"`
	Checked         bool     `json:"checked"`
	AlreadyImported bool     `json:"already_imported"`
	PageSlug        string   `json:"page_slug,omitempty"` // set when already imported
}

// TriggerCandidate is one pre-filled suggested trigger. Enabled is always false in the
// suggestion and in what Apply creates — the toggles ship OFF (brief §6.3).
type TriggerCandidate struct {
	ID             string   `json:"id"` // stable candidate id, what Apply selects by
	Name           string   `json:"name"`
	Event          string   `json:"event"`
	ActivityTypes  []string `json:"activity_types"`
	Description    string   `json:"description"`
	Workflows      []string `json:"workflows"` // the detected CI files that motivated it
	Checked        bool     `json:"checked"`
	AlreadyCreated bool     `json:"already_created"`
}

// AgentCandidate is one suggested starter agent. The candidate content itself lives in the
// agents service (S16 extracted it so the bootstrap checklist and the roster's "Use a starter
// roster" action can never drift); this alias keeps the preview's JSON name.
type AgentCandidate = agents.Candidate

// OverviewCandidate is the project-description draft generated from the README's first
// section. AlreadySet means the project has a description; the draft is then offered
// unchecked so an apply cannot silently overwrite it.
type OverviewCandidate struct {
	Draft      string `json:"draft"`
	Checked    bool   `json:"checked"`
	AlreadySet bool   `json:"already_set"`
}

// Preview is the one-payload bootstrap preview. It writes nothing.
type Preview struct {
	Issues   []IssueCandidate   `json:"issues"`
	Docs     []DocCandidate     `json:"docs"`
	Triggers []TriggerCandidate `json:"triggers"`
	Agents   []AgentCandidate   `json:"agents"`
	Overview OverviewCandidate  `json:"overview"`
}

// ---------------------------------------------------------------- preview -----

// BuildPreview assembles the checklist payload: open issues, detected docs, detected CI as
// suggested triggers, suggested agents and the Overview draft — in one round of forge reads,
// with previously imported items marked (matched on issue number and doc path).
func (s *Service) BuildPreview(ctx context.Context, projectKey string) (Preview, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return Preview{}, err
	}
	rp, err := s.st.Repos().ByProject(ctx, p.ID)
	if err != nil {
		return Preview{}, err
	}
	creds, err := s.creds(ctx, rp)
	if err != nil {
		return Preview{}, err
	}
	forge, err := s.forge(rp.Provider)
	if err != nil {
		return Preview{}, err
	}
	ref := rp.Ref()
	branch := ""
	if rp.DefaultBranch != nil {
		branch = *rp.DefaultBranch
	}

	pv := Preview{}

	// Issues, marked against the tickets already imported (marker match).
	issues, err := forge.ListOpenIssues(ctx, creds, ref)
	if err != nil {
		return Preview{}, err
	}
	imported, err := s.importedIssues(ctx, p.ID)
	if err != nil {
		return Preview{}, err
	}
	pv.Issues = make([]IssueCandidate, 0, len(issues))
	for _, is := range issues {
		labels := is.Labels
		if labels == nil {
			labels = []string{} // JSON [] — the UI maps over this
		}
		c := IssueCandidate{
			Number: is.Number, Title: is.Title, Body: is.Body,
			AuthorLogin: is.AuthorLogin, Labels: labels, URL: is.URL,
			Checked: true,
		}
		if key, ok := imported[is.Number]; ok {
			c.Checked = false
			c.AlreadyImported = true
			c.TicketKey = key
		}
		pv.Issues = append(pv.Issues, c)
	}

	// Docs, marked against wiki_pages.imported_from.
	detected, readme, err := s.detectDocs(ctx, creds, ref, branch)
	if err != nil {
		return Preview{}, err
	}
	importedPaths, err := s.st.Wiki().ImportedPaths(ctx, p.ID)
	if err != nil {
		return Preview{}, err
	}
	pv.Docs = markImported(detected, importedPaths)

	// CI → the two pre-filled triggers, marked against existing trigger names.
	workflows, err := s.docs.ListDir(ctx, creds, ref, branch, ".github/workflows")
	if err != nil {
		return Preview{}, err
	}
	var workflowFiles []string
	for _, e := range workflows {
		if e.Type == "file" {
			workflowFiles = append(workflowFiles, e.Path)
		}
	}
	existingTriggers := map[string]bool{}
	trs, err := s.st.Triggers().ForProject(ctx, p.ID)
	if err != nil {
		return Preview{}, err
	}
	for _, tr := range trs {
		existingTriggers[tr.Name] = true
	}
	for _, cand := range suggestedTriggers(workflowFiles) {
		if existingTriggers[cand.Name] {
			cand.Checked = false
			cand.AlreadyCreated = true
		}
		pv.Triggers = append(pv.Triggers, cand)
	}

	// Suggested agents, marked against existing agent names.
	stacks, err := s.detectStacks(ctx, creds, ref, branch)
	if err != nil {
		return Preview{}, err
	}
	existingAgents := map[string]bool{}
	ags, err := s.st.Agents().ForProject(ctx, p.ID)
	if err != nil {
		return Preview{}, err
	}
	for _, a := range ags {
		existingAgents[a.Name] = true
	}
	for _, cand := range agents.StarterCandidates(stacks) {
		if existingAgents[cand.Name] {
			cand.Checked = false
			cand.AlreadyCreated = true
		}
		pv.Agents = append(pv.Agents, cand)
	}

	// Overview draft from the README's first section.
	pv.Overview = OverviewCandidate{Draft: readmeFirstSection(readme)}
	if pv.Overview.Draft != "" {
		pv.Overview.Checked = true
	}
	if strings.TrimSpace(p.Description) != "" {
		pv.Overview.Checked = false
		pv.Overview.AlreadySet = true
	}
	return pv, nil
}

// importedIssues maps already-imported issue numbers to their ticket keys by parsing the
// import marker out of origin='import' tickets.
func (s *Service) importedIssues(ctx context.Context, projectID string) (map[int]string, error) {
	tickets, err := s.st.Tickets().ForProjectIncludingArchived(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := map[int]string{}
	for _, tk := range tickets {
		if tk.Origin != domain.OriginImport {
			continue
		}
		m := importMarkerRe.FindStringSubmatch(tk.Description)
		if m == nil {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(m[1], "%d", &n); err == nil {
			out[n] = tk.Key
		}
	}
	return out, nil
}

// detectDocs probes the §6.3 detection list. It returns the candidates in a stable order and
// the README's content (for the Overview draft) so the README is fetched once.
func (s *Service) detectDocs(ctx context.Context, creds ports.Creds, ref domain.RepoRef, branch string) ([]DocCandidate, string, error) {
	var out []DocCandidate
	var readme string

	for _, probe := range probeFiles {
		content, ok, err := s.docs.ReadFileIfExists(ctx, creds, ref, branch, probe.path)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			continue
		}
		if probe.path == "README.md" {
			readme = string(content)
		}
		out = append(out, DocCandidate{
			Path: probe.path, Title: docTitle(string(content), probe.path),
			ProposedScope: string(probe.scope), ScopePaths: []string{}, Checked: true,
		})
	}

	// .cursor/rules/* — rule files with a `globs:` front-matter line propose scope `paths`
	// carrying those globs; rules without globs propose `auto`.
	rules, err := s.docs.ListDir(ctx, creds, ref, branch, ".cursor/rules")
	if err != nil {
		return nil, "", err
	}
	for _, e := range rules {
		if e.Type != "file" || !isDocFile(e.Name) {
			continue
		}
		content, ok, err := s.docs.ReadFileIfExists(ctx, creds, ref, branch, e.Path)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			continue
		}
		globs := cursorRuleGlobs(string(content))
		scope, paths := domain.ScopeAuto, []string{}
		if len(globs) > 0 {
			scope, paths = domain.ScopePaths, globs
		}
		out = append(out, DocCandidate{
			Path: e.Path, Title: docTitle(string(content), e.Path),
			ProposedScope: string(scope), ScopePaths: paths, Checked: true,
		})
	}

	// docs/** at depth 2: docs/*.md plus docs/<dir>/*.md.
	docsPaths, err := s.listDocsTree(ctx, creds, ref, branch)
	if err != nil {
		return nil, "", err
	}
	for _, dp := range docsPaths {
		content, ok, err := s.docs.ReadFileIfExists(ctx, creds, ref, branch, dp)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			continue
		}
		out = append(out, DocCandidate{
			Path: dp, Title: docTitle(string(content), dp),
			ProposedScope: string(domain.ScopeAuto), ScopePaths: []string{}, Checked: true,
		})
	}
	return out, readme, nil
}

// listDocsTree lists docs/*.md and docs/<subdir>/*.md — the plan's "docs/** (depth 2)".
func (s *Service) listDocsTree(ctx context.Context, creds ports.Creds, ref domain.RepoRef, branch string) ([]string, error) {
	top, err := s.docs.ListDir(ctx, creds, ref, branch, "docs")
	if err != nil {
		return nil, err
	}
	var out []string
	var subdirs []string
	for _, e := range top {
		switch {
		case e.Type == "file" && isDocFile(e.Name):
			out = append(out, e.Path)
		case e.Type == "dir":
			subdirs = append(subdirs, e.Path)
		}
	}
	sort.Strings(subdirs)
	for _, d := range subdirs {
		entries, err := s.docs.ListDir(ctx, creds, ref, branch, d)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Type == "file" && isDocFile(e.Name) {
				out = append(out, e.Path)
			}
		}
	}
	return out, nil
}

// detectStacks probes well-known manifests and names the stacks found.
func (s *Service) detectStacks(ctx context.Context, creds ports.Creds, ref domain.RepoRef, branch string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, probe := range stackProbes {
		if seen[probe.stack] {
			continue
		}
		_, ok, err := s.docs.ReadFileIfExists(ctx, creds, ref, branch, probe.path)
		if err != nil {
			return nil, err
		}
		if ok {
			seen[probe.stack] = true
			out = append(out, probe.stack)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- pure helpers -----

func isDocFile(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	return ext == ".md" || ext == ".mdc" || ext == ".markdown"
}

// docTitle is the first `# ` heading, or the file name without extension.
func docTitle(content, p string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	base := path.Base(p)
	return strings.TrimSuffix(base, path.Ext(base))
}

// cursorRuleGlobs pulls the `globs:` list out of a .cursor/rules front-matter block. Both the
// inline form (`globs: ["a/**", "b/**"]` or `globs: a/**,b/**`) and the YAML list form are
// handled; anything unparseable simply yields no globs.
func cursorRuleGlobs(content string) []string {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil
	}
	var globs []string
	inList := false
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			break
		}
		if inList {
			if strings.HasPrefix(trimmed, "- ") {
				if g := cleanGlob(strings.TrimPrefix(trimmed, "- ")); g != "" {
					globs = append(globs, g)
				}
				continue
			}
			inList = false
		}
		if !strings.HasPrefix(trimmed, "globs:") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "globs:"))
		if rest == "" {
			inList = true
			continue
		}
		rest = strings.Trim(rest, "[]")
		for _, part := range strings.Split(rest, ",") {
			if g := cleanGlob(part); g != "" {
				globs = append(globs, g)
			}
		}
	}
	return globs
}

func cleanGlob(s string) string {
	return strings.Trim(strings.TrimSpace(s), `"'`)
}

// readmeFirstSection is the Overview draft: the README's prose between its title and the next
// heading, trimmed to something description-sized.
func readmeFirstSection(readme string) string {
	if readme == "" {
		return ""
	}
	var kept []string
	seenHeading := false
	for _, line := range strings.Split(readme, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			if seenHeading {
				break
			}
			seenHeading = true
			continue
		}
		// Skip badge-only lines — they make a poor project description.
		if strings.HasPrefix(trimmed, "[![") || strings.HasPrefix(trimmed, "![") {
			continue
		}
		kept = append(kept, line)
	}
	out := strings.TrimSpace(strings.Join(kept, "\n"))
	const maxLen = 1000
	if len(out) > maxLen {
		cut := out[:maxLen]
		if i := strings.LastIndexByte(cut, ' '); i > 0 {
			cut = cut[:i]
		}
		out = cut + "…"
	}
	return out
}

// The prompt overrides the two suggested rules ship with (contracts §4 `{{...}}`
// interpolation, evaluated against the normalized event payload by service/triggers).
//
// They are not decoration. A trigger-spawned run has no ticket, so the prompt override is the
// only thing that can put a "# Task" section in its prompt: with an empty override — which is
// what these rules used to carry — the agent was handed its directive, its project guidance
// and nothing whatsoever about what it was supposed to do. It then did something plausible and
// unrelated. The `event` context provider now supplies the facts of the occurrence; these
// strings supply the instruction, which is a different thing and still has to be said.
//
// Both are exported so the acceptance harness can fire the SHIPPED rule rather than a
// hand-written stand-in — the drift that let a broken default pass a green acceptance.
const (
	// ReviewerPrompt is the "agent PR opened → run Reviewer" task. It names the tool because
	// a review that is not submitted through `submit_review` is not posted anywhere: it ends
	// as the run's final message, which is exactly the failure this default exists to stop.
	ReviewerPrompt = "Review pull request #{{pr.number}} — \"{{pr.title}}\" — on branch " +
		"`{{pr.branch}}`, opened against `{{pr.base}}` by {{pr.author}}.\n\n" +
		"That head branch is already checked out in this workspace, so read the diff against " +
		"`{{pr.base}}` and review the change on its merits: correctness first, then the " +
		"tests, then anything a maintainer would have to fix later.\n\n" +
		"Post your findings with the `submit_review` tool (`mcp__lexicode__submit_review`): a " +
		"short summary, plus one severity-tagged finding per problem — `blocker`, `major`, " +
		"`minor` or `nit` — each with the file and line it is on. That tool call is the only " +
		"way your review reaches the pull request. A review written as your final message is " +
		"never posted, and nobody sees it."

	// CIFixPrompt is the "CI failed → run Dev" task.
	CIFixPrompt = "The `{{check.name}}` check suite failed on pull request #{{pr.number}}, " +
		"on branch `{{pr.branch}}`. The failing run is at {{check.url}}.\n\n" +
		"That branch is already checked out in this workspace. Work out why the suite failed, " +
		"fix it on this branch, and commit the fix. Keep it to what the failure needs — this " +
		"run exists to get the checks green, not to revisit the change they are checking."
)

// suggestedTriggers is the brief's two pre-filled rules, offered only when CI was detected.
// Both ship with Enabled=false — Apply preserves that.
func suggestedTriggers(workflowFiles []string) []TriggerCandidate {
	if len(workflowFiles) == 0 {
		return nil
	}
	sort.Strings(workflowFiles)
	return []TriggerCandidate{
		{
			ID: "agent-pr-review", Name: "Agent PR opened → run Reviewer",
			Event:         "pull_request",
			ActivityTypes: []string{"opened", "ready_for_review"},
			Description:   "When an agent opens a pull request, run the Reviewer agent on it.",
			Workflows:     workflowFiles, Checked: true,
		},
		{
			// No loop_config override: the guard exempts check_suite events from actor
			// suppression (a CI result is a machine verdict about the agent's work, not the
			// agent acting), so this rule fires on the agent's own branch under the shipped
			// defaults. See internal/kernel/guard's exemptFromActorSuppression.
			ID: "ci-failed-fix", Name: "CI failed → run Dev",
			Event:         "check_suite",
			ActivityTypes: []string{"completed"},
			Description:   "When a check suite completes with a failure on an agent branch, run the Dev agent to fix it.",
			Workflows:     workflowFiles, Checked: true,
		},
	}
}

// triggerRow turns a candidate into the row Apply persists. Enabled is hard-false here — the
// suggested triggers are created disabled (brief §6.3), no input can flip that at creation.
func triggerRow(cand TriggerCandidate, projectID, createdBy, now string) domain.Trigger {
	activity, _ := jsonArr(cand.ActivityTypes)
	var conditions, actions string
	switch cand.ID {
	case "agent-pr-review":
		conditions = `{"all":[{"field":"pr.author_kind","op":"enum.is","value":"agent"}]}`
		actions = runAgentAction("Reviewer", ReviewerPrompt)
	case "ci-failed-fix":
		conditions = `{"all":[{"field":"check.conclusion","op":"enum.is","value":"failure"}]}`
		actions = runAgentAction("Dev", CIFixPrompt)
	}
	var by *string
	if createdBy != "" {
		by = &createdBy
	}
	return domain.Trigger{
		ID: domain.NewID(), ProjectID: projectID, Name: cand.Name, Enabled: false,
		SourceID: "github.poll", Event: cand.Event,
		ActivityTypes: []byte(activity), Filters: []byte("{}"),
		Conditions: []byte(conditions), Actions: []byte(actions),
		LoopConfig: domain.DefaultLoopConfig(),
		CreatedBy:  by, CreatedAt: now, UpdatedAt: now,
	}
}

// runAgentAction renders the one-action list a suggested rule carries. json.Marshal, not a
// hand-built string: the prompts contain quotes and newlines, and a rule whose actions column
// is invalid JSON fires nothing.
func runAgentAction(agentName, prompt string) string {
	b, err := json.Marshal([]map[string]any{{
		"action_id": "run_agent",
		"params":    map[string]any{"agent_name": agentName, "prompt": prompt},
	}})
	if err != nil {
		// Unreachable: the inputs are strings and maps of strings.
		return `[]`
	}
	return string(b)
}

func jsonArr(ss []string) (string, error) {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(parts, ",") + "]", nil
}
