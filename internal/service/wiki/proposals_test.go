package wiki_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
)

// seedProposal writes a proposal row + version-1 snapshot exactly the way the S21
// propose_wiki_page MCP tool does (state proposed, provenance columns, no mention rows).
// target/baseVersion are zero-valued for create-proposals.
func (e *env) seedProposal(projectKey, slug, title, body, reason string, targetID string, baseVersion int64) domain.WikiPage {
	e.t.Helper()
	ctx := context.Background()
	p, err := e.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		e.t.Fatal(err)
	}
	now := domain.Now()
	// The runs FK: proposals reference a real run in production; this harness wires no runs
	// service, so ProposedByRunID stays nil (the mcp package's tests cover the attribution).
	page := domain.WikiPage{
		ID: domain.NewID(), ProjectID: p.ID, Slug: slug, Title: title,
		AgentScope: domain.ScopeAuto, ScopePaths: []string{}, Tags: []string{},
		Body: body, TokenEstimate: int64(len(body) / 4),
		State: domain.WikiProposed, ProposedReason: &reason,
		CreatedAt: now, UpdatedAt: now,
	}
	if targetID != "" {
		page.ProposalTargetID = &targetID
		page.ProposedBaseVersion = &baseVersion
	}
	if err := e.st.Wiki().CreatePage(ctx, &page); err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.Wiki().CreateVersion(ctx, &domain.WikiVersion{
		ID: domain.NewID(), PageID: page.ID, Version: 1,
		Title: title, Body: body, FrontMatter: map[string]any{}, CreatedAt: now,
	}); err != nil {
		e.t.Fatal(err)
	}
	return page
}

func (e *env) auditCount(action, targetID string) int {
	e.t.Helper()
	var n int
	err := e.st.Reader().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE action = ? AND target_id = ?`,
		action, targetID).Scan(&n)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

// S35 acceptance: accepting a create-proposal turns the same row live; the tree shows it
// without the PROPOSED state, and the accept left an audit row.
func TestAcceptCreateProposal(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")

	prop := e.seedProposal("PAY", "database-migrations", "Database migrations",
		"Always use the migration tool.", "You corrected me twice about migrations", "", 0)

	// The detail carries the review extras: the reason, no target (create-proposal).
	status, v := e.doJSON(c, "GET", "/api/v1/wiki/"+prop.ID, "")
	if status != http.StatusOK {
		t.Fatalf("detail = %d: %v", status, v)
	}
	proposal, ok := v["proposal"].(map[string]any)
	if !ok {
		t.Fatalf("detail has no proposal info: %v", v)
	}
	if proposal["reason"] != "You corrected me twice about migrations" {
		t.Fatalf("reason = %v", proposal["reason"])
	}

	status, v = e.doJSON(c, "POST", "/api/v1/wiki/"+prop.ID+"/accept", "")
	if status != http.StatusOK {
		t.Fatalf("accept = %d: %v", status, v)
	}
	page := v["page"].(map[string]any)
	if page["state"] != "live" || page["id"] != prop.ID {
		t.Fatalf("accepted page = %v, want the same row live", page)
	}

	got, err := e.st.Wiki().ByID(context.Background(), prop.ID)
	if err != nil || got.State != domain.WikiLive || got.ArchivedAt != nil {
		t.Fatalf("page after accept = %+v (err %v), want live and unarchived", got, err)
	}
	if n := e.auditCount("wiki.proposal_accept", prop.ID); n != 1 {
		t.Fatalf("wiki.proposal_accept audit rows = %d, want 1", n)
	}

	// Accepting twice is refused: no longer a pending proposal.
	status, v = e.doJSON(c, "POST", "/api/v1/wiki/"+prop.ID+"/accept", "")
	if status != http.StatusConflict || v["type"] != "wiki_not_a_proposal" {
		t.Fatalf("second accept = %d %v, want 409 wiki_not_a_proposal", status, v)
	}
}

// S35 acceptance: a clean edit-proposal applies as the target's next version and the
// proposal row is archived; a stale one (the target advanced past the base version) is the
// 409 wiki_proposal_conflict problem naming both versions — and nothing is clobbered.
func TestAcceptEditProposalThreeWayCheck(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")

	targetID, _ := e.createPage(c, "PAY",
		`{"title":"Deploys","body":"Ship on Tuesdays."}`) // version 1

	// Clean: proposed against version 1, target still at version 1.
	clean := e.seedProposal("PAY", "deploys-proposal", "Deploys",
		"Ship on Tuesdays.\nNever ship on Fridays.", "fresh", targetID, 1)
	status, v := e.doJSON(c, "POST", "/api/v1/wiki/"+clean.ID+"/accept", "")
	if status != http.StatusOK {
		t.Fatalf("clean accept = %d: %v", status, v)
	}
	page := v["page"].(map[string]any)
	if page["id"] != targetID || page["body"] != "Ship on Tuesdays.\nNever ship on Fridays." {
		t.Fatalf("accept returned %v, want the updated target", page)
	}
	version, err := e.st.Wiki().LatestVersion(context.Background(), targetID)
	if err != nil || version != 2 {
		t.Fatalf("target latest version = %d (err %v), want 2", version, err)
	}
	prop, err := e.st.Wiki().ByID(context.Background(), clean.ID)
	if err != nil || prop.ArchivedAt == nil {
		t.Fatalf("proposal after accept = %+v (err %v), want archived", prop, err)
	}

	// Stale: proposed against version 2, then the live page advances to version 3.
	stale := e.seedProposal("PAY", "deploys-proposal-2", "Deploys",
		"Ship whenever.", "stale", targetID, 2)
	status, v = e.doJSON(c, "PATCH", "/api/v1/wiki/"+targetID,
		`{"body":"Ship on Tuesdays.\nNever ship on Fridays.\nAlways tag releases."}`)
	if status != http.StatusOK {
		t.Fatalf("advance target = %d: %v", status, v)
	}

	status, v = e.doJSON(c, "POST", "/api/v1/wiki/"+stale.ID+"/accept", "")
	if status != http.StatusConflict || v["type"] != "wiki_proposal_conflict" {
		t.Fatalf("stale accept = %d %v, want 409 wiki_proposal_conflict", status, v)
	}
	detail := v["detail"].(string)
	for _, want := range []string{"version 3", "version 2"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("conflict detail %q does not name %q", detail, want)
		}
	}
	// Nothing clobbered: the target kept its advanced body, the proposal stays pending.
	target, err := e.st.Wiki().ByID(context.Background(), targetID)
	if err != nil || target.Body != "Ship on Tuesdays.\nNever ship on Fridays.\nAlways tag releases." {
		t.Fatalf("target after stale accept = %+v (err %v), want untouched", target, err)
	}
	prop, err = e.st.Wiki().ByID(context.Background(), stale.ID)
	if err != nil || prop.ArchivedAt != nil || prop.State != domain.WikiProposed {
		t.Fatalf("stale proposal = %+v (err %v), want still pending", prop, err)
	}

	// The detail's proposal info flags the divergence for the UI's up-front conflict view.
	status, v = e.doJSON(c, "GET", "/api/v1/wiki/"+stale.ID, "")
	if status != http.StatusOK {
		t.Fatalf("stale detail = %d", status)
	}
	info := v["proposal"].(map[string]any)
	if info["base_version"].(float64) != 2 || info["current_version"].(float64) != 3 {
		t.Fatalf("proposal info versions = %v, want base 2 / current 3", info)
	}
	if info["target_body"] == "" || info["base_body"] == "" {
		t.Fatalf("proposal info bodies missing: %v", info)
	}
}

// S35 acceptance: dismissing leaves an audit row; the proposal drops out of the tree but
// its row survives (archived, never deleted).
func TestDismissLeavesAuditRow(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")

	prop := e.seedProposal("PAY", "noise", "Noise", "Not useful.", "hm", "", 0)

	status, _ := e.doJSON(c, "POST", "/api/v1/wiki/"+prop.ID+"/dismiss", "")
	if status != http.StatusNoContent {
		t.Fatalf("dismiss = %d, want 204", status)
	}

	// Gone from the tree.
	status, v := e.doJSON(c, "GET", "/api/v1/projects/PAY/wiki", "")
	if status != http.StatusOK {
		t.Fatalf("list = %d", status)
	}
	for _, raw := range v["pages"].([]any) {
		if raw.(map[string]any)["id"] == prop.ID {
			t.Fatalf("dismissed proposal still in the tree: %v", raw)
		}
	}

	// The row survives, archived, with its audit entry.
	got, err := e.st.Wiki().ByID(context.Background(), prop.ID)
	if err != nil || got.ArchivedAt == nil {
		t.Fatalf("dismissed row = %+v (err %v), want archived", got, err)
	}
	if n := e.auditCount("wiki.proposal_dismiss", prop.ID); n != 1 {
		t.Fatalf("wiki.proposal_dismiss audit rows = %d, want 1", n)
	}

	// Dismissing twice is refused.
	status, v = e.doJSON(c, "POST", "/api/v1/wiki/"+prop.ID+"/dismiss", "")
	if status != http.StatusConflict || v["type"] != "wiki_not_a_proposal" {
		t.Fatalf("second dismiss = %d %v, want 409 wiki_not_a_proposal", status, v)
	}
}
