package diff

import (
	"strings"

	"github.com/dgr237/tflens/pkg/loader"
)

// PairResult is the per-module-call result of a project diff:
// the original loader.ModuleCallPair plus the diff.Change list this
// package produced for it.
//
// For added/removed pairs, Changes is empty — those are reported
// structurally based on Pair.Status. For changed pairs, Changes
// contains whatever Diff() / DiffTrackedCtx() / consumption checks
// produced.
type PairResult struct {
	Pair    loader.ModuleCallPair
	Changes []Change
}

// HasContentChanges reports whether the diff produced any per-change
// detail (i.e. there's something to render under this pair's heading).
func (r PairResult) HasContentChanges() bool { return len(r.Changes) > 0 }

// AttrsChanged reports whether the call's source or version
// attributes differ between sides — useful for deciding whether to
// surface a "source X → Y" line in the rendered output even when
// there are no per-change details.
func (r PairResult) AttrsChanged() bool {
	return r.Pair.OldSource != r.Pair.NewSource || r.Pair.OldVersion != r.Pair.NewVersion
}

// Interesting reports whether the result is worth rendering at all.
// Added/removed calls are always interesting (their existence itself
// is the news). Changed calls are interesting when they have content
// changes or differing attrs.
func (r PairResult) Interesting() bool {
	switch r.Pair.Status {
	case loader.StatusAdded, loader.StatusRemoved:
		return true
	default:
		return r.HasContentChanges() || r.AttrsChanged()
	}
}

// ExitCodeFor maps a count of Breaking changes to a CLI exit code:
// non-zero when any Breaking changes exist (suitable for CI gating),
// zero otherwise.
func ExitCodeFor(breaking int) int {
	if breaking > 0 {
		return 1
	}
	return 0
}

// ConsumptionChangesForLocal turns cross_validate findings against
// the new parent + new child into diff.Change entries. Used in place
// of Diff() for local-source ("internal") children, where the child's
// API is implementation detail and only the parent's consumption is
// observable.
//
// Returns an empty slice when the parent's usage is consistent — i.e.
// every required child variable is passed, no unknown args, types
// compatible, and every module.<name>.<output> reference still
// resolves. Each emitted Change carries a fix hint derived from the
// underlying validation message via HintForCrossValidateMsg.
//
// Returns nil when p.NewParent or p.NewNode is missing — the caller
// has no parent or child to validate against.
func ConsumptionChangesForLocal(p loader.ModuleCallPair) []Change {
	if p.NewParent == nil || p.NewNode == nil {
		return nil
	}
	cvErrs := loader.CrossValidateCall(p.NewParent.Module, p.LocalName, p.NewNode.Module)
	if len(cvErrs) == 0 {
		return nil
	}
	out := make([]Change, 0, len(cvErrs))
	for _, e := range cvErrs {
		out = append(out, Change{
			Kind:    Breaking,
			Subject: e.EntityID,
			Detail:  e.Msg,
			Hint:    HintForCrossValidateMsg(e.Msg),
			NewPos:  e.Pos,
		})
	}
	return out
}

// HintForCrossValidateMsg returns a one-line "how to fix this" hint
// based on the shape of a cross_validate error message. Recognises
// the four error templates emitted by loader/cross_validate.go.
// Returns "" when no template matches — the caller can drop the hint
// rather than show an empty one.
func HintForCrossValidateMsg(msg string) string {
	switch {
	case strings.Contains(msg, "unknown argument"):
		return "remove the argument from the module block, or restore the matching variable in the child"
	case strings.Contains(msg, "required input"):
		return "add the input to the module block, or give the child variable a default"
	case strings.Contains(msg, "but child variable expects"):
		return "convert the value to the expected type (tostring/tolist/...) or change the parent's expression"
	case strings.Contains(msg, "no such output"):
		return "restore the output in the child, or remove the parent's reference"
	}
	return ""
}
