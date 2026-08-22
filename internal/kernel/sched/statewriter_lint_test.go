package sched_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlyTheSchedulerWritesRunState is data model invariant 4 as a source lint: the store's
// run-state writers (SetState, SetHoldReason, SetProvisioned — the runstate.go surface) may
// be called only from kernel/sched (and defined in kernel/store). A compiler cannot enforce
// "who may call whom" across packages; this test is the rule's defence, the same way the
// import-graph test defends architecture §2.1. If it fails, route the state change through
// the scheduler — never add an exception here.
func TestOnlyTheSchedulerWritesRunState(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	writers := []string{".SetState(", ".SetHoldReason(", ".SetProvisioned("}
	allowed := func(path string) bool {
		return strings.Contains(path, filepath.Join("internal", "kernel", "sched")) ||
			strings.Contains(path, filepath.Join("internal", "kernel", "store"))
	}

	var scanned int
	var violations []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := d.Name()
			if base == "node_modules" || base == ".git" || base == "web" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++
		if allowed(path) {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(src)
		for _, w := range writers {
			if strings.Contains(text, w) {
				rel, _ := filepath.Rel(root, path)
				violations = append(violations, rel+" calls "+strings.Trim(w, ".("))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned == 0 {
		t.Fatal("no Go files scanned; the lint is not looking anywhere")
	}
	if len(violations) > 0 {
		t.Errorf("run state is written outside kernel/sched (data model invariant 4):\n  %s",
			strings.Join(violations, "\n  "))
	}
}
