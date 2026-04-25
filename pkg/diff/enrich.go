package diff

import (
	"fmt"
	"sort"

	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/plan"
)

// SourceStatic is the Change.Source value for findings produced by
// the standard source-side text-diff machinery (variables/outputs/
// resources comparison, tracked-attribute eval, condition-set
// multiset). Empty Source on a Change is treated as Static — the
// field exists so plan-derived findings can be distinguished, not
// to make every existing emitter set it explicitly.
const SourceStatic = "static"

// SourcePlan is the Change.Source value for findings derived from a
// terraform plan JSON via EnrichFromPlan. Renderers use this to
// decorate plan-derived rows with a 📋 marker so reviewers can tell
// at a glance which findings came from static analysis vs which came
// from the plan output.
const SourcePlan = "plan"

// EnrichFromPlan augments the existing change list with plan-derived
// attribute deltas. Each ResourceChange in p whose action is anything
// other than no-op produces zero or more Change entries:
//
//   - update / replace: one entry per attribute that differs between
//     before and after. Force-new attributes (those in replace_paths)
//     get Kind=Breaking; other attribute changes get Kind=Informational.
//
//   - create / delete: a single summary entry noting the resource is
//     entering or leaving the plan, Kind=Informational. (The static
//     diff already catches resource adds/removes when the source files
//     differ; the plan-derived entry corroborates from the plan side
//     and helps when the source-side change is ambiguous.)
//
// Matching strategy: each ResourceChange's EntityID() is looked up in
// newProj's modules. The match key is the (module path, entity id)
// pair — `module.network.resource.aws_vpc.main` in the plan matches
// the entity `resource.aws_vpc.main` inside `module.network`'s child
// node. The resource's Subject in the resulting Change carries the
// full plan address so renderers can show the precise instance.
//
// count/for_each indices are NOT yet matched: a plan ResourceChange
// at `aws_subnet.foo[0]` matches the source-side entity
// `resource.aws_subnet.foo`, with the index preserved in the Subject
// for human reference. Index-aware matching (where each instance
// is a separate Change) is a follow-up.
//
// Renames via `moved {}` blocks are also out of scope here: a plan
// describing a destroy + create across a rename will surface as two
// separate Change entries (one delete, one create) rather than being
// recognised as the same logical resource. The existing static-diff
// rename detection in pkg/diff doesn't depend on this; it surfaces
// the rename from the source side regardless.
//
// Returns the merged change list, sorted by (Kind, Subject) so the
// output is deterministic and Breaking findings sort first regardless
// of source.
func EnrichFromPlan(changes []Change, p *plan.Plan, newProj *loader.Project) []Change {
	if p == nil {
		return changes
	}
	out := make([]Change, 0, len(changes)+len(p.ResourceChanges))
	out = append(out, changes...)

	// Build a lookup so we can confirm each plan entry corresponds to
	// a known source-side entity. Plan entries that match nothing
	// still get emitted — they're useful (e.g. detecting a resource
	// added to the plan from outside the diff'd source) — but the
	// entity-existence lookup tells the renderer not to assume a
	// source position for them.
	projectEntities := buildEntityIndex(newProj)

	for _, rc := range p.ResourceChanges {
		if rc.Change.IsNoOp() {
			continue
		}
		entityID := rc.EntityID()
		_, exists := projectEntities[matchKey(rc.ModuleAddress, entityID)]

		switch {
		case rc.Change.IsCreate():
			out = append(out, Change{
				Kind:    Informational,
				Subject: planSubject(rc),
				Detail:  fmt.Sprintf("plan creates %s%s", planAddressDescriptor(rc), entityHint(exists)),
				Source:  SourcePlan,
			})
		case rc.Change.IsDelete():
			out = append(out, Change{
				Kind:    Breaking,
				Subject: planSubject(rc),
				Detail:  fmt.Sprintf("plan destroys %s — uncommitted state will be lost", planAddressDescriptor(rc)),
				Source:  SourcePlan,
			})
		case rc.Change.IsReplace():
			// Replace = destroy + recreate. Already Breaking by
			// definition. Surface the per-attribute deltas
			// underneath so reviewers see WHICH change forced it.
			out = append(out, Change{
				Kind:    Breaking,
				Subject: planSubject(rc),
				Detail:  fmt.Sprintf("plan replaces %s (destroy + recreate)", planAddressDescriptor(rc)),
				Source:  SourcePlan,
			})
			out = append(out, attrDeltaChanges(rc)...)
		case rc.Change.IsUpdate():
			out = append(out, attrDeltaChanges(rc)...)
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Subject < out[j].Subject
	})
	return out
}

// attrDeltaChanges turns each AttrDelta on a ResourceChange into a
// diff.Change. ForceNew deltas → Breaking; everything else →
// Informational. Each Change carries Subject = "<plan-address>:<attr-path>"
// so the output is unique even when multiple attributes change on
// the same resource.
func attrDeltaChanges(rc plan.ResourceChange) []Change {
	deltas := rc.Change.AttrDeltas()
	out := make([]Change, 0, len(deltas))
	for _, d := range deltas {
		kind := Informational
		hint := ""
		if d.ForceNew {
			kind = Breaking
			hint = "this attribute forces a destroy + recreate; coordinate with the operator"
		}
		out = append(out, Change{
			Kind:    kind,
			Subject: fmt.Sprintf("%s:%s", rc.Address, d.Path),
			Detail:  fmt.Sprintf("plan attribute change: %s → %s", formatValue(d.Before), formatValue(d.After)),
			Hint:    hint,
			Source:  SourcePlan,
		})
	}
	return out
}

// planSubject returns the Subject for a top-level plan-derived Change
// (resource-level create/delete/replace summaries — NOT the per-
// attribute child rows from attrDeltaChanges). Just the full plan
// address; renderers can split it back into module/type/name if they
// want richer formatting.
func planSubject(rc plan.ResourceChange) string {
	return rc.Address
}

// planAddressDescriptor formats the address for inclusion in a Detail
// string — backtick-quoted so renderers that respect markdown render
// it as inline code.
func planAddressDescriptor(rc plan.ResourceChange) string {
	return fmt.Sprintf("`%s`", rc.Address)
}

// entityHint returns a parenthetical note when the plan describes a
// resource not present in the source-side analysis. Helps debugging
// stale plans (plan was generated against a different commit than
// the diff is now comparing).
func entityHint(exists bool) string {
	if exists {
		return ""
	}
	return " (no matching source-side entity — plan may be stale)"
}

// formatValue produces a compact human-readable rendering of a JSON
// value for inclusion in Detail. Strings get quoted; nil → "null";
// nested structures collapse to JSON-like form. Optimised for
// readability inside a one-line Detail rather than full reproducibility
// — consumers wanting exact values can re-load the plan JSON.
func formatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return fmt.Sprintf("%q", x)
	case float64:
		// JSON numbers come back as float64. Render integers without
		// the trailing zero.
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	case bool:
		return fmt.Sprintf("%t", x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// buildEntityIndex walks every module in the project and returns the
// set of (modulePath, entityID) keys present in the source-side
// analysis. Used to flag plan entries with no matching source-side
// entity (typically: stale plan, or resource referenced from a
// child module that wasn't loaded).
func buildEntityIndex(p *loader.Project) map[string]bool {
	out := map[string]bool{}
	if p == nil {
		return out
	}
	p.Walk(func(node *loader.ModuleNode) bool {
		mod := node.Module
		if mod == nil {
			return true
		}
		modulePath := modulePathFromNode(p, node)
		for _, e := range mod.Entities() {
			out[matchKey(modulePath, e.ID())] = true
		}
		return true
	})
	return out
}

// matchKey is the joined "<modulePath>|<entityID>" string used for
// entity lookups. The vertical bar can't appear in either component
// so the encoding is unambiguous without per-character escaping.
func matchKey(modulePath, entityID string) string {
	return modulePath + "|" + entityID
}

// modulePathFromNode returns the dotted path of a module node from
// the project root. Empty for the root itself; "module.X" for a
// direct child; "module.X.module.Y" for nested. Plan addresses use
// the same format (with their own `module.` prefixes), so the two
// paths can be compared directly.
//
// Note: this is a minimal implementation — it walks from the root
// to find the node by pointer identity. Sufficient for the small
// project trees this enrichment runs against; would need to be
// indexed if performance ever became an issue.
func modulePathFromNode(p *loader.Project, target *loader.ModuleNode) string {
	if p == nil || p.Root == nil || target == nil {
		return ""
	}
	if p.Root == target {
		return ""
	}
	var found string
	var walk func(n *loader.ModuleNode, prefix string)
	walk = func(n *loader.ModuleNode, prefix string) {
		for name, child := range n.Children {
			childPath := "module." + name
			if prefix != "" {
				childPath = prefix + "." + childPath
			}
			if child == target {
				found = childPath
				return
			}
			walk(child, childPath)
			if found != "" {
				return
			}
		}
	}
	walk(p.Root, "")
	return found
}
