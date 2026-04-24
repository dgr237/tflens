package diff

import (
	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/loader"
)

// WhatifResult is the per-call result of a whatif simulation: the
// original loader.ModuleCallPair plus the two pieces of evidence the
// command surfaces — DirectImpact (would the OLD parent's usage break
// under the NEW child's API?) and APIChanges (the full Diff between
// OLD and NEW child for context).
//
// One of the two will often be empty:
//   - DirectImpact is empty when the parent's usage cross-validates
//     cleanly against the new child (the upgrade is consumer-safe).
//   - APIChanges is empty when there's no NEW child to diff against
//     (the call was removed) or no OLD side to diff from.
type WhatifResult struct {
	Pair         loader.ModuleCallPair
	DirectImpact []analysis.ValidationError
	APIChanges   []Change
}

// BuildWhatifResult populates a WhatifResult for one paired call.
// Skips DirectImpact computation when the call was removed (no new
// child to validate against) or when the old parent isn't available
// (e.g. a nested call whose parent was itself added in the new tree).
// Always populates APIChanges when both sides have a child to diff,
// even if DirectImpact is also populated — the caller may want both
// the focused "your usage breaks" answer AND the full API diff for
// context.
func BuildWhatifResult(p loader.ModuleCallPair) WhatifResult {
	r := WhatifResult{Pair: p}
	if p.Status == loader.StatusRemoved {
		return r
	}
	if p.NewNode == nil || p.OldParent == nil {
		// No child API available OR no old parent to cross-validate
		// against. Still emit the API diff if both sides resolved.
		if p.OldNode != nil && p.NewNode != nil {
			r.APIChanges = Diff(p.OldNode.Module, p.NewNode.Module)
		}
		return r
	}
	r.DirectImpact = loader.CrossValidateCall(p.OldParent.Module, p.LocalName, p.NewNode.Module)
	if p.OldNode != nil {
		r.APIChanges = Diff(p.OldNode.Module, p.NewNode.Module)
	}
	return r
}
