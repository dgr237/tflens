// Package diff compares two module analyses and reports API-level changes.
// The diff is intended to answer: "if I upgrade from the old module to the
// new one, what breaks?"
package diff

import (
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/token"
)

// ChangeKind classifies the impact of a detected change.
type ChangeKind int

const (
	// Breaking will require the caller (or the state) to be updated.
	Breaking ChangeKind = iota
	// NonBreaking is safe to roll out without changes.
	NonBreaking
	// Informational is worth mentioning but does not affect behaviour.
	Informational
)

func (k ChangeKind) String() string {
	switch k {
	case Breaking:
		return "breaking"
	case NonBreaking:
		return "non-breaking"
	case Informational:
		return "info"
	}
	return "unknown"
}

// Change describes a single difference between two module versions.
type Change struct {
	Kind    ChangeKind
	Subject string         // entity id (or "old-id → new-id" for renames)
	Detail  string         // human-readable description
	OldPos  token.Position // zero value for pure additions
	NewPos  token.Position // zero value for pure removals
}

func (c Change) String() string {
	return fmt.Sprintf("[%s] %s: %s", c.Kind, c.Subject, c.Detail)
}

// Diff compares two module analyses and returns all detected changes, sorted
// by kind (Breaking first) and then by subject for deterministic output.
func Diff(oldMod, newMod *analysis.Module) []Change {
	var changes []Change
	diffVariables(oldMod, newMod, &changes)
	diffOutputs(oldMod, newMod, &changes)
	diffStatefulEntities(oldMod, newMod, &changes)
	diffTerraformBlock(oldMod, newMod, &changes)

	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Kind != changes[j].Kind {
			return changes[i].Kind < changes[j].Kind
		}
		return changes[i].Subject < changes[j].Subject
	})
	return changes
}

// ---- variables ----

func diffVariables(oldMod, newMod *analysis.Module, changes *[]Change) {
	oldVars := byName(oldMod, analysis.KindVariable)
	newVars := byName(newMod, analysis.KindVariable)

	for name, oe := range oldVars {
		ne, present := newVars[name]
		if !present {
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: oe.ID(),
				Detail:  "variable removed",
				OldPos:  oe.Pos,
			})
			continue
		}
		if oe.HasDefault && !ne.HasDefault {
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: oe.ID(),
				Detail:  "default removed (variable became required)",
				OldPos:  oe.Pos,
				NewPos:  ne.Pos,
			})
		} else if !oe.HasDefault && ne.HasDefault {
			*changes = append(*changes, Change{
				Kind:    NonBreaking,
				Subject: oe.ID(),
				Detail:  "default added (variable became optional)",
				OldPos:  oe.Pos,
				NewPos:  ne.Pos,
			})
		}
		// Nullable transitions
		if !oe.NonNullable && ne.NonNullable {
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: oe.ID(),
				Detail:  "`nullable = false` added (null inputs now rejected)",
				OldPos:  oe.Pos,
				NewPos:  ne.Pos,
			})
		} else if oe.NonNullable && !ne.NonNullable {
			*changes = append(*changes, Change{
				Kind:    NonBreaking,
				Subject: oe.ID(),
				Detail:  "`nullable = false` removed (null inputs now accepted)",
				OldPos:  oe.Pos,
				NewPos:  ne.Pos,
			})
		}
		// Sensitive transitions on variables
		if !oe.Sensitive && ne.Sensitive {
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: oe.ID(),
				Detail:  "`sensitive = true` added (downstream non-sensitive uses will break)",
				OldPos:  oe.Pos,
				NewPos:  ne.Pos,
			})
		} else if oe.Sensitive && !ne.Sensitive {
			*changes = append(*changes, Change{
				Kind:    Informational,
				Subject: oe.ID(),
				Detail:  "sensitive flag dropped (value will no longer be masked)",
				OldPos:  oe.Pos,
				NewPos:  ne.Pos,
			})
		}
		// Validation blocks added
		if ne.Validations > oe.Validations {
			added := ne.Validations - oe.Validations
			*changes = append(*changes, Change{
				Kind:    Informational,
				Subject: oe.ID(),
				Detail: fmt.Sprintf("%d new validation block(s) — may reject previously-valid inputs",
					added),
				OldPos: oe.Pos,
				NewPos: ne.Pos,
			})
		}
		// Precondition / postcondition counts (variables)
		if ne.Preconditions > oe.Preconditions {
			*changes = append(*changes, Change{
				Kind:    Informational,
				Subject: oe.ID(),
				Detail: fmt.Sprintf("%d new precondition(s) — may reject previously-valid inputs",
					ne.Preconditions-oe.Preconditions),
				OldPos: oe.Pos,
				NewPos: ne.Pos,
			})
		}
		if ne.Postconditions > oe.Postconditions {
			*changes = append(*changes, Change{
				Kind:    Informational,
				Subject: oe.ID(),
				Detail: fmt.Sprintf("%d new postcondition(s) — may reject previously-valid state",
					ne.Postconditions-oe.Postconditions),
				OldPos: oe.Pos,
				NewPos: ne.Pos,
			})
		}
		if !typeEqual(oe.DeclaredType, ne.DeclaredType) {
			// When both types are objects, emit per-field changes instead
			// of a blanket "type changed".
			if isObjectType(oe.DeclaredType) && isObjectType(ne.DeclaredType) {
				if diffObjectFields(oe.ID(), oe.DeclaredType, ne.DeclaredType, oe.Pos, ne.Pos, changes) > 0 {
					continue
				}
			}
			kind := Breaking
			detail := fmt.Sprintf("type changed: %s → %s",
				typeStr(oe.DeclaredType), typeStr(ne.DeclaredType))
			if isAnyType(ne.DeclaredType) && !isAnyType(oe.DeclaredType) {
				kind = NonBreaking
				detail += " (widened to any)"
			}
			*changes = append(*changes, Change{
				Kind:    kind,
				Subject: oe.ID(),
				Detail:  detail,
				OldPos:  oe.Pos,
				NewPos:  ne.Pos,
			})
		}
	}

	for name, ne := range newVars {
		if _, present := oldVars[name]; present {
			continue
		}
		kind := NonBreaking
		detail := "variable added (optional)"
		if !ne.HasDefault {
			kind = Breaking
			detail = "required variable added (no default)"
		}
		*changes = append(*changes, Change{
			Kind:    kind,
			Subject: ne.ID(),
			Detail:  detail,
			NewPos:  ne.Pos,
		})
	}
}

// ---- outputs ----

func diffOutputs(oldMod, newMod *analysis.Module, changes *[]Change) {
	oldOuts := byName(oldMod, analysis.KindOutput)
	newOuts := byName(newMod, analysis.KindOutput)

	for name, oe := range oldOuts {
		ne, present := newOuts[name]
		if !present {
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: oe.ID(),
				Detail:  "output removed",
				OldPos:  oe.Pos,
			})
			continue
		}
		// Sensitive transitions on outputs
		if !oe.Sensitive && ne.Sensitive {
			*changes = append(*changes, Change{
				Kind:    Informational,
				Subject: oe.ID(),
				Detail:  "output became sensitive (callers logging the value will now see the mask)",
				OldPos:  oe.Pos,
				NewPos:  ne.Pos,
			})
		} else if oe.Sensitive && !ne.Sensitive {
			*changes = append(*changes, Change{
				Kind:    Informational,
				Subject: oe.ID(),
				Detail:  "output no longer sensitive (value will be visible in logs)",
				OldPos:  oe.Pos,
				NewPos:  ne.Pos,
			})
		}
		// Precondition / postcondition counts (outputs)
		if ne.Preconditions > oe.Preconditions {
			*changes = append(*changes, Change{
				Kind:    Informational,
				Subject: oe.ID(),
				Detail: fmt.Sprintf("%d new output precondition(s) — may reject previously-valid state",
					ne.Preconditions-oe.Preconditions),
				OldPos: oe.Pos,
				NewPos: ne.Pos,
			})
		}
		if ne.Postconditions > oe.Postconditions {
			*changes = append(*changes, Change{
				Kind:    Informational,
				Subject: oe.ID(),
				Detail: fmt.Sprintf("%d new output postcondition(s) — may reject previously-valid state",
					ne.Postconditions-oe.Postconditions),
				OldPos: oe.Pos,
				NewPos: ne.Pos,
			})
		}
		diffDependsOn(oe.ID(), oe, ne, changes)
		// Value-expression shape changes
		if oe.ValueExpr != nil && ne.ValueExpr != nil {
			oldText := oe.ValueExpr.Text()
			newText := ne.ValueExpr.Text()
			if oldText != newText {
				*changes = append(*changes, Change{
					Kind:    Informational,
					Subject: oe.ID(),
					Detail:  fmt.Sprintf("value expression changed: %s → %s", oldText, newText),
					OldPos:  oe.Pos,
					NewPos:  ne.Pos,
				})
			} else {
				// Expression text is identical, but a local it references may
				// have changed — surface that indirection.
				diffIndirectLocals(oe, ne, oldMod, newMod, changes)
			}
		}
	}
	for name, ne := range newOuts {
		if _, present := oldOuts[name]; !present {
			*changes = append(*changes, Change{
				Kind:    NonBreaking,
				Subject: ne.ID(),
				Detail:  "output added",
				NewPos:  ne.Pos,
			})
		}
	}
}

// ---- terraform block: required_version, required_providers ----

func diffTerraformBlock(oldMod, newMod *analysis.Module, changes *[]Change) {
	if ov, nv := oldMod.RequiredVersion(), newMod.RequiredVersion(); ov != nv {
		kind, detail := classifyVersionChange("required_version", ov, nv)
		*changes = append(*changes, Change{
			Kind:    kind,
			Subject: "terraform.required_version",
			Detail:  detail,
		})
	}

	oldProv := oldMod.RequiredProviders()
	newProv := newMod.RequiredProviders()

	for name, op := range oldProv {
		np, present := newProv[name]
		subject := "provider." + name
		if !present {
			*changes = append(*changes, Change{
				Kind:    NonBreaking,
				Subject: subject,
				Detail:  fmt.Sprintf("required provider %q removed (callers no longer need it)", name),
			})
			continue
		}
		if op.Source != np.Source {
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: subject,
				Detail: fmt.Sprintf("provider source changed: %q → %q (different provider entirely)",
					displayVersion(op.Source), displayVersion(np.Source)),
			})
		}
		if op.Version != np.Version {
			kind, detail := classifyVersionChange(
				fmt.Sprintf("provider %q version", name), op.Version, np.Version)
			*changes = append(*changes, Change{
				Kind:    kind,
				Subject: subject,
				Detail:  detail,
			})
		}
	}
	for name, np := range newProv {
		if _, present := oldProv[name]; present {
			continue
		}
		*changes = append(*changes, Change{
			Kind:    Breaking,
			Subject: "provider." + name,
			Detail: fmt.Sprintf("required provider %q added (source=%q, version=%q) — callers must configure it",
				name, displayVersion(np.Source), displayVersion(np.Version)),
		})
	}
}

// ---- resources, data sources, module calls ----

// diffStatefulEntities diffs entities that have state-addressing implications:
// resources, data sources, and module calls. `moved` and `removed` blocks in
// the new module pre-resolve some of these and downgrade them to
// Informational.
func diffStatefulEntities(oldMod, newMod *analysis.Module, changes *[]Change) {
	oldMap := statefulMap(oldMod)
	newMap := statefulMap(newMod)
	moved := newMod.Moved() // from-ID → to-ID, declared in new

	// Meta-argument changes on entities present in both versions.
	for id, oe := range oldMap {
		ne, present := newMap[id]
		if !present {
			continue
		}
		if oe.HasCount != ne.HasCount || oe.HasForEach != ne.HasForEach {
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: id,
				Detail: fmt.Sprintf("instance addressing changed: %s → %s (use `moved` blocks to migrate state)",
					addressMode(oe), addressMode(ne)),
				OldPos: oe.Pos,
				NewPos: ne.Pos,
			})
		} else {
			// Mode unchanged — but the expression content might differ, which
			// changes which keys/indices exist at plan time.
			if oe.HasForEach && ne.HasForEach {
				oldText := oe.ForEachExpr.Text()
				newText := ne.ForEachExpr.Text()
				if oldText != newText {
					*changes = append(*changes, Change{
						Kind:    Informational,
						Subject: id,
						Detail: fmt.Sprintf("for_each expression changed: %s → %s (state keys may differ)",
							oldText, newText),
						OldPos: oe.Pos,
						NewPos: ne.Pos,
					})
				}
			}
			if oe.HasCount && ne.HasCount {
				oldText := oe.CountExpr.Text()
				newText := ne.CountExpr.Text()
				if oldText != newText {
					*changes = append(*changes, Change{
						Kind:    Informational,
						Subject: id,
						Detail: fmt.Sprintf("count expression changed: %s → %s (instance count may differ)",
							oldText, newText),
						OldPos: oe.Pos,
						NewPos: ne.Pos,
					})
				}
			}
		}
		// Provider alias changes (resource/data only — module uses `providers = {}`).
		if oe.Kind == analysis.KindResource || oe.Kind == analysis.KindData {
			oldProv := oe.ProviderExpr.Text()
			newProv := ne.ProviderExpr.Text()
			if oldProv != newProv {
				*changes = append(*changes, Change{
					Kind:    Breaking,
					Subject: id,
					Detail: fmt.Sprintf("provider changed: %s → %s (resource will be recreated under the new provider configuration)",
						displayProvider(oldProv), displayProvider(newProv)),
					OldPos: oe.Pos,
					NewPos: ne.Pos,
				})
			}
		}
		// Lifecycle transitions (resources only).
		if oe.Kind == analysis.KindResource {
			diffLifecycle(id, oe, ne, changes)
		}
		// depends_on (any stateful entity)
		diffDependsOn(id, oe, ne, changes)
		// Module source / version changes (module blocks only).
		if oe.Kind == analysis.KindModule {
			if os, ns := oldMod.ModuleSource(oe.Name), newMod.ModuleSource(ne.Name); os != ns {
				*changes = append(*changes, Change{
					Kind:    Informational,
					Subject: id,
					Detail:  fmt.Sprintf("module source changed: %q → %q", os, ns),
					OldPos:  oe.Pos,
					NewPos:  ne.Pos,
				})
			}
			if ov, nv := oldMod.ModuleVersion(oe.Name), newMod.ModuleVersion(ne.Name); ov != nv {
				kind, detail := classifyVersionChange("module version", ov, nv)
				*changes = append(*changes, Change{
					Kind:    kind,
					Subject: id,
					Detail:  detail,
					OldPos:  oe.Pos,
					NewPos:  ne.Pos,
				})
			}
			diffModuleArgs(id, oe, ne, changes)
		}
	}

	// Walk explicit `moved` blocks first. If a rename is declared, it's not a
	// breaking change — it's a handled migration.
	handledRem := map[string]bool{}
	handledAdd := map[string]bool{}
	for fromID, toID := range moved {
		oe, oldHas := oldMap[fromID]
		ne, newHas := newMap[toID]
		if !oldHas || !newHas {
			continue
		}
		*changes = append(*changes, Change{
			Kind:    Informational,
			Subject: fromID + " → " + toID,
			Detail:  "rename handled by `moved` block",
			OldPos:  oe.Pos,
			NewPos:  ne.Pos,
		})
		handledRem[fromID] = true
		handledAdd[toID] = true
	}

	// Gather unmatched removed / added for rename pairing.
	var removed, added []analysis.Entity
	for id, oe := range oldMap {
		if _, present := newMap[id]; present || handledRem[id] {
			continue
		}
		removed = append(removed, oe)
	}
	for id, ne := range newMap {
		if _, present := oldMap[id]; present || handledAdd[id] {
			continue
		}
		added = append(added, ne)
	}

	pairs, unpairedRem, unpairedAdd := pairRenames(removed, added)

	for _, p := range pairs {
		*changes = append(*changes, Change{
			Kind:    Breaking,
			Subject: p.old.ID() + " → " + p.new.ID(),
			Detail:  "possible rename (add `moved` block to preserve state; otherwise destroy + create)",
			OldPos:  p.old.Pos,
			NewPos:  p.new.Pos,
		})
	}
	for _, oe := range unpairedRem {
		// Intentional removals declared via `removed` blocks are
		// Informational rather than Breaking.
		if newMod.RemovedDeclared(oe.ID()) {
			*changes = append(*changes, Change{
				Kind:    Informational,
				Subject: oe.ID(),
				Detail:  "removal handled by `removed` block",
				OldPos:  oe.Pos,
			})
			continue
		}
		*changes = append(*changes, Change{
			Kind:    Breaking,
			Subject: oe.ID(),
			Detail:  "removed (state will be destroyed; use `removed` block to keep existing infrastructure)",
			OldPos:  oe.Pos,
		})
	}
	for _, ne := range unpairedAdd {
		*changes = append(*changes, Change{
			Kind:    Informational,
			Subject: ne.ID(),
			Detail:  "added",
			NewPos:  ne.Pos,
		})
	}
}

type renamePair struct {
	old analysis.Entity
	new analysis.Entity
}

// pairRenames pairs a removed entity with an added entity when both share the
// same kind and type and are the only ones in their (kind,type) group on their
// respective side. This heuristic catches simple name changes without false-
// matching larger structural edits.
func pairRenames(removed, added []analysis.Entity) ([]renamePair, []analysis.Entity, []analysis.Entity) {
	key := func(e analysis.Entity) string { return string(e.Kind) + "|" + e.Type }

	remGroups := map[string][]analysis.Entity{}
	for _, e := range removed {
		k := key(e)
		remGroups[k] = append(remGroups[k], e)
	}
	addGroups := map[string][]analysis.Entity{}
	for _, e := range added {
		k := key(e)
		addGroups[k] = append(addGroups[k], e)
	}

	var pairs []renamePair
	paired := map[string]bool{}
	for k, rems := range remGroups {
		adds, ok := addGroups[k]
		if !ok || len(rems) != 1 || len(adds) != 1 {
			continue
		}
		pairs = append(pairs, renamePair{old: rems[0], new: adds[0]})
		paired["rem:"+rems[0].ID()] = true
		paired["add:"+adds[0].ID()] = true
	}

	var unpairedRem, unpairedAdd []analysis.Entity
	for _, e := range removed {
		if !paired["rem:"+e.ID()] {
			unpairedRem = append(unpairedRem, e)
		}
	}
	for _, e := range added {
		if !paired["add:"+e.ID()] {
			unpairedAdd = append(unpairedAdd, e)
		}
	}
	return pairs, unpairedRem, unpairedAdd
}

// diffLifecycle emits Informational changes for lifecycle-block transitions.
// These are operational rather than API-breaking, but worth flagging.
func diffLifecycle(id string, oe, ne analysis.Entity, changes *[]Change) {
	if oe.PreventDestroy != ne.PreventDestroy {
		var detail string
		if ne.PreventDestroy {
			detail = "`prevent_destroy = true` added (resource can no longer be destroyed via plan/apply)"
		} else {
			detail = "`prevent_destroy` removed (resource can now be destroyed)"
		}
		*changes = append(*changes, Change{
			Kind:    Informational,
			Subject: id,
			Detail:  detail,
			OldPos:  oe.Pos,
			NewPos:  ne.Pos,
		})
	}
	if oe.CreateBeforeDestroy != ne.CreateBeforeDestroy {
		var detail string
		if ne.CreateBeforeDestroy {
			detail = "`create_before_destroy = true` added (new instance will be created before the old one is destroyed)"
		} else {
			detail = "`create_before_destroy` removed (destroy-then-create order)"
		}
		*changes = append(*changes, Change{
			Kind:    Informational,
			Subject: id,
			Detail:  detail,
			OldPos:  oe.Pos,
			NewPos:  ne.Pos,
		})
	}
	if s := oe.IgnoreChangesExpr.Text(); s != ne.IgnoreChangesExpr.Text() {
		*changes = append(*changes, Change{
			Kind:    Informational,
			Subject: id,
			Detail: fmt.Sprintf("`ignore_changes` changed: %s → %s (drift detection behaviour differs)",
				displayIgnoreList(s), displayIgnoreList(ne.IgnoreChangesExpr.Text())),
			OldPos: oe.Pos,
			NewPos: ne.Pos,
		})
	}
	if s := oe.ReplaceTriggeredByExpr.Text(); s != ne.ReplaceTriggeredByExpr.Text() {
		*changes = append(*changes, Change{
			Kind:    Informational,
			Subject: id,
			Detail: fmt.Sprintf("`replace_triggered_by` changed: %s → %s (replacement triggers differ)",
				displayIgnoreList(s), displayIgnoreList(ne.ReplaceTriggeredByExpr.Text())),
			OldPos: oe.Pos,
			NewPos: ne.Pos,
		})
	}
	// Lifecycle precondition / postcondition counts
	if ne.Preconditions > oe.Preconditions {
		*changes = append(*changes, Change{
			Kind:    Informational,
			Subject: id,
			Detail: fmt.Sprintf("%d new lifecycle precondition(s) — may reject previously-valid plans",
				ne.Preconditions-oe.Preconditions),
			OldPos: oe.Pos,
			NewPos: ne.Pos,
		})
	}
	if ne.Postconditions > oe.Postconditions {
		*changes = append(*changes, Change{
			Kind:    Informational,
			Subject: id,
			Detail: fmt.Sprintf("%d new lifecycle postcondition(s) — may reject previously-valid state",
				ne.Postconditions-oe.Postconditions),
			OldPos: oe.Pos,
			NewPos: ne.Pos,
		})
	}
}

// diffIndirectLocals detects when an output's value expression is textually
// unchanged but references a local whose own expression shifted — meaning the
// output's runtime value may have changed even though the expression did not.
func diffIndirectLocals(oe, ne analysis.Entity, oldMod, newMod *analysis.Module, changes *[]Change) {
	// Gather local names referenced by the output's value expression.
	referenced := map[string]bool{}
	if ne.ValueExpr != nil && ne.ValueExpr.E != nil {
		for _, trav := range ne.ValueExpr.E.Variables() {
			parts := traversalLocalName(trav)
			if parts != "" {
				referenced[parts] = true
			}
		}
	}
	// For each referenced local, compare its expression between versions.
	for name := range referenced {
		id := (analysis.Entity{Kind: analysis.KindLocal, Name: name}).ID()
		oldLocal, oldOK := findEntity(oldMod, id)
		newLocal, newOK := findEntity(newMod, id)
		if !oldOK || !newOK {
			continue // added or removed — dep-graph changes would show it
		}
		oldText := oldLocal.LocalExpr.Text()
		newText := newLocal.LocalExpr.Text()
		if oldText == newText {
			continue
		}
		*changes = append(*changes, Change{
			Kind:    Informational,
			Subject: oe.ID(),
			Detail: fmt.Sprintf("referenced %s expression changed: %s → %s (output expression unchanged, but value may differ)",
				id, oldText, newText),
			OldPos: oe.Pos,
			NewPos: ne.Pos,
		})
	}
}

// findEntity looks up an entity by ID via Entities() — avoids exposing byID.
func findEntity(m *analysis.Module, id string) (analysis.Entity, bool) {
	for _, e := range m.Entities() {
		if e.ID() == id {
			return e, true
		}
	}
	return analysis.Entity{}, false
}

// diffModuleArgs emits Informational changes when the arguments passed to a
// module block change (added, removed, or value shape shifted).
func diffModuleArgs(id string, oe, ne analysis.Entity, changes *[]Change) {
	oldArgs := oe.ModuleArgs
	newArgs := ne.ModuleArgs
	// Removed
	for name, oldExpr := range oldArgs {
		newExpr, present := newArgs[name]
		if !present {
			*changes = append(*changes, Change{
				Kind:    Informational,
				Subject: id,
				Detail:  fmt.Sprintf("module argument %q removed", name),
				OldPos:  oe.Pos,
				NewPos:  ne.Pos,
			})
			continue
		}
		// Value-shape change
		oldText := oldExpr.Text()
		newText := newExpr.Text()
		if oldText != newText {
			*changes = append(*changes, Change{
				Kind:    Informational,
				Subject: id,
				Detail: fmt.Sprintf("module argument %q value changed: %s → %s",
					name, oldText, newText),
				OldPos: oe.Pos,
				NewPos: ne.Pos,
			})
		}
	}
	// Added
	for name := range newArgs {
		if _, present := oldArgs[name]; !present {
			*changes = append(*changes, Change{
				Kind:    Informational,
				Subject: id,
				Detail:  fmt.Sprintf("module argument %q added", name),
				OldPos:  oe.Pos,
				NewPos:  ne.Pos,
			})
		}
	}
}

// diffDependsOn compares depends_on expressions and emits an Informational
// change when they differ. Used by both outputs and stateful entities.
func diffDependsOn(subject string, oe, ne analysis.Entity, changes *[]Change) {
	oldText := oe.DependsOnExpr.Text()
	newText := ne.DependsOnExpr.Text()
	if oldText == newText {
		return
	}
	*changes = append(*changes, Change{
		Kind:    Informational,
		Subject: subject,
		Detail: fmt.Sprintf("`depends_on` changed: %s → %s (evaluation order shifted; may surface timing issues)",
			displayIgnoreList(oldText), displayIgnoreList(newText)),
		OldPos: oe.Pos,
		NewPos: ne.Pos,
	})
}

func displayIgnoreList(s string) string {
	if s == "" {
		return "<none>"
	}
	return s
}

// traversalLocalName returns the local-name part of a `local.X[...]`
// traversal, or "" if the traversal does not begin with `local.<name>`.
func traversalLocalName(trav hcl.Traversal) string {
	if len(trav) < 2 {
		return ""
	}
	root, ok := trav[0].(hcl.TraverseRoot)
	if !ok || root.Name != "local" {
		return ""
	}
	attr, ok := trav[1].(hcl.TraverseAttr)
	if !ok {
		return ""
	}
	return attr.Name
}

// ---- helpers ----

// byName returns a map of Name → Entity for entities of the given kind.
// Used for variables, locals, outputs, and modules — kinds whose identity is
// a single name.
func byName(m *analysis.Module, kind analysis.EntityKind) map[string]analysis.Entity {
	out := map[string]analysis.Entity{}
	for _, e := range m.Filter(kind) {
		out[e.Name] = e
	}
	return out
}

// statefulMap returns a map of canonical ID → Entity for entities whose
// presence affects Terraform state: resources, data sources, and module calls.
func statefulMap(m *analysis.Module) map[string]analysis.Entity {
	out := map[string]analysis.Entity{}
	for _, e := range m.Entities() {
		switch e.Kind {
		case analysis.KindResource, analysis.KindData, analysis.KindModule:
			out[e.ID()] = e
		}
	}
	return out
}

func addressMode(e analysis.Entity) string {
	switch {
	case e.HasForEach:
		return "for_each"
	case e.HasCount:
		return "count"
	default:
		return "single instance"
	}
}

func isAnyType(t *analysis.TFType) bool {
	return t != nil && t.Kind == analysis.TypeAny
}

func isObjectType(t *analysis.TFType) bool {
	return t != nil && t.Kind == analysis.TypeObject
}

func displayVersion(v string) string {
	if v == "" {
		return "<unpinned>"
	}
	return v
}

func displayProvider(p string) string {
	if p == "" {
		return "<default>"
	}
	return p
}

func typeStr(t *analysis.TFType) string {
	if t == nil {
		return "<none>"
	}
	return t.String()
}

// typeEqual performs a deep structural comparison of two TFTypes, including
// the Optional flag on object fields.
func typeEqual(a, b *analysis.TFType) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.Kind != b.Kind || a.Optional != b.Optional {
		return false
	}
	if !typeEqual(a.Elem, b.Elem) {
		return false
	}
	if len(a.Fields) != len(b.Fields) {
		return false
	}
	for k, av := range a.Fields {
		if !typeEqual(av, b.Fields[k]) {
			return false
		}
	}
	if len(a.Elems) != len(b.Elems) {
		return false
	}
	for i := range a.Elems {
		if !typeEqual(a.Elems[i], b.Elems[i]) {
			return false
		}
	}
	return true
}

// diffObjectFields emits per-field changes when two object types differ.
// Returns the number of field-level changes appended.
func diffObjectFields(subject string, oldT, newT *analysis.TFType, oldPos, newPos token.Position, changes *[]Change) int {
	if oldT == nil || newT == nil || oldT.Kind != analysis.TypeObject || newT.Kind != analysis.TypeObject {
		return 0
	}
	n := 0
	for name, oldField := range oldT.Fields {
		newField, present := newT.Fields[name]
		if !present {
			kind := Breaking
			detail := fmt.Sprintf("object field %q removed", name)
			if oldField != nil && oldField.Optional {
				// Callers may not have been passing it anyway.
				kind = NonBreaking
				detail = fmt.Sprintf("optional object field %q removed", name)
			}
			*changes = append(*changes, Change{
				Kind:    kind,
				Subject: subject,
				Detail:  detail,
				OldPos:  oldPos,
				NewPos:  newPos,
			})
			n++
			continue
		}
		// Optionality transitions
		if oldField != nil && newField != nil && oldField.Optional && !newField.Optional {
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: subject,
				Detail:  fmt.Sprintf("object field %q became required", name),
				OldPos:  oldPos,
				NewPos:  newPos,
			})
			n++
		}
		if oldField != nil && newField != nil && !oldField.Optional && newField.Optional {
			*changes = append(*changes, Change{
				Kind:    NonBreaking,
				Subject: subject,
				Detail:  fmt.Sprintf("object field %q became optional", name),
				OldPos:  oldPos,
				NewPos:  newPos,
			})
			n++
		}
		// Inner-type change (ignoring optionality)
		if !sameInnerType(oldField, newField) {
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: subject,
				Detail: fmt.Sprintf("object field %q type changed: %s → %s",
					name, typeStr(oldField), typeStr(newField)),
				OldPos: oldPos,
				NewPos: newPos,
			})
			n++
		}
	}
	for name, newField := range newT.Fields {
		if _, present := oldT.Fields[name]; present {
			continue
		}
		kind := Breaking
		detail := fmt.Sprintf("required object field %q added", name)
		if newField != nil && newField.Optional {
			kind = NonBreaking
			detail = fmt.Sprintf("optional object field %q added", name)
		}
		*changes = append(*changes, Change{
			Kind:    kind,
			Subject: subject,
			Detail:  detail,
			OldPos:  oldPos,
			NewPos:  newPos,
		})
		n++
	}
	return n
}

// sameInnerType compares two object-field types while ignoring their Optional
// flags (the caller handles optionality transitions separately).
func sameInnerType(a, b *analysis.TFType) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	ac, bc := *a, *b
	ac.Optional = false
	bc.Optional = false
	return typeEqual(&ac, &bc)
}
