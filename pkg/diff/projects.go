package diff

import (
	"sort"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/resolver"
)

// AnalyzeProjects produces the full set of diff findings for a
// (oldProj, newProj) pair: per-module-call results plus a list of
// changes for the root module (which isn't itself a call). The
// breakingCount tally is the CI-gating signal — non-zero means the
// caller should exit non-zero.
//
// Per-call results are sorted by Key for deterministic output. The
// rootChanges list is sorted by Kind then Subject. Local-source
// children get the consumer view (cross-validate against the new
// child); registry/git children get the full API diff. Tracked-
// attribute changes are layered on top of both, with the parent's
// call context plumbed through so a marker in a child can catch
// changes flowing through the parent.
func AnalyzeProjects(oldProj, newProj *loader.Project) (results []PairResult, rootChanges []Change, breakingCount int) {
	pairs := loader.PairModuleCalls(oldProj, newProj)
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Key < pairs[j].Key })

	results = make([]PairResult, 0, len(pairs))
	for _, p := range pairs {
		r := PairResult{Pair: p}
		if p.Status == loader.StatusChanged && p.OldNode != nil && p.NewNode != nil {
			r.Changes = changesForPair(p)
			for _, c := range r.Changes {
				if c.Kind == Breaking {
					breakingCount++
				}
			}
		}
		results = append(results, r)
	}

	oldRoot, newRoot := rootModuleOf(oldProj), rootModuleOf(newProj)
	rootChanges = Diff(oldRoot, newRoot)
	rootChanges = append(rootChanges, DiffTracked(oldRoot, newRoot)...)
	sort.Slice(rootChanges, func(i, j int) bool {
		if rootChanges[i].Kind != rootChanges[j].Kind {
			return rootChanges[i].Kind < rootChanges[j].Kind
		}
		return rootChanges[i].Subject < rootChanges[j].Subject
	})
	for _, c := range rootChanges {
		if c.Kind == Breaking {
			breakingCount++
		}
	}
	return results, rootChanges, breakingCount
}

// changesForPair runs the appropriate API diff for a single paired
// call (consumer view for local-source children, full API diff for
// registry/git) and layers cross-module tracked-attribute changes
// on top with the parent's call context.
func changesForPair(p loader.ModuleCallPair) []Change {
	var out []Change
	if resolver.IsLocalSource(p.NewSource) {
		out = ConsumptionChangesForLocal(p)
	} else {
		out = Diff(p.OldNode.Module, p.NewNode.Module)
	}
	out = append(out, DiffTrackedCtx(p.OldNode.Module, p.NewNode.Module, TrackedContext{
		OldParent: parentModuleOf(p.OldParent),
		NewParent: parentModuleOf(p.NewParent),
		CallName:  p.LocalName,
	})...)
	return out
}

// AnalyzeWhatif produces the whatif simulation results for every
// paired module call. When only is non-empty it filters to the
// matching call (by Key or LocalName); otherwise every paired call
// is simulated. Added calls are skipped (no base-side caller exists
// to validate against). Returns the per-call results plus the total
// number of DirectImpact findings — the CI-gating signal.
//
// Returns (nil, 0, true) when only is non-empty but no call matches;
// the caller can convert that into a user-facing error.
func AnalyzeWhatif(oldProj, newProj *loader.Project, only string) (calls []WhatifResult, totalImpact int, filteredOut bool) {
	pairs := loader.PairModuleCalls(oldProj, newProj)
	if only != "" {
		filtered := pairs[:0]
		for _, p := range pairs {
			if p.Key == only || p.LocalName == only {
				filtered = append(filtered, p)
			}
		}
		pairs = filtered
		if len(pairs) == 0 {
			return nil, 0, true
		}
	}

	for _, p := range pairs {
		if p.Status == loader.StatusAdded {
			continue
		}
		r := BuildWhatifResult(p)
		totalImpact += len(r.DirectImpact)
		calls = append(calls, r)
	}
	return calls, totalImpact, false
}

// rootModuleOf returns p.Root.Module if both are non-nil, otherwise
// nil. Diff and DiffTracked are nil-safe so this is mostly cosmetic
// — keeps the call sites flatter.
func rootModuleOf(p *loader.Project) *analysis.Module {
	if p == nil || p.Root == nil {
		return nil
	}
	return p.Root.Module
}

// parentModuleOf returns n.Module if non-nil, else nil. TrackedContext
// is nil-safe so this just spares the call sites a nil check.
func parentModuleOf(n *loader.ModuleNode) *analysis.Module {
	if n == nil {
		return nil
	}
	return n.Module
}
