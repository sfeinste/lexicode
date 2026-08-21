package board

import (
	"github.com/spruce/lexicode/internal/domain"
)

// This file is the one place default column names exist in code — the "creation-site list"
// the column-name lint test (columnname_lint_test.go) excludes by path. The names below are
// display strings a user may rename five minutes later; nothing anywhere may compare against
// them (plan rule 3). Automation keys off Category.

// positionGap is the spacing between adjacent column positions. Reordering places a column at
// the midpoint of its new neighbours; the gap gives ten halvings before a renumber is needed.
const positionGap = 1024

// DefaultColumns returns the S09 default column set for a new project, in board order:
// Backlog(backlog) · Ready(ready) · In Progress(running) · In Review(review) · Done(done) ·
// Canceled(canceled). Fresh IDs and gap-spaced positions; timestamps stamped from now. Pure
// data — the projects service inserts these inside its project-create transaction (and audits
// that mutation), so a project is never observable without its board.
func DefaultColumns(projectID, now string) []domain.Column {
	defs := []struct {
		name     string
		category domain.ColumnCategory
	}{
		{"Backlog", domain.CategoryBacklog},
		{"Ready", domain.CategoryReady},
		{"In Progress", domain.CategoryRunning},
		{"In Review", domain.CategoryReview},
		{"Done", domain.CategoryDone},
		{"Canceled", domain.CategoryCanceled},
	}
	out := make([]domain.Column, len(defs))
	for i, d := range defs {
		out[i] = domain.Column{
			ID: domain.NewID(), ProjectID: projectID, Name: d.name, Category: d.category,
			Position: int64((i + 1) * positionGap), CreatedAt: now, UpdatedAt: now,
		}
	}
	return out
}
