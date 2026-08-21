package kernel_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const modulePath = "github.com/spruce/lexicode"

// forbidden lists the package trees the kernel may never depend on, directly or transitively.
// This is the dependency rule from architecture §2.1:
//
//	module  ──▶ kernel/ports ──▶ kernel/domain types
//	service ──▶ kernel
//	api     ──▶ service
//	kernel  ──▶ nothing above it
//
// Nothing in Go enforces "who may import whom", which is why the architecture calls a test like
// this one its only real defence. If this test fails, the fix is to move the shared thing down
// into the kernel or to invert the dependency behind a port — never to add an exception here.
var forbidden = []string{
	modulePath + "/internal/module",
	modulePath + "/internal/api",
	modulePath + "/internal/service",
}

const kernelTree = modulePath + "/internal/kernel"

// TestKernelImportsNothingAboveIt walks the real package import graph and fails on the first edge
// that leaves the kernel for a layer above it, printing the whole chain as "pkg -> pkg".
func TestKernelImportsNothingAboveIt(t *testing.T) {
	imports := listPackages(t)

	var roots []string
	for pkg := range imports {
		if inTree(pkg, kernelTree) {
			roots = append(roots, pkg)
		}
	}
	if len(roots) == 0 {
		t.Fatal("no packages found under " + kernelTree + "; the import graph was not walked")
	}

	// Sorting the roots makes the report deterministic, and makes the chain reported for a
	// shared offending edge the one starting at the outermost kernel package.
	sort.Strings(roots)

	seen := map[string]bool{}
	var violations []string
	for _, root := range roots {
		path := findForbidden(root, imports)
		if path == nil {
			continue
		}
		// Report each offending edge once, however many kernel packages reach it.
		edge := short(path[len(path)-2]) + " -> " + short(path[len(path)-1])
		if seen[edge] {
			continue
		}
		seen[edge] = true
		violations = append(violations, renderPath(path))
	}
	if len(violations) > 0 {
		t.Fatalf(
			"the kernel imports a layer above it — architecture §2.1 forbids this:\n\n%s\n\n"+
				"The kernel must not import internal/module, internal/api or internal/service.\n"+
				"Move the shared type down into the kernel, or invert the dependency behind a port\n"+
				"in internal/kernel/ports. Wiring belongs in cmd/lexicode and nowhere else.",
			strings.Join(violations, "\n"))
	}
}

// findForbidden returns the shortest import chain from root to a forbidden package, or nil.
func findForbidden(root string, imports map[string][]string) []string {
	type node struct {
		pkg  string
		path []string
	}
	seen := map[string]bool{root: true}
	queue := []node{{pkg: root, path: []string{root}}}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, imp := range imports[cur.pkg] {
			if seen[imp] {
				continue
			}
			seen[imp] = true
			path := append(append([]string(nil), cur.path...), imp)
			for _, bad := range forbidden {
				if inTree(imp, bad) {
					return path
				}
			}
			queue = append(queue, node{pkg: imp, path: path})
		}
	}
	return nil
}

// listPackages returns first-party package → its first-party imports, from the real build.
func listPackages(t *testing.T) map[string][]string {
	t.Helper()

	// "./..." is relative to this package's directory, so the roots of the walk are the kernel
	// tree; -deps adds everything they reach, which is where a violation would be visible.
	cmd := exec.Command(goTool(t), "list", "-deps", "-json", "./...")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list -deps -json ./...: %v\n%s", err, stderr.String())
	}

	imports := map[string][]string{}
	dec := json.NewDecoder(&stdout)
	for {
		var pkg struct {
			ImportPath string
			Imports    []string
		}
		if err := dec.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		if !inTree(pkg.ImportPath, modulePath) {
			continue // stdlib and third-party packages cannot import this module
		}
		var first []string
		for _, imp := range pkg.Imports {
			if inTree(imp, modulePath) {
				first = append(first, imp)
			}
		}
		imports[pkg.ImportPath] = first
	}
	return imports
}

// goTool finds the go binary, which is how the test reads the build's own view of the graph
// rather than re-implementing package resolution.
func goTool(t *testing.T) string {
	t.Helper()
	if path, err := exec.LookPath("go"); err == nil {
		return path
	}
	// runtime.GOROOT is deprecated (meaningless for a copied binary); the environment variable
	// is the remaining honest signal when go is not on PATH.
	if root := os.Getenv("GOROOT"); root != "" {
		candidate := filepath.Join(root, "bin", "go")
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate
		}
	}
	t.Fatal("no go binary on PATH or in GOROOT; the import-graph test cannot run")
	return ""
}

func inTree(pkg, tree string) bool {
	return pkg == tree || strings.HasPrefix(pkg, tree+"/")
}

func renderPath(path []string) string {
	parts := make([]string, 0, len(path)-1)
	for i := 0; i+1 < len(path); i++ {
		parts = append(parts, fmt.Sprintf("  %s -> %s", short(path[i]), short(path[i+1])))
	}
	return strings.Join(parts, "\n")
}

// short trims the module path so that the failure reads as the layers, not as a URL.
func short(pkg string) string { return strings.TrimPrefix(pkg, modulePath+"/") }
