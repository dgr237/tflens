package diff_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
)

// whatifFixtureDir returns the absolute path to
// pkg/diff/testdata/whatif/<case>/<side>. Returns "" when the side
// directory doesn't exist (e.g. cases that only need a parent, only
// need a new child, or no fixtures at all).
func whatifFixtureDir(t *testing.T, name, side string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	abs, err := filepath.Abs(filepath.Join(filepath.Dir(file), "testdata", "whatif", name, side))
	if err != nil {
		t.Fatalf("resolving fixture %s/%s: %v", name, side, err)
	}
	if _, err := os.Stat(abs); os.IsNotExist(err) {
		return ""
	}
	return abs
}

// loadWhatifNode loads pkg/diff/testdata/whatif/<case>/<side>/ as a
// single-file ModuleNode. Returns nil when the side dir doesn't exist
// — lets cases that only need a subset of the (parent / old_child /
// new_child) trio simply omit the unused directories.
func loadWhatifNode(t *testing.T, casename, side string) *loader.ModuleNode {
	t.Helper()
	dir := whatifFixtureDir(t, casename, side)
	if dir == "" {
		return nil
	}
	mod, _, err := loader.LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir(%s/%s): %v", casename, side, err)
	}
	return &loader.ModuleNode{
		Dir:      dir,
		Module:   mod,
		Children: map[string]*loader.ModuleNode{},
	}
}

// whatifCase pairs a fixture set with assertions on
// diff.BuildWhatifResult. The Pair lambda receives the three loaded
// sides (parent, old_child, new_child — any may be nil) and builds
// the ModuleCallPair the case wants to feed into BuildWhatifResult.
type whatifCase struct {
	Name   string
	Pair   func(parent, oldChild, newChild *loader.ModuleNode) loader.ModuleCallPair
	Custom func(t *testing.T, r diff.WhatifResult)
}

func TestBuildWhatifResultCases(t *testing.T) {
	for _, tc := range whatifCases {
		t.Run(tc.Name, func(t *testing.T) {
			parent := loadWhatifNode(t, tc.Name, "parent")
			oldChild := loadWhatifNode(t, tc.Name, "old_child")
			newChild := loadWhatifNode(t, tc.Name, "new_child")
			r := diff.BuildWhatifResult(tc.Pair(parent, oldChild, newChild))
			tc.Custom(t, r)
		})
	}
}

var whatifCases = []whatifCase{
	{
		// A removed call has no new child, so DirectImpact and
		// APIChanges should both be empty — the cmd layer reports it
		// structurally instead. No fixtures.
		Name: "removed_skips_cross_validate",
		Pair: func(_, _, _ *loader.ModuleNode) loader.ModuleCallPair {
			return loader.ModuleCallPair{Key: "vpc", Status: loader.StatusRemoved}
		},
		Custom: func(t *testing.T, r diff.WhatifResult) {
			if len(r.DirectImpact) != 0 {
				t.Errorf("removed call: DirectImpact = %v, want empty", r.DirectImpact)
			}
			if len(r.APIChanges) != 0 {
				t.Errorf("removed call: APIChanges = %v, want empty", r.APIChanges)
			}
		},
	},
	{
		// When the new side has no resolved child (e.g. --offline
		// against a registry source not in cache), DirectImpact should
		// be skipped — there's nothing to validate against.
		Name: "missing_child_skips_cross_validate",
		Pair: func(parent, _, _ *loader.ModuleNode) loader.ModuleCallPair {
			return loader.ModuleCallPair{
				Key:       "x",
				LocalName: "x",
				Status:    loader.StatusChanged,
				OldParent: parent,
				// NewNode intentionally nil
			}
		},
		Custom: func(t *testing.T, r diff.WhatifResult) {
			if len(r.DirectImpact) != 0 {
				t.Errorf("missing new child: DirectImpact = %v, want empty", r.DirectImpact)
			}
			if len(r.APIChanges) != 0 {
				t.Errorf("missing new child: APIChanges = %v, want empty", r.APIChanges)
			}
		},
	},
	{
		// When the new tree added the parent of a nested call, there's
		// no old parent to cross-validate against — but if both
		// children resolved, we can still surface the API diff for
		// context.
		Name: "missing_old_parent_still_emits_api_diff",
		Pair: func(_, oldChild, newChild *loader.ModuleNode) loader.ModuleCallPair {
			return loader.ModuleCallPair{
				Key:       "x",
				LocalName: "x",
				Status:    loader.StatusChanged,
				OldNode:   oldChild,
				NewNode:   newChild,
				// OldParent intentionally nil
			}
		},
		Custom: func(t *testing.T, r diff.WhatifResult) {
			if len(r.DirectImpact) != 0 {
				t.Errorf("no old parent: DirectImpact should be skipped, got %v", r.DirectImpact)
			}
			if len(r.APIChanges) == 0 {
				t.Errorf("no old parent but both children present: APIChanges should still surface, got empty")
			}
		},
	},
	{
		// The realistic case — OLD parent passes input "x", NEW child
		// requires "y". Cross-validate fires: required input missing
		// + unknown argument.
		Name: "breaking_child_caller_misses_new_arg",
		Pair: func(parent, _, newChild *loader.ModuleNode) loader.ModuleCallPair {
			return loader.ModuleCallPair{
				Key:       "child",
				LocalName: "child",
				Status:    loader.StatusChanged,
				OldParent: parent,
				NewNode:   newChild,
			}
		},
		Custom: func(t *testing.T, r diff.WhatifResult) {
			if len(r.DirectImpact) == 0 {
				t.Fatalf("expected DirectImpact entries (parent passes unknown arg + misses required input), got none")
			}
			saw := map[string]bool{}
			for _, e := range r.DirectImpact {
				switch {
				case strings.Contains(e.Msg, "unknown argument") && strings.Contains(e.Msg, "x"):
					saw["unknown"] = true
				case strings.Contains(e.Msg, "required input") && strings.Contains(e.Msg, "y"):
					saw["required"] = true
				}
			}
			if !saw["unknown"] {
				t.Errorf("missing unknown-argument error; got %+v", r.DirectImpact)
			}
			if !saw["required"] {
				t.Errorf("missing required-input error; got %+v", r.DirectImpact)
			}
		},
	},
	{
		// The "consumer-safe" case — parent's argument set lines up
		// exactly with the new child's variable set, even though the
		// API itself changed.
		Name: "clean_upgrade_no_direct_impact",
		Pair: func(parent, _, newChild *loader.ModuleNode) loader.ModuleCallPair {
			return loader.ModuleCallPair{
				Key:       "child",
				LocalName: "child",
				Status:    loader.StatusChanged,
				OldParent: parent,
				NewNode:   newChild,
			}
		},
		Custom: func(t *testing.T, r diff.WhatifResult) {
			if len(r.DirectImpact) != 0 {
				t.Errorf("clean upgrade: DirectImpact should be empty, got %+v", r.DirectImpact)
			}
		},
	},
}
