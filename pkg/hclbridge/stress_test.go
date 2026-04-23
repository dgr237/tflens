package hclbridge_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/hclbridge"
	"github.com/dgr237/tflens/pkg/loader"
)

// TestStressAgainstExternalRepo compares every module directory under
// $TFLENS_STRESS_ROOT between the hand-rolled and hcl2 paths. Skips silently
// when the env var is unset.
//
// Usage:
//
//	TFLENS_STRESS_ROOT=/tmp/terraform-aws-eks go test ./pkg/hclbridge -run Stress -v
func TestStressAgainstExternalRepo(t *testing.T) {
	root := os.Getenv("TFLENS_STRESS_ROOT")
	if root == "" {
		t.Skip("TFLENS_STRESS_ROOT not set; skipping stress test")
	}
	dirs, err := findModuleDirs(root)
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(dirs) == 0 {
		t.Fatalf("no .tf-containing directories under %s", root)
	}

	var (
		totalDirs       = len(dirs)
		oldOnly         []string
		newOnly         []string
		bothFailed      []string
		entityMismatch  []string
		depMismatch     []string
		valErrMismatch  []string
		typeErrMismatch []string
		allMatched      int
		totalEntities   int
		totalDeps       int
		totalValErrs    int
	)

	for _, d := range dirs {
		rel, _ := filepath.Rel(root, d)

		// Stderr so each line flushes — t.Logf buffers until the test ends
		// and we want to see which dir hangs if one does.
		fmt.Fprintf(os.Stderr, "STRESS: %-45s  ", rel)

		tNew := time.Now()
		newRes, newErr := hclbridge.LoadGraph(d)
		newDur := time.Since(tNew)
		fmt.Fprintf(os.Stderr, "new=%-6s ", newDur.Round(time.Millisecond))

		tOld := time.Now()
		oldMod, oldErr := loadOldDirWithTimeout(rel, d, 10*time.Second)
		oldDur := time.Since(tOld)
		fmt.Fprintf(os.Stderr, "old=%-6s\n", oldDur.Round(time.Millisecond))

		switch {
		case oldErr != nil && newErr != nil:
			bothFailed = append(bothFailed, fmt.Sprintf("%s: old=%v new=%v", rel, oldErr, newErr))
			continue
		case oldErr != nil:
			newOnly = append(newOnly, fmt.Sprintf("%s (old: %v)", rel, oldErr))
			continue
		case newErr != nil:
			oldOnly = append(oldOnly, fmt.Sprintf("%s (new: %v)", rel, newErr))
			continue
		}

		ok := true
		oldCounts := countByKind(oldMod.Entities())
		newCounts := countByKind(newRes.Entities)
		if !countMapsEqual(oldCounts, newCounts) {
			entityMismatch = append(entityMismatch, fmt.Sprintf("%s: old=%v new=%v", rel, oldCounts, newCounts))
			ok = false
		}
		totalEntities += len(oldMod.Entities())

		oldDepCount := totalDepEdges(oldMod)
		newDepCount := totalDepEdgesMap(newRes.Dependencies)
		if oldDepCount != newDepCount {
			depMismatch = append(depMismatch, fmt.Sprintf("%s: old=%d new=%d", rel, oldDepCount, newDepCount))
			ok = false
		}
		totalDeps += oldDepCount

		oldVal := filterRefValErrs(oldMod.Validate())
		newVal := filterRefValErrs(newRes.ValErrors)
		if !valErrSetsEqual(oldVal, newVal) {
			valErrMismatch = append(valErrMismatch, fmt.Sprintf("%s: %s", rel, symDiff(oldVal, newVal)))
			ok = false
		}
		totalValErrs += len(oldVal)

		oldType := oldMod.TypeErrors()
		newType := newRes.TypeErrors
		if len(oldType) != len(newType) {
			typeErrMismatch = append(typeErrMismatch,
				fmt.Sprintf("%s: old=%d new=%d", rel, len(oldType), len(newType)))
			ok = false
		}

		if ok {
			allMatched++
		}
	}

	t.Logf("STRESS: %d directories scanned (root=%s)", totalDirs, root)
	t.Logf("STRESS: %d matched on entities+deps+valErrs+typeErrs", allMatched)
	t.Logf("STRESS: totals — entities=%d, dep edges=%d, undefined-ref errors=%d",
		totalEntities, totalDeps, totalValErrs)

	if len(bothFailed) > 0 {
		t.Logf("STRESS: both parsers failed on %d dirs:\n  %s", len(bothFailed),
			strings.Join(bothFailed, "\n  "))
	}
	if len(oldOnly) > 0 {
		t.Errorf("STRESS: bridge failed where old succeeded on %d dirs:\n  %s",
			len(oldOnly), strings.Join(oldOnly, "\n  "))
	}
	if len(newOnly) > 0 {
		t.Logf("STRESS: old failed where bridge succeeded on %d dirs (bridge wins):\n  %s",
			len(newOnly), strings.Join(newOnly, "\n  "))
	}
	if len(entityMismatch) > 0 {
		t.Errorf("STRESS: entity-count mismatch in %d dirs:\n  %s",
			len(entityMismatch), strings.Join(entityMismatch, "\n  "))
	}
	if len(depMismatch) > 0 {
		t.Errorf("STRESS: dep-edge-count mismatch in %d dirs:\n  %s",
			len(depMismatch), strings.Join(depMismatch, "\n  "))
	}
	if len(valErrMismatch) > 0 {
		t.Errorf("STRESS: validation-error-set mismatch in %d dirs:\n  %s",
			len(valErrMismatch), strings.Join(valErrMismatch, "\n  "))
	}
	if len(typeErrMismatch) > 0 {
		t.Logf("STRESS: type-error-count divergence in %d dirs (expected — see earlier map/object bug):\n  %s",
			len(typeErrMismatch), strings.Join(typeErrMismatch, "\n  "))
	}
}

func findModuleDirs(root string) ([]string, error) {
	seen := make(map[string]bool)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".terraform" || name == ".git" || (strings.HasPrefix(name, ".") && name != ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".tf") && !strings.HasSuffix(d.Name(), ".tftest.tf") {
			seen[filepath.Dir(path)] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}

func loadOldDir(label, dir string) (*analysis.Module, error) {
	mod, _, err := loader.LoadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%s: LoadDir: %w", label, err)
	}
	return mod, nil
}

// loadOldDirWithTimeout guards the hand-rolled loader against infinite loops
// — there is at least one known lexer bug (`$${...:...}` escapes) that hangs
// the current parser. Times out return an error so the stress test can
// continue past the hang rather than stalling.
func loadOldDirWithTimeout(label, dir string, timeout time.Duration) (*analysis.Module, error) {
	type result struct {
		mod *analysis.Module
		err error
	}
	done := make(chan result, 1)
	go func() {
		mod, err := loadOldDir(label, dir)
		done <- result{mod, err}
	}()
	select {
	case r := <-done:
		return r.mod, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("hand-rolled loader timed out after %s (hang in old parser)", timeout)
	}
}

func countByKind(es []analysis.Entity) map[analysis.EntityKind]int {
	out := make(map[analysis.EntityKind]int)
	for _, e := range es {
		out[e.Kind]++
	}
	return out
}

func countMapsEqual(a, b map[analysis.EntityKind]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func totalDepEdges(m *analysis.Module) int {
	n := 0
	for _, e := range m.Entities() {
		n += len(m.Dependencies(e.ID()))
	}
	return n
}

func totalDepEdgesMap(d map[string]map[string]bool) int {
	n := 0
	for _, set := range d {
		n += len(set)
	}
	return n
}

func filterRefValErrs(errs []analysis.ValidationError) map[valKey]bool {
	out := make(map[valKey]bool, len(errs))
	for _, e := range errs {
		if e.Ref == "" {
			continue
		}
		out[valKey{e.EntityID, e.Ref}] = true
	}
	return out
}

func valErrSetsEqual(a, b map[valKey]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func symDiff(a, b map[valKey]bool) string {
	var onlyA, onlyB []string
	for k := range a {
		if !b[k] {
			onlyA = append(onlyA, fmt.Sprintf("%s→%s", k.entity, k.ref))
		}
	}
	for k := range b {
		if !a[k] {
			onlyB = append(onlyB, fmt.Sprintf("%s→%s", k.entity, k.ref))
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	return fmt.Sprintf("only-old=%v only-new=%v", onlyA, onlyB)
}
