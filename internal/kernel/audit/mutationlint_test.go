package audit_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryServiceMutationWritesAudit is the S06 acceptance criterion "every mutation in later
// stories that lacks an audit row fails a lint test", implemented as a source scan rather than
// runtime magic.
//
// The heuristic, spelled out so a failure is debuggable and a false positive fixable:
//
//   - Scope: every non-test .go file under internal/service/**. Services are the only layer
//     that mutates on behalf of a request (architecture §14); kernel-internal writes (sessions,
//     events, the audit log itself) are not user-facing mutations.
//   - A file is "mutation-shaped" when it declares an exported function or method whose name
//     starts with Create, Update, Delete or Move — the verbs the implementation plan gives
//     mutations. Unexported helpers and other verbs are out of scope by design: the goal is a
//     cheap tripwire, not proof.
//   - Such a file must mention the audit writer: either "audit." (the package, e.g.
//     audit.Target, or a struct field holding the writer) or ".Audit()" (the kernel accessor).
//     The mention is per-file, so a file may delegate to a sibling method in the same file but
//     not to another file — keep the audit call near the mutation it records.
//
// If this test fails, the fix is to add the missing kernel.Audit().Write call to the mutation —
// never to rename the method around the heuristic.
//
// Today internal/service holds only doc.go placeholders, so the test passes vacuously; it is
// merged now so the first story that adds a service mutation trips it immediately.
func TestEveryServiceMutationWritesAudit(t *testing.T) {
	serviceRoot, err := filepath.Abs(filepath.Join("..", "..", "service"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(serviceRoot); err != nil {
		t.Fatalf("internal/service not found at %s: %v", serviceRoot, err)
	}

	var scanned, failures []string
	err = filepath.WalkDir(serviceRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned = append(scanned, path)

		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}

		var mutations []string
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() {
				continue
			}
			for _, prefix := range []string{"Create", "Update", "Delete", "Move"} {
				if strings.HasPrefix(fn.Name.Name, prefix) {
					mutations = append(mutations, fn.Name.Name)
					break
				}
			}
		}
		if len(mutations) == 0 {
			return nil
		}
		text := string(src)
		if !strings.Contains(text, "audit.") && !strings.Contains(text, ".Audit()") {
			rel, _ := filepath.Rel(serviceRoot, path)
			failures = append(failures,
				rel+" declares "+strings.Join(mutations, ", ")+" but never touches the audit writer")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scanned) == 0 {
		t.Fatal("no Go files under internal/service were scanned; the lint is not looking anywhere")
	}
	if len(failures) > 0 {
		t.Errorf("mutations without an audit write (architecture §14: every mutation through a "+
			"service writes audit_log):\n  %s", strings.Join(failures, "\n  "))
	}
}
