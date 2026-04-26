package diff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/plan"
	"github.com/dgr237/tflens/pkg/token"
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
// Each plan ResourceChange (count/for_each instance) gets its own
// Change row with the full plan address — including the index —
// preserved in the Subject. The source-side lookup uses the
// index-stripped path so multiple instances all resolve to the same
// source-side ModuleNode + Entity, which is the correct shape for
// flagging a stale plan ("no matching source-side entity") without
// false positives on indexed instances.
//
// Source positions: when the plan ResourceChange matches a source-side
// entity, the entity's Pos is propagated onto the emitted Change's
// NewPos. The markdown renderer uses this to render `file:line` next
// to plan-derived rows so reviewers can navigate from the diff back
// to the resource declaration. Plan rows with no source-side match
// get a zero Position (and the renderer omits the file:line line).
//
// Moved-block awareness: when the source declares a `moved { from = X
// to = Y }` block AND the plan still shows X as a delete plus Y as a
// create, the pair is collapsed into a single Informational entry
// hinting that the plan is stale and should be regenerated. (When the
// plan correctly honours the moved block, terraform emits a no-op /
// update at the new address rather than delete+create — those pass
// through unchanged.) Both resource/data renames AND module-call
// renames are handled — module renames collapse the whole cluster
// of nested resources into one entry with a count; partial matches
// flow through normally.
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
	walkPlanChanges(p, newProj, func(_ string, c Change) {
		out = append(out, c)
	})
	sortChanges(out)
	return out
}

// walkPlanChanges runs the per-rc enrichment loop and dispatches each
// resulting Change to the caller-supplied route function with the
// originating plan module_address. Centralises the shared logic of
// EnrichFromPlan / EnrichResultsFromPlan / EnrichWhatifsFromPlan —
// each caller routes the same emitted Changes into different
// destinations (flat list, per-pair Changes, per-call APIChanges).
//
// Skips no-ops and the individual halves of detected stale-move
// pairs; the collapsed stale-move Informational entries are emitted
// after the main loop, routed by the To-side module path.
//
// Nil plan is a no-op so callers don't need to branch on the flag.
func walkPlanChanges(p *plan.Plan, newProj *loader.Project, route func(moduleAddress string, c Change)) {
	if p == nil {
		return
	}
	projectEntities := buildEntityIndex(newProj)
	movedIdx := buildMovedIndex(newProj)
	stale := detectStalePlanMoves(p.ResourceChanges, movedIdx)
	deleteSkip, createSkip := stalePlanMoveSkipSets(stale)

	for _, rc := range p.ResourceChanges {
		if rc.Change.IsNoOp() {
			continue
		}
		if shouldSkipForStaleMove(rc, deleteSkip, createSkip) {
			continue
		}
		exists, pos := lookupEntity(projectEntities, rc)
		for _, c := range changesForResourceChange(rc, exists, pos) {
			route(rc.ModuleAddress, c)
		}
	}
	for _, m := range stale {
		c := stalePlanMoveChange(m, projectEntities)
		route(stalePlanMoveRouteAddress(m.Pair), c)
	}
}

// stalePlanMoveRouteAddress returns the plan-style module address
// that the stale-move entry should be routed under. For a resource/
// data move that's the leading `module.X` portion of the To address;
// for a module-call move (`moved { from = module.old; to = module.new }`)
// the entry routes under the parent of `module.new` — the module
// that DECLARES the moved block, not the renamed module itself —
// because the moved {} declaration lives in the parent and the
// reviewer expects to see it next to the parent's other findings.
func stalePlanMoveRouteAddress(p movedPair) string {
	if p.Kind == "module" {
		return parentModuleAddress(p.To)
	}
	return moduleAddressOf(p.To)
}

// parentModuleAddress strips the trailing `module.<name>` segment
// from a plan-style module address. Returns "" for a top-level
// module (`module.X` → "" because the moved {} sits at the root).
func parentModuleAddress(planAddress string) string {
	stripped := stripIndices(planAddress)
	parts := strings.Split(stripped, ".")
	if len(parts) < 2 {
		return ""
	}
	parts = parts[:len(parts)-2] // drop the trailing `module.<name>` pair
	return strings.Join(parts, ".")
}

// EnrichResultsFromPlan is the per-module-aware variant of
// EnrichFromPlan. Findings whose module_address matches a paired
// module call land in that PairResult.Changes; findings for the
// root module (or for a module that doesn't have a paired call —
// typically a module added or removed entirely between sides) fall
// back to rootChanges.
//
// The motivation: with the flat EnrichFromPlan, a plan describing
// a `cidr_block` change inside `module.network` shows up under the
// project root in the rendered output, even though the source-side
// findings for `module.network` already have their own per-pair
// section. Reviewers had to mentally join the two. This routing
// puts plan-derived rows next to the matching static-side rows.
//
// Each result's Changes gets re-sorted after enrichment so the
// merged (static, plan) rows interleave by (Kind, Subject) — the
// same ordering AnalyzeProjects produces for the original list.
//
// Returns the (possibly modified) results slice + the merged
// rootChanges. The results slice is mutated in place; callers that
// need the originals untouched should clone first.
func EnrichResultsFromPlan(results []PairResult, rootChanges []Change,
	p *plan.Plan, newProj *loader.Project) ([]PairResult, []Change) {
	if p == nil {
		return results, rootChanges
	}
	// Pair key → results index. Empty-key entries (root pairs, if any
	// ever existed) skip routing — root-module findings always go
	// through rootChanges.
	pairIdx := map[string]int{}
	for i, r := range results {
		if r.Pair.Key != "" {
			pairIdx[r.Pair.Key] = i
		}
	}
	mergedRoot := append([]Change(nil), rootChanges...)
	walkPlanChanges(p, newProj, func(moduleAddress string, c Change) {
		key := planModuleKey(moduleAddress)
		if i, ok := pairIdx[key]; ok {
			results[i].Changes = append(results[i].Changes, c)
			return
		}
		mergedRoot = append(mergedRoot, c)
	})
	for i := range results {
		sortChanges(results[i].Changes)
	}
	sortChanges(mergedRoot)
	return results, mergedRoot
}

// EnrichWhatifsFromPlan augments each WhatifResult's APIChanges with
// plan-derived findings whose module address matches the call's
// pair key. Surfaces force-new attribute changes and other plan-only
// signals (replaces, deletes) alongside the static-side API diff for
// the same call so reviewers see one merged view per call.
//
// Plan rows whose module address has no matching call are dropped
// silently — whatif is per-call only with no rootChanges concept.
// Use `tflens diff --enrich-with-plan` when root-level coverage
// matters.
//
// Whatif's DirectImpact list is NOT touched. DirectImpact is
// strictly the cross-validation result (would the parent's USE break
// under the new child's API?); plan deltas are attribute-level
// signals on the resource side, not cross-validation failures. The
// caller can still gate the run on plan-derived Breaking findings —
// see cmd/whatif.go's countWhatifBreaking — but the separation
// keeps the two signal sources legible.
//
// Calls slice is mutated in place; nil plan is a no-op.
func EnrichWhatifsFromPlan(calls []WhatifResult, p *plan.Plan, newProj *loader.Project) []WhatifResult {
	if p == nil {
		return calls
	}
	pairIdx := map[string]int{}
	for i, r := range calls {
		if r.Pair.Key != "" {
			pairIdx[r.Pair.Key] = i
		}
	}
	walkPlanChanges(p, newProj, func(moduleAddress string, c Change) {
		key := planModuleKey(moduleAddress)
		if i, ok := pairIdx[key]; ok {
			calls[i].APIChanges = append(calls[i].APIChanges, c)
		}
	})
	for i := range calls {
		if len(calls[i].APIChanges) > 0 {
			sortChanges(calls[i].APIChanges)
		}
	}
	return calls
}

// changesForResourceChange returns the plan-derived Change entries for
// one ResourceChange. Centralised so EnrichFromPlan and the per-
// module routing variant produce identical Detail / Kind / Subject
// shapes — the only difference between the two callers is which
// bucket the result lands in.
//
// The (exists, pos) pair is the precomputed entity-index lookup
// result: `exists` controls the "no matching source-side entity" hint
// on create entries; `pos` populates the Change's NewPos so renderers
// can show file:line next to the row. A zero Position (no source-side
// match) leaves NewPos zero and the renderer omits the location.
func changesForResourceChange(rc plan.ResourceChange, exists bool, pos token.Position) []Change {
	var out []Change
	switch {
	case rc.Change.IsCreate():
		out = append(out, Change{
			Kind:    Informational,
			Subject: planSubject(rc),
			Detail:  fmt.Sprintf("plan creates %s%s", planAddressDescriptor(rc), entityHint(exists)),
			NewPos:  pos,
			Source:  SourcePlan,
		})
	case rc.Change.IsDelete():
		out = append(out, Change{
			Kind:    Breaking,
			Subject: planSubject(rc),
			Detail:  fmt.Sprintf("plan destroys %s — uncommitted state will be lost", planAddressDescriptor(rc)),
			NewPos:  pos,
			Source:  SourcePlan,
		})
	case rc.Change.IsReplace():
		// Replace = destroy + recreate. Already Breaking by
		// definition. Surface the per-attribute deltas underneath so
		// reviewers see WHICH change forced it.
		out = append(out, Change{
			Kind:    Breaking,
			Subject: planSubject(rc),
			Detail:  fmt.Sprintf("plan replaces %s (destroy + recreate)", planAddressDescriptor(rc)),
			NewPos:  pos,
			Source:  SourcePlan,
		})
		out = append(out, attrDeltaChangesWithPos(rc, pos)...)
	case rc.Change.IsUpdate():
		out = append(out, attrDeltaChangesWithPos(rc, pos)...)
	}
	return out
}

// lookupEntity hides the index-stripping detail so both enrichment
// entry points share a single way to ask "does this plan's resource
// match a known source-side entity?". Returns (exists, position) so
// callers can both gate the entity-existence hint and propagate the
// source position onto the resulting Change.
func lookupEntity(index map[string]entityRef, rc plan.ResourceChange) (bool, token.Position) {
	// Strip count/for_each indices from each module segment when
	// looking up the source-side entity — the source-side module tree
	// has one node per module CALL, not per instance, so
	// `module.foo[0].aws_vpc.main` and `module.foo[1].aws_vpc.main`
	// both need to find the same `module.foo`. Resource indices on
	// the trailing `[idx]` are already stripped by EntityID(), which
	// goes through the entity's canonical ID without index decoration.
	ref, ok := index[matchKey(stripIndices(rc.ModuleAddress), rc.EntityID())]
	return ok, ref.Pos
}

// sortChanges orders a slice by (Kind, Subject) so Breaking findings
// come first and ties break alphabetically. SliceStable preserves
// insertion order within a (Kind, Subject) tie — useful when a single
// resource emits a summary row + per-attribute rows that share the
// same Subject prefix.
func sortChanges(s []Change) {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].Kind != s[j].Kind {
			return s[i].Kind < s[j].Kind
		}
		return s[i].Subject < s[j].Subject
	})
}

// planModuleKey converts a plan's module_address (e.g.
// `module.regions["us-east-1"].module.subnets`) into the dotted-key
// form loader.ModuleCallPair uses (`regions.subnets`). Returns "" for
// an empty input (root module).
//
// count/for_each indices are stripped first so multiple instances of
// the same module call route to the same pair — there's only ever one
// PairResult per call regardless of how many instances it expands to.
//
// If the input doesn't fit the `module.X.module.Y...` shape (e.g. a
// malformed address), returns "" so the caller falls back to root
// rather than risk routing to a wrong pair.
func planModuleKey(moduleAddress string) string {
	stripped := stripIndices(moduleAddress)
	if stripped == "" {
		return ""
	}
	parts := strings.Split(stripped, ".")
	if len(parts)%2 != 0 {
		return ""
	}
	keyParts := make([]string, 0, len(parts)/2)
	for i := 0; i < len(parts); i += 2 {
		if parts[i] != "module" {
			return ""
		}
		keyParts = append(keyParts, parts[i+1])
	}
	return strings.Join(keyParts, ".")
}

// attrDeltaChanges turns each AttrDelta on a ResourceChange into a
// diff.Change. ForceNew deltas → Breaking; everything else →
// Informational. Each Change carries Subject = "<plan-address>:<attr-path>"
// so the output is unique even when multiple attributes change on
// the same resource.
//
// Sensitive markers redact the rendered value before it lands in
// Detail — without this, a `tflens diff --enrich-with-plan` against
// a plan touching e.g. an RDS password would write the password into
// CI logs. AfterUnknown surfaces as "(known after apply)" so the
// reader can tell a placeholder apart from an unset attribute.
func attrDeltaChangesWithPos(rc plan.ResourceChange, pos token.Position) []Change {
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
			Detail:  fmt.Sprintf("plan attribute change: %s → %s", renderBefore(d), renderAfter(d)),
			Hint:    hint,
			NewPos:  pos,
			Source:  SourcePlan,
		})
	}
	return out
}

// renderBefore formats the AttrDelta's Before value for inclusion in
// Detail. Sensitive markers take precedence — we never want the raw
// value in the output even if a renderer is being permissive.
func renderBefore(d plan.AttrDelta) string {
	if d.BeforeSensitive {
		return "(sensitive)"
	}
	return formatValue(d.Before)
}

// renderAfter formats the AttrDelta's After value for inclusion in
// Detail. Unknown beats sensitive (the value isn't computed yet, so
// "sensitive" would be misleading); both beat the raw value.
func renderAfter(d plan.AttrDelta) string {
	switch {
	case d.AfterUnknown:
		return "(known after apply)"
	case d.AfterSensitive:
		return "(sensitive)"
	}
	return formatValue(d.After)
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

// entityRef is the value side of the project-entity index — a flag
// that the entity exists plus its source position so plan-derived
// Changes can carry NewPos for renderers that show file:line.
type entityRef struct {
	Pos token.Position
}

// buildEntityIndex walks every module in the project and returns a
// map from (modulePath, entityID) to the entity's reference (currently
// just the source position). Used to flag plan entries with no
// matching source-side entity (typically: stale plan, or resource
// referenced from a child module that wasn't loaded) AND to propagate
// the entity's source position onto the plan-derived Change so the
// markdown renderer can show file:line next to the row.
func buildEntityIndex(p *loader.Project) map[string]entityRef {
	out := map[string]entityRef{}
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
			out[matchKey(modulePath, e.ID())] = entityRef{Pos: e.Pos}
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

// stripIndices removes count/for_each `[idx]` suffixes from each
// segment of a Terraform module path. Plan addresses include indices
// when a module call uses `count` or `for_each` (e.g.
// `module.regions["us-east-1"].aws_vpc.main`); the source-side module
// tree has a single ModuleNode per module CALL regardless of how many
// instances it expands to, so the lookup needs to drop the indices to
// find the match.
//
// Implementation: walk the string once, copying everything except
// content between `[` and `]`. Stays single-pass and avoids regexp
// for a hot-path helper.
func stripIndices(modulePath string) string {
	if modulePath == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(modulePath))
	depth := 0
	for i := 0; i < len(modulePath); i++ {
		switch modulePath[i] {
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteByte(modulePath[i])
			}
		}
	}
	return b.String()
}

// movedPair captures one source-side `moved { from = X; to = Y }`
// declaration with both addresses already prefixed by the containing
// module's path so they can be compared directly against plan
// ResourceChange addresses.
//
// Kind distinguishes:
//
//   - "resource" — `moved { from = aws_vpc.old; to = aws_vpc.new }`. The
//     plan-side stale signature is one delete at From + one create at To.
//   - "module" — `moved { from = module.old; to = module.new }`. The
//     plan-side stale signature is N deletes whose addresses start with
//     From + N creates whose addresses start with To, with matching
//     "inner" suffixes (`module.old.aws_vpc.main` ↔
//     `module.new.aws_vpc.main`, etc.).
//
// Indexed module moves (`moved { from = module.X[0]; to = module.X[1] }`)
// aren't recognised by the source-side parser (only bare references
// in from/to are accepted) so they don't appear here either.
type movedPair struct {
	From string
	To   string
	Kind string // "resource" or "module"
}

// stalePlanMove records one detected stale-move event together with
// the concrete plan addresses it covers. For a resource/data move
// this is a single (delete, create) pair; for a module-call rename
// it can cover N nested-resource pairs from the same parent
// declaration.
type stalePlanMove struct {
	Pair    movedPair
	Matches []stalePlanMatch
}

// stalePlanMatch is one paired (delete, create) plan ResourceChange
// that belongs to a stale-move event. Addresses keep their original
// indexes so renderers can show the precise instances the move
// affects.
type stalePlanMatch struct {
	DeleteAddress string
	CreateAddress string
}

// buildMovedIndex collects every `moved {}` block in the project
// (resource/data and module-call kinds). Empty when newProj is nil.
func buildMovedIndex(p *loader.Project) []movedPair {
	if p == nil {
		return nil
	}
	var out []movedPair
	p.Walk(func(node *loader.ModuleNode) bool {
		mod := node.Module
		if mod == nil {
			return true
		}
		prefix := modulePathFromNode(p, node)
		for from, to := range mod.Moved() {
			fromAddr := entityIDToPlanAddress(from)
			toAddr := entityIDToPlanAddress(to)
			if fromAddr == "" || toAddr == "" {
				continue // unhandled kind (variable / output / local)
			}
			kind := movedPairKind(from)
			if kind != movedPairKind(to) {
				// Mismatched kinds (resource ↔ module) — invalid in
				// real Terraform but be defensive.
				continue
			}
			if prefix != "" {
				fromAddr = prefix + "." + fromAddr
				toAddr = prefix + "." + toAddr
			}
			out = append(out, movedPair{From: fromAddr, To: toAddr, Kind: kind})
		}
		return true
	})
	return out
}

// entityIDToPlanAddress converts a canonical entity ID
// (`resource.aws_vpc.main`, `data.aws_ami.latest`, `module.X`) to the
// Terraform plan address form (`aws_vpc.main`, `data.aws_ami.latest`,
// `module.X`). Returns "" for kinds that don't appear in plan
// resource_changes addresses (variables, outputs, locals).
func entityIDToPlanAddress(entityID string) string {
	switch {
	case strings.HasPrefix(entityID, "resource."):
		return entityID[len("resource."):]
	case strings.HasPrefix(entityID, "data."):
		return entityID
	case strings.HasPrefix(entityID, "module."):
		return entityID
	}
	return ""
}

// movedPairKind returns "resource" or "module" based on the entity
// ID's prefix. data sources are grouped with resources because the
// plan-side stale signature is identical: one delete + one create at
// the literal addresses.
func movedPairKind(entityID string) string {
	if strings.HasPrefix(entityID, "module.") {
		return "module"
	}
	return "resource"
}

// detectStalePlanMoves finds plan delete + create combinations whose
// addresses match a source-side moved block. Their existence means
// the plan was generated BEFORE the moved block was added (or the
// block didn't take effect for some reason) — in either case the
// recommended action is "regenerate the plan."
//
// Resource/data moves match a single (delete, create) pair at the
// literal From/To addresses. Module-call moves match a CLUSTER:
// every plan delete whose address starts with `From.` paired with a
// plan create at the matching `To.<inner>` address. Partial matches
// are tracked — only the matched halves are skipped from the normal
// output, so unmatched deletes/creates inside a module-rename
// cluster (e.g. a resource that was added during the same PR as the
// rename) flow through the normal path as their own findings.
//
// When the moved block IS being honoured by the plan, terraform
// emits a no-op or update at the new address rather than
// delete+create — nothing matches and nothing collapses.
func detectStalePlanMoves(rcs []plan.ResourceChange, moved []movedPair) []stalePlanMove {
	if len(moved) == 0 {
		return nil
	}
	// Index plan changes by stripped address so module-rename matching
	// works against indexed instances too. Keep the original (possibly
	// indexed) address as the value — that's what renderers display.
	deletes := map[string]string{} // stripped address → original address
	creates := map[string]string{}
	for _, rc := range rcs {
		stripped := stripIndices(rc.Address)
		switch {
		case rc.Change.IsDelete():
			deletes[stripped] = rc.Address
		case rc.Change.IsCreate():
			creates[stripped] = rc.Address
		}
	}
	var out []stalePlanMove
	for _, m := range moved {
		switch m.Kind {
		case "module":
			matches := matchModuleMove(m, deletes, creates)
			if len(matches) > 0 {
				out = append(out, stalePlanMove{Pair: m, Matches: matches})
			}
		default: // "resource"
			delAddr, hasDel := deletes[m.From]
			creAddr, hasCre := creates[m.To]
			if hasDel && hasCre {
				out = append(out, stalePlanMove{
					Pair: m,
					Matches: []stalePlanMatch{{
						DeleteAddress: delAddr,
						CreateAddress: creAddr,
					}},
				})
			}
		}
	}
	return out
}

// matchModuleMove pairs plan deletes under `From.<inner>` with plan
// creates under `To.<inner>` — the signature of a stale plan that
// pre-dates a `moved { from = module.X; to = module.Y }` declaration.
// Only inner addresses that exist on BOTH sides are returned;
// unmatched halves (a delete with no create twin or vice versa) are
// left out so they fall through to the normal output as their own
// findings.
func matchModuleMove(m movedPair, deletes, creates map[string]string) []stalePlanMatch {
	fromPrefix := m.From + "."
	toPrefix := m.To + "."
	var out []stalePlanMatch
	for delStripped, delAddr := range deletes {
		if !strings.HasPrefix(delStripped, fromPrefix) {
			continue
		}
		inner := delStripped[len(fromPrefix):]
		creStripped := toPrefix + inner
		creAddr, ok := creates[creStripped]
		if !ok {
			continue
		}
		out = append(out, stalePlanMatch{
			DeleteAddress: delAddr,
			CreateAddress: creAddr,
		})
	}
	// Sort for deterministic output — map iteration order is random.
	sort.Slice(out, func(i, j int) bool {
		return out[i].DeleteAddress < out[j].DeleteAddress
	})
	return out
}

// stalePlanMoveSkipSets returns the address sets used to suppress
// the individual delete + create entries that the stale-move
// detector will replace with collapsed Informational entries.
// Walks every Match across every detected move so module-rename
// clusters skip all their N halves, not just one.
func stalePlanMoveSkipSets(matches []stalePlanMove) (deleteSkip, createSkip map[string]bool) {
	deleteSkip = map[string]bool{}
	createSkip = map[string]bool{}
	for _, m := range matches {
		for _, mm := range m.Matches {
			deleteSkip[mm.DeleteAddress] = true
			createSkip[mm.CreateAddress] = true
		}
	}
	return deleteSkip, createSkip
}

// shouldSkipForStaleMove reports whether the given ResourceChange is
// the delete or create half of a detected stale-move pair, in which
// case the main loop omits it (it'll be replaced with a single
// Informational entry by stalePlanMoveChange).
func shouldSkipForStaleMove(rc plan.ResourceChange, deleteSkip, createSkip map[string]bool) bool {
	switch {
	case rc.Change.IsDelete():
		return deleteSkip[rc.Address]
	case rc.Change.IsCreate():
		return createSkip[rc.Address]
	}
	return false
}

// stalePlanMoveChange produces a single Informational Change for a
// detected stale-move event. Resource/data moves get a one-line
// "regenerate the plan" hint with the from/to identifiers; module
// moves get the same hint plus a count of how many nested resources
// the rename would have covered (so the reviewer can sanity-check
// the scope before regenerating).
//
// NewPos points at the destination entity when found in the source-
// side index, so the markdown renderer can link to the new module
// call (resource/data) or the new module call's declaration (module).
func stalePlanMoveChange(m stalePlanMove, projectEntities map[string]entityRef) Change {
	pair := m.Pair
	modulePath, entityID := splitPlanAddress(pair.To)
	pos := projectEntities[matchKey(modulePath, entityID)].Pos

	if pair.Kind == "module" {
		return Change{
			Kind:    Informational,
			Subject: fmt.Sprintf("%s → %s", pair.From, pair.To),
			Detail: fmt.Sprintf(
				"source declares `moved { from = %s; to = %s }` but plan still shows %d nested %s being recreated under the new path — regenerate the plan to honour the moved block",
				planAddressIdentifier(pair.From), planAddressIdentifier(pair.To),
				len(m.Matches), pluralResources(len(m.Matches)),
			),
			NewPos: pos,
			Source: SourcePlan,
		}
	}
	return Change{
		Kind:    Informational,
		Subject: fmt.Sprintf("%s → %s", pair.From, pair.To),
		Detail: fmt.Sprintf(
			"source declares `moved { from = %s; to = %s }` but plan still shows destroy + recreate — regenerate the plan to honour the moved block",
			planAddressIdentifier(pair.From), planAddressIdentifier(pair.To),
		),
		NewPos: pos,
		Source: SourcePlan,
	}
}

// pluralResources picks "resource" or "resources" for the count.
// Tiny helper kept local so the message construction stays readable.
func pluralResources(n int) string {
	if n == 1 {
		return "resource"
	}
	return "resources"
}

// planAddressIdentifier returns the local resource address (without
// module prefix) for inclusion in a Detail string. Mirrors the form
// authors actually wrote in the moved block — `aws_vpc.new` rather
// than `module.network.aws_vpc.new`.
func planAddressIdentifier(planAddress string) string {
	_, local := splitPlanAddress(planAddress)
	// Convert canonical entity ID back to plan-address form for
	// display so the user sees what they wrote in the moved block.
	if a := entityIDToPlanAddress(local); a != "" {
		return a
	}
	return local
}

// splitPlanAddress splits a full plan ResourceChange address into
// (modulePath, entityID). modulePath is the leading `module.X`-only
// segments joined verbatim ("module.network.module.subnets" — index
// segments stripped); entityID is the canonical form expected by the
// entity index (`resource.<type>.<name>` or `data.<type>.<name>`).
//
// Examples:
//
//	`aws_vpc.main`                                → ("", "resource.aws_vpc.main")
//	`module.network.aws_vpc.main`                 → ("module.network", "resource.aws_vpc.main")
//	`module.regions["us-east-1"].aws_vpc.main`    → ("module.regions", "resource.aws_vpc.main")
//	`data.aws_ami.latest`                         → ("", "data.aws_ami.latest")
func splitPlanAddress(addr string) (modulePath, entityID string) {
	stripped := stripIndices(addr)
	parts := strings.Split(stripped, ".")
	// Walk forward through `module.X` pairs.
	i := 0
	for i+1 < len(parts) && parts[i] == "module" {
		i += 2
	}
	modulePath = strings.Join(parts[:i], ".")
	rest := parts[i:]
	switch {
	case len(rest) >= 3 && rest[0] == "data":
		entityID = "data." + rest[1] + "." + rest[2]
	case len(rest) >= 2:
		entityID = "resource." + rest[0] + "." + rest[1]
	}
	return modulePath, entityID
}

// moduleAddressOf returns the leading `module.X[.module.Y...]` portion
// of a plan address. Used by the routing variant to send a stale-move
// entry to the same pair the moved-to resource will live in.
func moduleAddressOf(planAddress string) string {
	stripped := stripIndices(planAddress)
	parts := strings.Split(stripped, ".")
	i := 0
	for i+1 < len(parts) && parts[i] == "module" {
		i += 2
	}
	return strings.Join(parts[:i], ".")
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
