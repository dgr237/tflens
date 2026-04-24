package diff_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
)

// TestBuildWhatifResultRemovedSkipsCrossValidate: a removed call has
// no new child, so DirectImpact and APIChanges should both be empty —
// the cmd layer reports it structurally instead.
func TestBuildWhatifResultRemovedSkipsCrossValidate(t *testing.T) {
	r := diff.BuildWhatifResult(loader.ModuleCallPair{
		Key:    "vpc",
		Status: loader.StatusRemoved,
	})
	if len(r.DirectImpact) != 0 {
		t.Errorf("removed call: DirectImpact = %v, want empty", r.DirectImpact)
	}
	if len(r.APIChanges) != 0 {
		t.Errorf("removed call: APIChanges = %v, want empty", r.APIChanges)
	}
}

// TestBuildWhatifResultMissingChildSkipsCrossValidate: when the new
// side has no resolved child (e.g. --offline against a registry source
// not in cache), DirectImpact should be skipped — there's nothing to
// validate against.
func TestBuildWhatifResultMissingChildSkipsCrossValidate(t *testing.T) {
	parent := loadModuleNodeOrFail(t, `
module "x" {
  source = "registry/x/aws"
}
`)
	r := diff.BuildWhatifResult(loader.ModuleCallPair{
		Key:       "x",
		LocalName: "x",
		Status:    loader.StatusChanged,
		OldParent: parent,
		// NewNode intentionally nil
	})
	if len(r.DirectImpact) != 0 {
		t.Errorf("missing new child: DirectImpact = %v, want empty", r.DirectImpact)
	}
	if len(r.APIChanges) != 0 {
		t.Errorf("missing new child: APIChanges = %v, want empty", r.APIChanges)
	}
}

// TestBuildWhatifResultMissingOldParentStillEmitsAPIDiff: when the
// new tree added the parent of a nested call, there's no old parent
// to cross-validate against — but if both children resolved, we can
// still surface the API diff for context.
func TestBuildWhatifResultMissingOldParentStillEmitsAPIDiff(t *testing.T) {
	oldChild := loadModuleNodeOrFail(t, `
variable "old_only" {
  type = string
}
`)
	newChild := loadModuleNodeOrFail(t, `
variable "new_only" {
  type = string
}
`)
	r := diff.BuildWhatifResult(loader.ModuleCallPair{
		Key:       "x",
		LocalName: "x",
		Status:    loader.StatusChanged,
		OldNode:   oldChild,
		NewNode:   newChild,
		// OldParent intentionally nil
	})
	if len(r.DirectImpact) != 0 {
		t.Errorf("no old parent: DirectImpact should be skipped, got %v", r.DirectImpact)
	}
	if len(r.APIChanges) == 0 {
		t.Errorf("no old parent but both children present: APIChanges should still surface, got empty")
	}
}

// TestBuildWhatifResultBreakingChildCallerMissesNewArg: the realistic
// case — OLD parent passes input "x", NEW child requires "y".
// Cross-validate fires: required input missing + unknown argument.
func TestBuildWhatifResultBreakingChildCallerMissesNewArg(t *testing.T) {
	oldParent := loadModuleNodeOrFail(t, `
module "child" {
  source = "./child"
  x      = "value"
}
`)
	newChild := loadModuleNodeOrFail(t, `
variable "y" {
  type = string
}
`)
	r := diff.BuildWhatifResult(loader.ModuleCallPair{
		Key:       "child",
		LocalName: "child",
		Status:    loader.StatusChanged,
		OldParent: oldParent,
		NewNode:   newChild,
	})
	if len(r.DirectImpact) == 0 {
		t.Fatalf("expected DirectImpact entries (parent passes unknown arg + misses required input), got none")
	}
	// Should see at least the two distinct errors.
	saw := map[string]bool{}
	for _, e := range r.DirectImpact {
		switch {
		case containsAll(e.Msg, "unknown argument", "x"):
			saw["unknown"] = true
		case containsAll(e.Msg, "required input", "y"):
			saw["required"] = true
		}
	}
	if !saw["unknown"] {
		t.Errorf("missing unknown-argument error; got %+v", r.DirectImpact)
	}
	if !saw["required"] {
		t.Errorf("missing required-input error; got %+v", r.DirectImpact)
	}
}

// TestBuildWhatifResultCleanUpgradeProducesNoDirectImpact: the
// "consumer-safe" case — parent's argument set lines up exactly with
// the new child's variable set, even though the API itself changed.
func TestBuildWhatifResultCleanUpgradeProducesNoDirectImpact(t *testing.T) {
	oldParent := loadModuleNodeOrFail(t, `
module "child" {
  source = "./child"
  x      = "value"
}
`)
	newChild := loadModuleNodeOrFail(t, `
variable "x" {
  type    = string
  default = "new-default"
}
`)
	r := diff.BuildWhatifResult(loader.ModuleCallPair{
		Key:       "child",
		LocalName: "child",
		Status:    loader.StatusChanged,
		OldParent: oldParent,
		NewNode:   newChild,
	})
	if len(r.DirectImpact) != 0 {
		t.Errorf("clean upgrade: DirectImpact should be empty, got %+v", r.DirectImpact)
	}
}

// ---- helpers ----

// loadModuleNodeOrFail builds a single-file ModuleNode from the given
// source. The Children map is empty.
func loadModuleNodeOrFail(t *testing.T, src string) *loader.ModuleNode {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	mod, _, err := loader.LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	return &loader.ModuleNode{
		Dir:      dir,
		Module:   mod,
		Children: map[string]*loader.ModuleNode{},
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
