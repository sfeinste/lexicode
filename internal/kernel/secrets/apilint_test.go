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
//
// One documented exemption (S15): service/bootstrap/service.go. Contracts §2.2 states, of
// ports.Creds, that "the service layer resolves the token from the secret store
// (repos.token_secret_id) and passes it down; the adapter never reads secrets itself" — the
// bootstrap service's creds() helper is that sanctioned in-process resolution. Its Get call
// feeds forge calls only; no handler writes the value to a response (the repo bodies carry
// has_token, never the token). Widen this list only with the same justification.
func TestNoAPIHandlerTouchesSecretValues(t *testing.T) {
	roots := []string{
		filepath.Join("..", "..", "api"),
		filepath.Join("..", "..", "service"),
	}
	exempt := map[string]bool{
		filepath.Join("service", "bootstrap", "service.go"): true,
		// S19: the workspace-prep builder is the other sanctioned in-process reader D-16
		// names ("container env building"). Its Get calls feed SandboxSpec.Env and the
		// run's Redactor only; no HTTP handler calls into the file, and the runs service's
		// future handlers live in other files that stay under this lint.
		filepath.Join("service", "runs", "prep.go"): true,
		// S24: the PR opener is the contracts §2.2 pattern again — "the service layer
		// resolves the token from the secret store and passes it down; the adapter never
		// reads secrets itself". Its Get feeds forge.OpenPullRequest only; the scheduler
		// calls it at run completion, no HTTP handler reaches the file, and the token never
		// appears in a response body.
		filepath.Join("service", "runs", "propen.go"): true,
		// S39: the review submitter is the same pattern once more — the service layer
		// resolves the repo token and passes it down to forge.SubmitReview, which is the
		// only thing it does with it. It is reached from the MCP server's submit_review
		// tool (a container-facing endpoint that never echoes the value), not from an API
		// handler, and no response body carries the token.
		filepath.Join("service", "runs", "review.go"): true,
		// The orchestrator-owned push (D-9 amendment): the same pattern a fourth time. The
		// container holds no repository credential at all now, so the token has to be
		// resolved here and handed to exactly one exec's environment at teardown. Its Get
		// feeds sched.PushAuth.Env and PushAuth.Secrets (the run redactor) and nothing
		// else; the scheduler is the only caller, no HTTP handler reaches the file, and the
		// value never appears in argv, in .git/config or in a response body.
		filepath.Join("service", "runs", "push.go"): true,
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
			if exempt[rel] {
				return nil
			}

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
