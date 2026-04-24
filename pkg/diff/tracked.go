package diff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dgr237/tflens/pkg/analysis"
)

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
//   - Marker added: present in new only. Informational.
//   - Marker present in both: compare the attribute's canonical text and
//     the canonical text of every transitively-referenced variable
//     default and local value. Any difference is Breaking.
//
// The marker's free-text description (after `tflens:track:`) becomes the
// Change's Hint, so authors can attach domain context (e.g. "EKS cluster
// version: bump only after add-on compatibility check").
func DiffTracked(oldMod, newMod *analysis.Module) []Change {
	oldTracked := indexTracked(oldMod)
	newTracked := indexTracked(newMod)

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
			oldText, located := oldMod.LookupAttrText(n.EntityID, n.AttrName)
			if located && oldText != n.ExprText {
				changes = append(changes, Change{
					Kind:    Breaking,
					Subject: key,
					Detail:  fmt.Sprintf("tracked-attribute marker added; value %s → %s", display(oldText), display(n.ExprText)),
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
