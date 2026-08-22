package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/service/agents"
)

// DocChoice is one checked doc in an apply: the path plus the (possibly human-adjusted) scope.
type DocChoice struct {
	Path  string   `json:"path"`
	Scope string   `json:"scope"`
	Paths []string `json:"paths"`
}

// ApplyInput is POST /projects/{key}/bootstrap/apply: exactly the checked subset. Absent
// sections create nothing — nothing is created silently (brief §6.3).
type ApplyInput struct {
	Issues   []int       `json:"issues"`
	Docs     []DocChoice `json:"docs"`
	Triggers []string    `json:"triggers"` // candidate IDs from the preview
	Agents   []string    `json:"agents"`   // candidate names from the preview
	Overview *string     `json:"overview"` // description to set; nil leaves it alone
}

// ApplyResult reports what was created and what was skipped as already imported.
type ApplyResult struct {
	TicketsCreated  []string `json:"tickets_created"` // ticket keys
	IssuesSkipped   []int    `json:"issues_skipped"`  // already imported
	PagesCreated    []string `json:"pages_created"`   // slugs
	DocsSkipped     []string `json:"docs_skipped"`    // already imported paths
	TriggersCreated []string `json:"triggers_created"`
	AgentsCreated   []string `json:"agents_created"`
	OverviewSet     bool     `json:"overview_set"`
}

// Apply creates the checked subset: tickets from issues (origin='import', the issue-number
// marker appended), live wiki pages from docs (imported_from = the repo path, D-11), the
// suggested triggers DISABLED, the starter agents, and the project description. Items already
// imported are skipped even when a stale client re-sends them, which is what makes a re-run
// duplicate-free.
func (s *Service) Apply(ctx context.Context, projectKey string, in ApplyInput) (ApplyResult, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return ApplyResult{}, err
	}
	rp, err := s.st.Repos().ByProject(ctx, p.ID)
	if err != nil {
		return ApplyResult{}, err
	}
	creds, err := s.creds(ctx, rp)
	if err != nil {
		return ApplyResult{}, err
	}
	forge, err := s.forge(rp.Provider)
	if err != nil {
		return ApplyResult{}, err
	}
	ref := rp.Ref()
	branch := ""
	if rp.DefaultBranch != nil {
		branch = *rp.DefaultBranch
	}
	var userID string
	if u, ok := auth.UserFrom(ctx); ok {
		userID = u.ID
	}

	res := ApplyResult{
		TicketsCreated: []string{}, IssuesSkipped: []int{}, PagesCreated: []string{},
		DocsSkipped: []string{}, TriggersCreated: []string{}, AgentsCreated: []string{},
	}

	// ---- issues → tickets --------------------------------------------------------------
	if len(in.Issues) > 0 {
		issues, err := forge.ListOpenIssues(ctx, creds, ref)
		if err != nil {
			return ApplyResult{}, err
		}
		byNumber := map[int]domain.Issue{}
		for _, is := range issues {
			byNumber[is.Number] = is
		}
		imported, err := s.importedIssues(ctx, p.ID)
		if err != nil {
			return ApplyResult{}, err
		}
		backlog, err := s.backlogColumn(ctx, p.ID)
		if err != nil {
			return ApplyResult{}, err
		}
		for _, n := range in.Issues {
			if _, done := imported[n]; done {
				res.IssuesSkipped = append(res.IssuesSkipped, n)
				continue
			}
			is, ok := byNumber[n]
			if !ok {
				continue // closed or gone since the preview; not an error
			}
			key, err := s.importIssue(ctx, p, backlog, is, userID)
			if err != nil {
				return ApplyResult{}, err
			}
			imported[n] = key
			res.TicketsCreated = append(res.TicketsCreated, key)
		}
	}

	// ---- docs → live wiki pages --------------------------------------------------------
	if len(in.Docs) > 0 {
		importedPaths, err := s.st.Wiki().ImportedPaths(ctx, p.ID)
		if err != nil {
			return ApplyResult{}, err
		}
		for _, choice := range in.Docs {
			if _, done := importedPaths[choice.Path]; done {
				res.DocsSkipped = append(res.DocsSkipped, choice.Path)
				continue
			}
			scope := domain.AgentScope(choice.Scope)
			if !scope.IsValid() {
				return ApplyResult{}, fieldErr("docs",
					fmt.Sprintf("%q is not a valid agent scope for %s.", choice.Scope, choice.Path))
			}
			content, ok, err := s.docs.ReadFileIfExists(ctx, creds, ref, branch, choice.Path)
			if err != nil {
				return ApplyResult{}, err
			}
			if !ok {
				continue // deleted since the preview; nothing to import
			}
			slug, err := s.importDoc(ctx, p, choice, string(content), userID)
			if err != nil {
				return ApplyResult{}, err
			}
			importedPaths[choice.Path] = slug
			res.PagesCreated = append(res.PagesCreated, slug)
		}
	}

	// ---- CI → disabled triggers --------------------------------------------------------
	if len(in.Triggers) > 0 {
		existing := map[string]bool{}
		trs, err := s.st.Triggers().ForProject(ctx, p.ID)
		if err != nil {
			return ApplyResult{}, err
		}
		for _, tr := range trs {
			existing[tr.Name] = true
		}
		candidates := suggestedTriggers([]string{"detected"}) // rows; workflows list unused here
		byID := map[string]TriggerCandidate{}
		for _, c := range candidates {
			byID[c.ID] = c
		}
		for _, id := range in.Triggers {
			cand, ok := byID[id]
			if !ok {
				return ApplyResult{}, fieldErr("triggers",
					fmt.Sprintf("%q is not a suggested trigger.", id))
			}
			if existing[cand.Name] {
				continue // already created by a previous apply
			}
			row := triggerRow(cand, p.ID, userID, s.now())
			if err := s.st.Triggers().Create(ctx, &row); err != nil {
				return ApplyResult{}, err
			}
			existing[cand.Name] = true
			res.TriggersCreated = append(res.TriggersCreated, cand.Name)
		}
	}

	// ---- starter agents ----------------------------------------------------------------
	if len(in.Agents) > 0 {
		existing := map[string]bool{}
		ags, err := s.st.Agents().ForProject(ctx, p.ID)
		if err != nil {
			return ApplyResult{}, err
		}
		for _, a := range ags {
			existing[a.Name] = true
		}
		byName := map[string]AgentCandidate{}
		for _, c := range agents.StarterCandidates(nil) {
			byName[c.Name] = c
		}
		for _, name := range in.Agents {
			cand, ok := byName[name]
			if !ok {
				return ApplyResult{}, fieldErr("agents",
					fmt.Sprintf("%q is not a suggested agent.", name))
			}
			if existing[name] {
				continue
			}
			if err := s.importAgent(ctx, p, cand, userID); err != nil {
				return ApplyResult{}, err
			}
			existing[name] = true
			res.AgentsCreated = append(res.AgentsCreated, name)
		}
	}

	// ---- overview ----------------------------------------------------------------------
	if in.Overview != nil && strings.TrimSpace(*in.Overview) != "" {
		p.Description = strings.TrimSpace(*in.Overview)
		p.UpdatedAt = s.now()
		if err := s.st.Projects().Update(ctx, &p); err != nil {
			return ApplyResult{}, err
		}
		res.OverviewSet = true
	}

	// Stamp the sync time and leave one audit entry for the whole apply.
	now := s.now()
	rp.LastSyncedAt = &now
	rp.UpdatedAt = now
	if err := s.st.Repos().Update(ctx, &rp); err != nil {
		return ApplyResult{}, err
	}
	if err := s.audit.Write(ctx, "bootstrap.apply",
		audit.Target{Kind: "repo", ID: p.ID, ProjectID: p.ID}, nil, res); err != nil {
		return ApplyResult{}, err
	}
	s.emit(ctx, "bootstrap.applied", p, map[string]any{
		"tickets": len(res.TicketsCreated), "pages": len(res.PagesCreated),
		"triggers": len(res.TriggersCreated), "agents": len(res.AgentsCreated),
	})
	return res, nil
}

// backlogColumn is where imported tickets land: the project's first backlog-category column
// (never by name — plan rule 3).
func (s *Service) backlogColumn(ctx context.Context, projectID string) (domain.Column, error) {
	cols, err := s.st.Columns().ByCategory(ctx, projectID, domain.CategoryBacklog)
	if err != nil {
		return domain.Column{}, err
	}
	if len(cols) == 0 {
		return domain.Column{}, errors.New("bootstrap: the project has no backlog-category column to import into")
	}
	return cols[0], nil
}

// importIssue creates one origin='import' ticket, allocating its key in the insert
// transaction, attaching the issue's labels (created on first use) and appending the
// issue-number marker that keys idempotency.
func (s *Service) importIssue(ctx context.Context, p domain.Project, col domain.Column, is domain.Issue, userID string) (string, error) {
	now := s.now()
	desc := strings.TrimRight(is.Body, "\n")
	if desc != "" {
		desc += "\n\n"
	}
	desc += fmt.Sprintf("---\nImported from GitHub issue [#%d](%s) by @%s.\n%s",
		is.Number, is.URL, is.AuthorLogin, fmt.Sprintf(importMarker, is.Number))

	tk := domain.Ticket{
		ID: domain.NewID(), ProjectID: p.ID, Title: strings.TrimSpace(is.Title),
		Description: desc, ColumnID: col.ID, Priority: domain.PriorityNone,
		Origin: domain.OriginImport, CreatedAt: now, UpdatedAt: now,
	}
	if tk.Title == "" {
		tk.Title = fmt.Sprintf("Issue #%d", is.Number)
	}
	if userID != "" {
		uid := userID
		tk.CreatedByUserID = &uid
	}
	err := s.st.Tx(ctx, func(tx *store.Tx) error {
		seq, err := tx.Projects().AllocateTicketSeq(ctx, p.ID)
		if err != nil {
			return err
		}
		tk.Seq = seq
		tk.Key = fmt.Sprintf("%s-%d", p.Key, seq)
		tk.Position = float64(seq) // import order; fractional ranking takes over on first drag
		if err := tx.Tickets().Create(ctx, &tk); err != nil {
			return err
		}
		for _, name := range is.Labels {
			id, err := getOrCreateLabel(ctx, tx, p.ID, name)
			if err != nil {
				return err
			}
			if err := tx.Labels().Attach(ctx, tk.ID, id); err != nil &&
				!errors.Is(err, store.ErrUnique) {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return tk.Key, nil
}

// importLabelColor is the one color imported labels get; users recolor in board settings.
const importLabelColor = "#5b8def"

func getOrCreateLabel(ctx context.Context, tx *store.Tx, projectID, name string) (string, error) {
	existing, err := tx.Labels().ForProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	for _, l := range existing {
		if l.Name == name {
			return l.ID, nil
		}
	}
	l := domain.Label{ID: domain.NewID(), ProjectID: projectID, Name: name, Color: importLabelColor}
	if err := tx.Labels().Create(ctx, &l); err != nil {
		return "", err
	}
	return l.ID, nil
}

// importDoc creates one live wiki page (D-11: imported pages are live, with the chosen
// agent_scope; imported_from records the source path) plus its version-1 snapshot.
func (s *Service) importDoc(ctx context.Context, p domain.Project, choice DocChoice, content, userID string) (string, error) {
	now := s.now()
	title := docTitle(content, choice.Path)
	scopePaths := choice.Paths
	if scopePaths == nil {
		scopePaths = []string{}
	}
	var owner *string
	if userID != "" {
		uid := userID
		owner = &uid
	}
	srcPath := choice.Path

	base := slugify(strings.TrimSuffix(path.Base(choice.Path), path.Ext(choice.Path)))
	slug := base
	for attempt := 2; ; attempt++ {
		page := domain.WikiPage{
			ID: domain.NewID(), ProjectID: p.ID, Slug: slug, Title: title,
			Position: 0, OwnerID: owner,
			AgentScope: domain.AgentScope(choice.Scope), ScopePaths: scopePaths,
			Tags: []string{}, Body: content,
			TokenEstimate: int64(len(content) / 4),
			State:         domain.WikiLive, ImportedFrom: &srcPath,
			CreatedAt: now, UpdatedAt: now,
		}
		err := s.st.Tx(ctx, func(tx *store.Tx) error {
			if err := tx.Wiki().CreatePage(ctx, &page); err != nil {
				return err
			}
			return tx.Wiki().CreateVersion(ctx, &domain.WikiVersion{
				ID: domain.NewID(), PageID: page.ID, Version: 1, Title: title, Body: content,
				FrontMatter: map[string]any{
					"agent_scope": choice.Scope, "paths": scopePaths,
					"imported_from": choice.Path,
				},
				AuthorUserID: owner, CreatedAt: now,
			})
		})
		if errors.Is(err, store.ErrUnique) && attempt <= 20 {
			slug = fmt.Sprintf("%s-%d", base, attempt)
			continue
		}
		if err != nil {
			return "", err
		}
		return slug, nil
	}
}

// importAgent creates one starter agent and its version-1 directive through the shared S16
// creator, so the bootstrap path and the roster's starter action write identical rows.
func (s *Service) importAgent(ctx context.Context, p domain.Project, cand AgentCandidate, userID string) error {
	now := s.now()
	a := agents.StarterAgent(cand, p.ID, now)
	return agents.CreateWithDirective(ctx, s.st, s.audit, &a, cand.Directive,
		"Starter directive from repository bootstrap", userID, now)
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return "page"
	}
	return s
}
