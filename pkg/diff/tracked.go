package diff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dgr237/tflens/pkg/analysis"
)

// TrackedContext supplies the parent module's call context to the
// cross-module tracked-diff path. When a marker is on a child resource
// attribute that references `var.X`, the child module on its own can't
// tell what the parent passes for X; with TrackedContext, DiffTrackedCtx
// follows the chain into the parent's `module "<CallName>" { X = ... }`
// argument and any locals/vars it transitively references. The result
// is that a marker in a child catches changes flowing through the
// parent (a new conditional, a flipped variable default, a different
// local) the same way an in-module marker catches in-module changes.
type TrackedContext struct {
	OldParent *analysis.Module // parent at the base ref; nil disables cross-module resolution
	NewParent *analysis.Module // parent at the working tree
	CallName  string           // local name of the call inside the parent (e.g. "eks" for `module "eks" {}`)
}

// DiffTracked compares the `# tflens:track` annotations in two module
// versions and reports every change as a separate diff.Change. It is
// additive to the regular Diff: the regular pass focuses on the public
// API surface (variables/outputs/etc), while tracked attributes let
// authors opt in to surface specific resource attribute changes that
// the API diff intentionally ignores (engine versions, instance classes,
// kms key references, …).
//
// Three kinds of change are reported:
//
//   - Marker removed: the annotation existed in old but is gone in new.
//     Always Breaking — silently dropping the safety guard is the exact
//     thing the marker is meant to prevent.
//   - Marker added: present in new only. Informational on its own; if
//     the underlying value moved (directly or via any transitively-
//     referenced var/local), promoted to Breaking.
//   - Marker present in both: compare the attribute's canonical text and
//     the canonical text of every transitively-referenced variable
//     default and local value. Any difference is Breaking.
//
// The marker's free-text description (after `tflens:track:`) becomes the
// Change's Hint, so authors can attach domain context (e.g. "EKS cluster
// version: bump only after add-on compatibility check").
//
// For cross-module resolution (marker in a child, change in the parent),
// use DiffTrackedCtx with the parent modules supplied.
func DiffTracked(oldMod, newMod *analysis.Module) []Change {
	return DiffTrackedCtx(oldMod, newMod, TrackedContext{})
}

// DiffTrackedCtx is DiffTracked with optional parent context. When ctx
// supplies parent modules + a CallName, child variable references are
// resolved through the parent's module call argument, and any vars or
// locals that argument transitively references on the parent's side
// are added to the comparison under "parent." prefixed keys.
func DiffTrackedCtx(oldMod, newMod *analysis.Module, ctx TrackedContext) []Change {
	oldTracked := expandThroughParent(indexTracked(oldMod), ctx.OldParent, ctx.CallName)
	newTracked := expandThroughParent(indexTracked(newMod), ctx.NewParent, ctx.CallName)

	keys := map[string]struct{}{}
	for k := range oldTracked {
		keys[k] = struct{}{}
	}
	for k := range newTracked {
		keys[k] = struct{}{}
	}

	var changes []Change
	for key := range keys {
		o, hasOld := oldTracked[key]
		n, hasNew := newTracked[key]
		switch {
		case !hasNew:
			changes = append(changes, Change{
				Kind:    Breaking,
				Subject: key,
				Detail:  "tracked-attribute marker removed (the safety guard is gone)",
				Hint:    "restore the `# tflens:track` comment, or remove the attribute entirely if the resource is gone",
				OldPos:  o.Pos,
			})
		case !hasOld:
			// Adding a marker registers an attribute for future
			// tracking — Informational on its own. But the most common
			// real-world flow is "I'm calling out THIS specific change
			// in THIS PR" — so if the underlying value also moved, the
			// reviewer needs the Breaking signal too.
			//
			// Two paths to detect a value change:
			//
			//   a) Direct attribute lookup. Works for entities where
			//      the attribute expression is cached on the Entity
			//      (locals, outputs, variable defaults, module args).
			//      Resource/data attributes aren't cached, so this
			//      step yields nothing for those — we fall through to
			//      the indirection check.
			//
			//   b) Per-ref comparison of transitively-referenced vars
			//      and locals. Catches the case where the marker is
			//      on a resource attribute (which we can't diff
			//      directly) but the underlying local changed or a
			//      newly-introduced variable took effect.
			var details []string
			if oldText, located := oldMod.LookupAttrText(n.EntityID, n.AttrName); located && oldText != n.ExprText {
				details = append(details, fmt.Sprintf("value %s → %s", display(oldText), display(n.ExprText)))
			}
			for _, id := range n.SortedRefIDs() {
				newRefText := n.Refs[id]
				oldRefText, oldLocated := refValueOldSide(id, oldMod, ctx.OldParent, ctx.CallName)
				switch {
				case !oldLocated:
					details = append(details, fmt.Sprintf("now references %s = %s", id, display(newRefText)))
				case oldRefText != newRefText:
					details = append(details, fmt.Sprintf("%s changed: %s → %s", id, display(oldRefText), display(newRefText)))
				}
			}
			if len(details) > 0 {
				details = append([]string{"tracked-attribute marker added"}, details...)
				changes = append(changes, Change{
					Kind:    Breaking,
					Subject: key,
					Detail:  strings.Join(details, "; "),
					Hint:    n.Description,
					NewPos:  n.Pos,
				})
			} else {
				changes = append(changes, Change{
					Kind:    Informational,
					Subject: key,
					Detail:  "tracked-attribute marker added",
					Hint:    n.Description,
					NewPos:  n.Pos,
				})
			}
		default:
			diffs := compareTracked(o, n)
			if len(diffs) == 0 {
				continue
			}
			changes = append(changes, Change{
				Kind:    Breaking,
				Subject: key,
				Detail:  strings.Join(diffs, "; "),
				Hint:    n.Description,
				OldPos:  o.Pos,
				NewPos:  n.Pos,
			})
		}
	}

	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Kind != changes[j].Kind {
			return changes[i].Kind < changes[j].Kind
		}
		return changes[i].Subject < changes[j].Subject
	})
	return changes
}

// refValueOldSide looks up a ref id in the old child module, with
// optional fall-through to the parent's call argument:
//
//   - id == "parent.<inner>": recurse with parent as the lookup module,
//     so "parent.local.X" / "parent.variable.X" route to the parent.
//   - id == "variable.X" with parent context supplied: prefer the
//     parent's `module "<callName>" { X = ... }` argument expression
//     over the child variable's own default — that's what the child
//     actually receives at instantiation.
//   - everything else: standard child-namespace lookup.
//
// Returns ("", false) when nothing matches. ("", true) means the entity
// exists but has no value (e.g. variable with no default), which the
// caller treats as "ref existed before, no change to surface".
func refValueOldSide(id string, child, parent *analysis.Module, callName string) (string, bool) {
	if strings.HasPrefix(id, "parent.") {
		if parent == nil {
			return "", false
		}
		return refValueOldSide(strings.TrimPrefix(id, "parent."), parent, nil, "")
	}
	if strings.HasPrefix(id, "variable.") && parent != nil && callName != "" {
		varName := strings.TrimPrefix(id, "variable.")
		for _, e := range parent.Filter(analysis.KindModule) {
			if e.Name != callName {
				continue
			}
			if argExpr, ok := e.ModuleArgs[varName]; ok {
				return argExpr.Text(), true
			}
			break
		}
	}
	switch {
	case strings.HasPrefix(id, "variable."):
		return child.LookupAttrText(id, "default")
	case strings.HasPrefix(id, "local."):
		return child.LookupAttrText(id, "value")
	}
	return "", false
}

// expandThroughParent rewrites each tracked attribute's Refs map to
// inject parent-context entries when the marker is in a child module
// and the parent module's call arguments are available. For every
// `variable.X` ref where the parent passes an expression for X:
//
//   - The Refs[variable.X] value is replaced with the parent's argument
//     expression text (this is what the child actually receives).
//   - Every transitively-referenced var/local in the parent's
//     expression is added under "parent.<id>" so the comparison fires
//     when the parent's locals or variable defaults change underneath
//     the call.
//
// With nil parent or empty callName, returns the input unchanged.
func expandThroughParent(tracked map[string]analysis.TrackedAttribute, parent *analysis.Module, callName string) map[string]analysis.TrackedAttribute {
	if parent == nil || callName == "" {
		return tracked
	}
	var callEntity analysis.Entity
	var found bool
	for _, e := range parent.Filter(analysis.KindModule) {
		if e.Name == callName {
			callEntity = e
			found = true
			break
		}
	}
	if !found || callEntity.ModuleArgs == nil {
		return tracked
	}
	out := make(map[string]analysis.TrackedAttribute, len(tracked))
	for k, t := range tracked {
		expanded := make(map[string]string, len(t.Refs))
		for id, val := range t.Refs {
			expanded[id] = val
		}
		for id := range t.Refs {
			if !strings.HasPrefix(id, "variable.") {
				continue
			}
			varName := strings.TrimPrefix(id, "variable.")
			argExpr, ok := callEntity.ModuleArgs[varName]
			if !ok {
				continue
			}
			expanded[id] = argExpr.Text()
			for refID, refVal := range parent.GatherRefsFromExpr(argExpr) {
				expanded["parent."+refID] = refVal
			}
		}
		t.Refs = expanded
		out[k] = t
	}
	return out
}

func indexTracked(m *analysis.Module) map[string]analysis.TrackedAttribute {
	out := map[string]analysis.TrackedAttribute{}
	if m == nil {
		return out
	}
	for _, t := range m.TrackedAttributes() {
		out[t.Key()] = t
	}
	return out
}

// compareTracked returns one diff string per changed surface (the
// attribute itself, plus each transitively-referenced var/local). Empty
// when nothing changed. Ordered: attribute first, then refs sorted by ID.
func compareTracked(o, n analysis.TrackedAttribute) []string {
	var diffs []string
	if o.ExprText != n.ExprText {
		diffs = append(diffs, fmt.Sprintf("value %s → %s", display(o.ExprText), display(n.ExprText)))
	}
	refIDs := unionSortedRefIDs(o.Refs, n.Refs)
	for _, id := range refIDs {
		ov, oOK := o.Refs[id]
		nv, nOK := n.Refs[id]
		switch {
		case oOK && !nOK:
			diffs = append(diffs, fmt.Sprintf("no longer references %s", id))
		case !oOK && nOK:
			diffs = append(diffs, fmt.Sprintf("now references %s = %s", id, display(nv)))
		case ov != nv:
			diffs = append(diffs, fmt.Sprintf("%s changed: %s → %s", id, display(ov), display(nv)))
		}
	}
	return diffs
}

func unionSortedRefIDs(a, b map[string]string) []string {
	seen := map[string]struct{}{}
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// display returns the value text as-is for display. Empty strings show as
// `<unset>` to disambiguate from a literal "" expression. The text from
// analysis.Expr.Text() already preserves HCL syntax (string literals
// arrive with their surrounding quotes), so additional escaping here
// would just double-quote.
func display(s string) string {
	if s == "" {
		return "<unset>"
	}
	return s
}
