package board_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoCodeComparesDefaultColumnNames is the S09 lint test for plan rule 3 / brief D2:
// **never reference a board column by name in code — always by category**. Renaming a column
// must change nothing functional, which is only true while no code path branches on a name.
//
// The heuristic, spelled out so a failure is debuggable and a false positive fixable:
//
//   - Scope: every non-test .go file under internal/. Test files are excluded — tests
//     legitimately assert display strings (e.g. "the default set is named Backlog…").
//   - A violation is a string literal equal to one of the default column names — "Backlog",
//     "Ready", "In Progress", "In Review", "Done", "Canceled" (exact case) — appearing where
//     code branches on it: as an operand of == or !=, or in a switch case clause.
//   - Excluded by path: internal/service/board/defaults.go (the creation-site list — the one
//     place the names may exist, as data in a composite literal, never in a comparison) and
//     internal/kernel/store/seed/ (demo fixtures; its column names are deliberately different
//     display strings anyway).
//   - Out of scope by design: comparisons routed through variables or helper calls. This is a
//     cheap tripwire against the obvious mistake, not proof. Automation-grade enforcement is
//     the Category field's type: repositories only offer lookups by ID and category.
//
// If this test fails, the fix is to branch on domain.ColumnCategory instead of the name —
// never to rename the column or launder the literal through a variable.
func TestNoCodeComparesDefaultColumnNames(t *testing.T) {
	defaultNames := map[string]bool{
		"Backlog": true, "Ready": true, "In Progress": true,
		"In Review": true, "Done": true, "Canceled": true,
	}

	internalRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(internalRoot) != "internal" {
		t.Fatalf("expected to resolve internal/, got %s", internalRoot)
	}
	excluded := []string{
		filepath.Join(internalRoot, "service", "board", "defaults.go"),
		filepath.Join(internalRoot, "kernel", "store", "seed") + string(filepath.Separator),
	}

	// isDefaultName unwraps parens and reports whether the expression is a string literal of
	// a default column name.
	isDefaultName := func(e ast.Expr) (string, bool) {
		for {
			p, ok := e.(*ast.ParenExpr)
			if !ok {
				break
			}
			e = p.X
		}
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		var v string
		if _, err := fmt.Sscanf(lit.Value, "%q", &v); err != nil {
			return "", false
		}
		return v, defaultNames[v]
	}

	var scanned int
	var violations []string
	err = filepath.WalkDir(internalRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		for _, ex := range excluded {
			if path == ex || strings.HasPrefix(path, ex) {
				return nil
			}
		}
		scanned++

		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.BinaryExpr:
				if node.Op != token.EQL && node.Op != token.NEQ {
					return true
				}
				for _, side := range []ast.Expr{node.X, node.Y} {
					if name, ok := isDefaultName(side); ok {
						violations = append(violations, fmt.Sprintf(
							"%s: compares against default column name %q — branch on the column's category instead (plan rule 3)",
							fset.Position(node.Pos()), name))
					}
				}
			case *ast.CaseClause:
				for _, v := range node.List {
					if name, ok := isDefaultName(v); ok {
						violations = append(violations, fmt.Sprintf(
							"%s: switch case on default column name %q — switch on the column's category instead (plan rule 3)",
							fset.Position(v.Pos()), name))
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned == 0 {
		t.Fatal("no non-test .go files scanned under internal/; the walk is broken")
	}
	for _, v := range violations {
		t.Error(v)
	}
}
