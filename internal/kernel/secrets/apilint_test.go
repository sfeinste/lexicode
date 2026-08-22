package secrets_test

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

// TestNoAPIHandlerTouchesSecretValues is the S13 acceptance criterion — data model invariant
// 9: "No API path returns ciphertext or plaintext; a test asserts no handler references the
// field" — implemented as a source scan in the style of the S06 audit lint
// (internal/kernel/audit/mutationlint_test.go).
//
// The heuristic, spelled out so a failure is debuggable and a false positive fixable:
//
//   - Scope: every non-test .go file under internal/api/** and internal/service/** — the
//     HTTP surface. internal/kernel/secrets itself and internal/module/** are exempt: the
//     store must touch its own ciphertext, and modules (forge S14, container env S19) are
//     the in-process Get callers D-16 sanctions.
//   - Forbidden words: "ciphertext", "plaintext" and "nonce" (case-insensitive, so the Go
//     field names Ciphertext/Nonce match too) anywhere in the file, comments included —
//     an HTTP-layer file has no business even talking about them.
//   - Forbidden call: in a file that imports internal/kernel/secrets, any call whose method
//     name is Get. The secrets service works on names and metadata (List/Set/Rename/
//     Delete/ByID); Get is the one plaintext reader and it is in-process only. The check is
//     per-file and syntactic, not type-checked — which is the point: a service file that
//     imports the secret store must not contain a .Get( call at all, whatever the receiver,
//     so the plaintext reader cannot hide behind an alias.
//
// If this test fails, the fix is to remove the reference — never to rename fields or smuggle
// the value through a differently-named accessor.
func TestNoAPIHandlerTouchesSecretValues(t *testing.T) {
	roots := []string{
		filepath.Join("..", "..", "api"),
		filepath.Join("..", "..", "service"),
	}
	forbiddenWords := []string{"ciphertext", "plaintext", "nonce"}

	var scanned, failures []string
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(abs); err != nil {
			t.Fatalf("%s not found: %v", root, err)
		}
		err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
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
			rel, _ := filepath.Rel(abs, path)
			rel = filepath.Base(root) + string(filepath.Separator) + rel

			lower := strings.ToLower(string(src))
			for _, word := range forbiddenWords {
				if strings.Contains(lower, word) {
					failures = append(failures, rel+" mentions \""+word+"\"")
				}
			}

			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, src, 0)
			if err != nil {
				return err
			}
			if !importsSecretStore(file) {
				return nil
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Get" {
					failures = append(failures,
						rel+" imports the secret store and calls .Get( at "+
							fset.Position(call.Pos()).String()+
							" — the plaintext reader is in-process only (D-16)")
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(scanned) == 0 {
		t.Fatal("no Go files scanned; the lint is not looking anywhere")
	}
	if len(failures) > 0 {
		t.Errorf("secret values are write-only through the API (D-16, data model invariant 9); "+
			"these files break that:\n  %s", strings.Join(failures, "\n  "))
	}
}

func importsSecretStore(file *ast.File) bool {
	for _, imp := range file.Imports {
		if strings.Trim(imp.Path.Value, `"`) == "github.com/spruce/lexicode/internal/kernel/secrets" {
			return true
		}
	}
	return false
}
