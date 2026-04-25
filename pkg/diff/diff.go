// Package diff compares two module analyses and reports API-level changes.
// The diff is intended to answer: "if I upgrade from the old module to the
// new one, what breaks?"
package diff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty/convert"

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
	Hint    string         // optional one-line "how to fix this" guidance
	OldPos  token.Position // zero value for pure additions
	NewPos  token.Position // zero value for pure removals
	// Source records the change's provenance. Empty / "static" = the
	// source-side text-diff machinery; "plan" = derived from a
	// terraform plan JSON via diff.EnrichFromPlan. Renderers use this
	// to decorate plan-derived rows with a 📋 marker so reviewers
	// know which findings came from which path.
	Source string
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
				Hint:    "callers passing this variable will fail; document the migration or restore the variable",
				OldPos:  oe.Pos,
			})
			continue
		}
		if oe.HasDefault && !ne.HasDefault {
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: oe.ID(),
				Detail:  "default removed (variable became required)",
				Hint:    "keep the default; only remove it if every caller already passes the input explicitly",
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
				Hint:    "remove the nullable line, or document — callers passing null are now rejected",
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
				Hint:    "every output / local that consumes this value must also be marked sensitive = true",
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
		// Ephemeral transitions on variables (Terraform 1.10+).
		if !oe.Ephemeral && ne.Ephemeral {
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: oe.ID(),
				Detail:  "`ephemeral = true` added (callers must now pass an ephemeral value)",
				Hint:    "callers must pass a value that's also ephemeral (Terraform 1.10+); revert if not intentional",
				OldPos:  oe.Pos,
				NewPos:  ne.Pos,
			})
		} else if oe.Ephemeral && !ne.Ephemeral {
			*changes = append(*changes, Change{
				Kind:    NonBreaking,
				Subject: oe.ID(),
				Detail:  "`ephemeral = true` removed (variable now persisted to state)",
				OldPos:  oe.Pos,
				NewPos:  ne.Pos,
			})
		}
		// Validation / precondition / postcondition content changes
		// (variables). Comparing canonical condition text catches the case
		// where one block is removed and another is added with a different
		// condition — the count is unchanged but the rule set differs.
		diffConditionSet(oe.ID(), analysis.ConditionTexts(oe.Validations), analysis.ConditionTexts(ne.Validations), oe.Pos, ne.Pos,
			"validation block",
			"may reject previously-valid inputs",
			"accepts a wider input set", changes)
		diffConditionSet(oe.ID(), analysis.ConditionTexts(oe.Preconditions), analysis.ConditionTexts(ne.Preconditions), oe.Pos, ne.Pos,
			"precondition",
			"may reject previously-valid inputs",
			"loosens the precondition contract", changes)
		diffConditionSet(oe.ID(), analysis.ConditionTexts(oe.Postconditions), analysis.ConditionTexts(ne.Postconditions), oe.Pos, ne.Pos,
			"postcondition",
			"may reject previously-valid state",
			"loosens the postcondition contract", changes)
		if !typeEqual(oe.DeclaredType, ne.DeclaredType) {
			// When both types are objects, emit per-field changes instead
			// of a blanket "type changed".
			if isObjectType(oe.DeclaredType) && isObjectType(ne.DeclaredType) {
				if diffObjectFields(oe.ID(), oe.DeclaredType, ne.DeclaredType, oe.Pos, ne.Pos, changes) > 0 {
					checkDefaultStillValid(oe, ne, changes)
					continue
				}
			}
			kind := Breaking
			label := "type changed"
			hint := ""
			switch compareTypes(oe.DeclaredType, ne.DeclaredType) {
			case typeRelWidened:
				kind = NonBreaking
				label = "type widened"
			case typeRelNarrowed:
				kind = Breaking
				label = "type narrowed"
				hint = "keep the wider type and add a validation block instead, or document the migration"
			case typeRelEqual:
				// Reachable when typeEqual is structurally false but cty
				// considers the types equivalent (e.g. tuple shapes that
				// happen to match list element types).
				kind = NonBreaking
				label = "type equivalent"
			default:
				hint = "this is an incompatible type change; revert or document the migration"
			}
			*changes = append(*changes, Change{
				Kind:    kind,
				Subject: oe.ID(),
				Detail: fmt.Sprintf("%s: %s → %s",
					label, typeStr(oe.DeclaredType), typeStr(ne.DeclaredType)),
				Hint:   hint,
				OldPos: oe.Pos,
				NewPos: ne.Pos,
			})
			checkDefaultStillValid(oe, ne, changes)
		}
	}

	for name, ne := range newVars {
		if _, present := oldVars[name]; present {
			continue
		}
		kind := NonBreaking
		detail := "variable added (optional)"
		hint := ""
		if !ne.HasDefault {
			kind = Breaking
			detail = "required variable added (no default)"
			hint = "add `default = ...` to make it optional, or document that callers must set it"
		}
		*changes = append(*changes, Change{
			Kind:    kind,
			Subject: ne.ID(),
			Detail:  detail,
			Hint:    hint,
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
				Hint:    "callers using `module.X." + oe.Name + "` will fail; restore the output, rename via alias, or document the migration",
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
				Kind:    Breaking,
				Subject: oe.ID(),
				Detail:  "output sensitive flag dropped (sensitive leak — value previously masked is now exposed in logs and downstream consumers)",
				Hint:    "if accidental, restore `sensitive = true`; if intentional, audit downstream uses for log exposure",
				OldPos:  oe.Pos,
				NewPos:  ne.Pos,
			})
		}
		// Ephemeral transitions on outputs (Terraform 1.10+).
		if !oe.Ephemeral && ne.Ephemeral {
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: oe.ID(),
				Detail:  "output became ephemeral (consumers expecting a persisted value will fail)",
				Hint:    "consumers will no longer find this value in state; revert if not intentional",
				OldPos:  oe.Pos,
				NewPos:  ne.Pos,
			})
		} else if oe.Ephemeral && !ne.Ephemeral {
			*changes = append(*changes, Change{
				Kind:    NonBreaking,
				Subject: oe.ID(),
				Detail:  "output ephemeral flag removed (value now persisted to state)",
				OldPos:  oe.Pos,
				NewPos:  ne.Pos,
			})
		}
		// Precondition / postcondition content changes (outputs)
		diffConditionSet(oe.ID(), analysis.ConditionTexts(oe.Preconditions), analysis.ConditionTexts(ne.Preconditions), oe.Pos, ne.Pos,
			"output precondition",
			"may reject previously-valid state",
			"loosens the output precondition contract", changes)
		diffConditionSet(oe.ID(), analysis.ConditionTexts(oe.Postconditions), analysis.ConditionTexts(ne.Postconditions), oe.Pos, ne.Pos,
			"output postcondition",
			"may reject previously-valid state",
			"loosens the output postcondition contract", changes)
		diffDependsOn(oe.ID(), oe, ne, changes)
		// Value-expression shape changes
		if oe.ValueExpr != nil && ne.ValueExpr != nil {
			typeBreaking := emitOutputValueChange(oldMod, newMod, oe, ne, changes)
			if !typeBreaking && oe.ValueExpr.Text() == ne.ValueExpr.Text() {
				// Expression text is identical AND type compatible, but a
				// referenced local may have changed.
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

	diffBackend(oldMod.Backend(), newMod.Backend(), changes)
}

// diffBackend reports terraform { backend "X" { ... } } changes. Adding,
// removing, switching backend type, or changing any config attribute moves
// the state location and forces a `terraform init -migrate-state` — all
// classified as Breaking.
func diffBackend(oldB, newB *analysis.Backend, changes *[]Change) {
	const subject = "terraform.backend"
	const initMigrateHint = "run `terraform init -migrate-state` to relocate the existing state"
	switch {
	case oldB == nil && newB == nil:
		return
	case oldB == nil && newB != nil:
		*changes = append(*changes, Change{
			Kind:    Breaking,
			Subject: subject,
			Detail: fmt.Sprintf("backend %q added (state migrates from local to remote — run `terraform init -migrate-state`)",
				newB.Type),
			Hint:   initMigrateHint,
			NewPos: newB.Pos,
		})
		return
	case oldB != nil && newB == nil:
		*changes = append(*changes, Change{
			Kind:    Breaking,
			Subject: subject,
			Detail: fmt.Sprintf("backend %q removed (state migrates back to local — run `terraform init -migrate-state`)",
				oldB.Type),
			Hint:   initMigrateHint,
			OldPos: oldB.Pos,
		})
		return
	}
	if oldB.Type != newB.Type {
		*changes = append(*changes, Change{
			Kind:    Breaking,
			Subject: subject,
			Detail: fmt.Sprintf("backend type changed: %q → %q (state moves between providers — run `terraform init -migrate-state`)",
				oldB.Type, newB.Type),
			Hint:   initMigrateHint,
			OldPos: oldB.Pos,
			NewPos: newB.Pos,
		})
		return
	}
	// Same backend type; report per-attribute changes. Any config change is
	// potentially state-moving.
	keys := map[string]bool{}
	for k := range oldB.Config {
		keys[k] = true
	}
	for k := range newB.Config {
		keys[k] = true
	}
	for k := range keys {
		oldV, oldOK := oldB.Config[k]
		newV, newOK := newB.Config[k]
		switch {
		case oldOK && !newOK:
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: subject,
				Detail: fmt.Sprintf("backend %q config: attribute %q removed (was %s) — may relocate state",
					oldB.Type, k, oldV),
				Hint:   initMigrateHint,
				OldPos: oldB.Pos,
				NewPos: newB.Pos,
			})
		case !oldOK && newOK:
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: subject,
				Detail: fmt.Sprintf("backend %q config: attribute %q added (now %s) — may relocate state",
					newB.Type, k, newV),
				Hint:   initMigrateHint,
				OldPos: oldB.Pos,
				NewPos: newB.Pos,
			})
		case oldV != newV:
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: subject,
				Detail: fmt.Sprintf("backend %q config: %q changed: %s → %s (run `terraform init -migrate-state` if the location moved)",
					oldB.Type, k, oldV, newV),
				Hint:   initMigrateHint,
				OldPos: oldB.Pos,
				NewPos: newB.Pos,
			})
		}
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
				Hint:   "add `moved {}` blocks per instance (e.g. moved { from = " + id + "[0], to = " + id + "[\"key\"] }) to migrate state without recreate",
				OldPos: oe.Pos,
				NewPos: ne.Pos,
			})
		} else {
			// Mode unchanged — but the expression content might differ, which
			// changes which keys/indices exist at plan time. Type analysis
			// also runs even when the text is identical, since a referenced
			// variable's type may have changed underneath.
			if oe.HasForEach && ne.HasForEach {
				emitForEachChange(oldMod, newMod, id, oe, ne, changes)
			}
			if oe.HasCount && ne.HasCount {
				emitCountChange(oldMod, newMod, id, oe, ne, changes)
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
					Hint:   "Terraform recreates the resource on the new provider; use `terraform state mv` if the existing instance must be retained",
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
			Hint:    "add `moved { from = " + p.old.ID() + ", to = " + p.new.ID() + " }` so Terraform migrates state in place",
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
			Hint:    "if intentional, add `removed { from = " + oe.ID() + " }` to keep the existing infrastructure without recreating",
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
		oldAll := isIgnoreChangesAll(oe.IgnoreChangesExpr)
		newAll := isIgnoreChangesAll(ne.IgnoreChangesExpr)
		switch {
		case oldAll && !newAll:
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: id,
				Detail: fmt.Sprintf("`ignore_changes` narrowed: all → %s (drift detection now fires on attributes that were previously ignored)",
					displayIgnoreList(ne.IgnoreChangesExpr.Text())),
				Hint:   "drift detection will fire on attributes that were previously suppressed; revert to `all` if not intentional",
				OldPos: oe.Pos,
				NewPos: ne.Pos,
			})
		case !oldAll && newAll:
			*changes = append(*changes, Change{
				Kind:    NonBreaking,
				Subject: id,
				Detail: fmt.Sprintf("`ignore_changes` widened to all (was %s — drift detection now suppressed for every attribute)",
					displayIgnoreList(oe.IgnoreChangesExpr.Text())),
				OldPos: oe.Pos,
				NewPos: ne.Pos,
			})
		default:
			*changes = append(*changes, Change{
				Kind:    Informational,
				Subject: id,
				Detail: fmt.Sprintf("`ignore_changes` changed: %s → %s (drift detection behaviour differs)",
					displayIgnoreList(s), displayIgnoreList(ne.IgnoreChangesExpr.Text())),
				OldPos: oe.Pos,
				NewPos: ne.Pos,
			})
		}
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
	// Lifecycle precondition / postcondition content
	diffConditionSet(id, analysis.ConditionTexts(oe.Preconditions), analysis.ConditionTexts(ne.Preconditions), oe.Pos, ne.Pos,
		"lifecycle precondition",
		"may reject previously-valid plans",
		"loosens the lifecycle precondition contract", changes)
	diffConditionSet(id, analysis.ConditionTexts(oe.Postconditions), analysis.ConditionTexts(ne.Postconditions), oe.Pos, ne.Pos,
		"lifecycle postcondition",
		"may reject previously-valid state",
		"loosens the lifecycle postcondition contract", changes)
}

// diffConditionSet compares two multisets of canonical condition texts
// (each from a validation/precondition/postcondition block) and emits
// Informational changes for added and removed conditions. Identical-text
// blocks on both sides cancel out — moving or reordering blocks is a
// no-op. label is the singular block-kind name (e.g. "validation block").
// addedReason and removedReason follow "—" in the emitted detail.
func diffConditionSet(subject string, oldConds, newConds []string, oldPos, newPos token.Position, label, addedReason, removedReason string, changes *[]Change) {
	added, removed := multisetDiff(oldConds, newConds)
	if len(added) > 0 {
		*changes = append(*changes, Change{
			Kind:    Informational,
			Subject: subject,
			Detail: fmt.Sprintf("%d new %s(s) — %s: %s",
				len(added), label, addedReason, strings.Join(quoteAll(added), ", ")),
			OldPos: oldPos,
			NewPos: newPos,
		})
	}
	if len(removed) > 0 {
		*changes = append(*changes, Change{
			Kind:    Informational,
			Subject: subject,
			Detail: fmt.Sprintf("%d %s(s) removed — %s: %s",
				len(removed), label, removedReason, strings.Join(quoteAll(removed), ", ")),
			OldPos: oldPos,
			NewPos: newPos,
		})
	}
}

// multisetDiff returns the elements that need to be added to old to obtain
// new (added), and removed from old (removed). Both results are sorted.
// Equal counts of the same string on both sides cancel out.
func multisetDiff(oldS, newS []string) (added, removed []string) {
	count := map[string]int{}
	for _, s := range oldS {
		count[s]--
	}
	for _, s := range newS {
		count[s]++
	}
	for s, n := range count {
		switch {
		case n > 0:
			for i := 0; i < n; i++ {
				added = append(added, s)
			}
		case n < 0:
			for i := 0; i < -n; i++ {
				removed = append(removed, s)
			}
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		if s == "" {
			out[i] = "<no condition>"
		} else {
			out[i] = fmt.Sprintf("`%s`", s)
		}
	}
	return out
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

// typeRel classifies the relationship between two declared types from the
// perspective of a caller upgrading from old to new.
type typeRel int

const (
	typeRelEqual        typeRel = iota // identical or cty-equivalent
	typeRelWidened                     // every old value is still acceptable to new
	typeRelNarrowed                    // some old values are now rejected
	typeRelIncompatible                // unrelated shapes
)

// compareTypes returns the relationship between oldT and newT. When both
// types carry their underlying cty.Type (i.e. came from ParseTypeExpr), the
// answer uses cty.Convert as the assignability oracle. Otherwise we fall
// back to the structural typeEqual / kind-mismatch check.
func compareTypes(oldT, newT *analysis.TFType) typeRel {
	if typeEqual(oldT, newT) {
		return typeRelEqual
	}
	if !oldT.HasCty() || !newT.HasCty() {
		// Best effort: any-widening is non-breaking; everything else is
		// incompatible at this level.
		if isAnyType(newT) && !isAnyType(oldT) {
			return typeRelWidened
		}
		return typeRelIncompatible
	}
	canForward := convert.GetConversion(oldT.Cty, newT.Cty) != nil
	canBackward := convert.GetConversion(newT.Cty, oldT.Cty) != nil
	switch {
	case canForward && canBackward:
		return typeRelEqual
	case canForward:
		return typeRelWidened
	case canBackward:
		return typeRelNarrowed
	default:
		return typeRelIncompatible
	}
}

// emitOutputValueChange evaluates an output's value expression on both
// sides. When both yield a known inferred type and the new type narrows or
// is incompatible with the old, emits a Breaking change and returns true
// (downstream consumers expecting the old type will fail) — this fires
// whether or not the expression text changed, so it catches the case where
// the same `var.X` reference is reused but X's declared type narrowed.
// Otherwise emits the existing Informational text change when the text
// differs, and returns false so the caller can run further checks
// (indirect-locals).
func emitOutputValueChange(oldMod, newMod *analysis.Module, oe, ne analysis.Entity, changes *[]Change) bool {
	oldType := oldMod.InferExprType(oe.ValueExpr.E)
	newType := newMod.InferExprType(ne.ValueExpr.E)

	if oldType != nil && newType != nil &&
		oldType.Kind != analysis.TypeUnknown && newType.Kind != analysis.TypeUnknown {
		switch compareTypes(oldType, newType) {
		case typeRelNarrowed, typeRelIncompatible:
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: oe.ID(),
				Detail: fmt.Sprintf("output type changed: %s → %s (downstream consumers expecting %s will fail)",
					typeStr(oldType), typeStr(newType), typeStr(oldType)),
				Hint:   fmt.Sprintf("restore the wider %s type, or document that consumers must adjust", typeStr(oldType)),
				OldPos: oe.Pos,
				NewPos: ne.Pos,
			})
			return true
		}
	}
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
	}
	return false
}

// emitForEachChange handles a stateful entity's for_each expression. When
// key types on both sides are known and have narrowed or become
// incompatible, emits a Breaking change — every instance will be
// re-addressed under a different key. This fires whether or not the
// for_each expression text changed (so we catch a referenced variable's
// type narrowing under a textually-unchanged `var.X`). Otherwise emits the
// existing Informational change when the text differs.
func emitForEachChange(oldMod, newMod *analysis.Module, id string, oe, ne analysis.Entity, changes *[]Change) {
	oldKey := forEachKeyType(oldMod.InferExprType(oe.ForEachExpr.E))
	newKey := forEachKeyType(newMod.InferExprType(ne.ForEachExpr.E))

	if oldKey != nil && newKey != nil {
		switch compareTypes(oldKey, newKey) {
		case typeRelNarrowed, typeRelIncompatible:
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: id,
				Detail: fmt.Sprintf("for_each key type changed: %s → %s (every instance will be recreated under new keys)",
					typeStr(oldKey), typeStr(newKey)),
				Hint:   "add `moved {}` blocks per instance to migrate state without recreate, or revert the key-type change",
				OldPos: oe.Pos,
				NewPos: ne.Pos,
			})
			return
		}
	}
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

// emitCountChange handles a stateful entity's count expression. count must
// evaluate to a number; if the new expression infers to anything else
// (list, object, bool, etc.) Terraform will reject the plan — Breaking.
// Otherwise emits the existing Informational text-change message.
func emitCountChange(oldMod, newMod *analysis.Module, id string, oe, ne analysis.Entity, changes *[]Change) {
	oldType := oldMod.InferExprType(oe.CountExpr.E)
	newType := newMod.InferExprType(ne.CountExpr.E)
	if newType != nil {
		switch newType.Kind {
		case analysis.TypeList, analysis.TypeSet, analysis.TypeMap, analysis.TypeObject, analysis.TypeTuple, analysis.TypeBool:
			*changes = append(*changes, Change{
				Kind:    Breaking,
				Subject: id,
				Detail: fmt.Sprintf("count expression type changed: %s → %s (count must be a number — plan will be rejected)",
					typeStr(oldType), typeStr(newType)),
				OldPos: oe.Pos,
				NewPos: ne.Pos,
			})
			return
		}
	}
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

// isIgnoreChangesAll reports whether the expression is the bare identifier
// `all` — Terraform's special form meaning "ignore drift on every
// attribute". Returns false for nil, list expressions, or anything else.
func isIgnoreChangesAll(e *analysis.Expr) bool {
	if e == nil || e.E == nil {
		return false
	}
	stv, ok := e.E.(*hclsyntax.ScopeTraversalExpr)
	if !ok || len(stv.Traversal) != 1 {
		return false
	}
	root, ok := stv.Traversal[0].(hcl.TraverseRoot)
	return ok && root.Name == "all"
}

// forEachKeyType returns the type that becomes the instance key when t is
// used as a for_each value. Maps and objects key by string; sets key by
// element. Returns nil for any other shape — including a TypeSet whose
// element type is unknown — so the caller can fall back to a textual
// comparison rather than guessing.
func forEachKeyType(t *analysis.TFType) *analysis.TFType {
	if t == nil {
		return nil
	}
	switch t.Kind {
	case analysis.TypeMap, analysis.TypeObject:
		return &analysis.TFType{Kind: analysis.TypeString}
	case analysis.TypeSet:
		return t.Elem
	}
	return nil
}

// checkDefaultStillValid emits an Informational change when a variable's
// declared type changed but the existing default value is still convertible
// to the new type — i.e. callers that relied on the default are unaffected.
func checkDefaultStillValid(oe, ne analysis.Entity, changes *[]Change) {
	if !oe.HasDefault || oe.DefaultExpr == nil || oe.DefaultExpr.E == nil {
		return
	}
	if ne.DeclaredType == nil || !ne.DeclaredType.HasCty() {
		return
	}
	val, diags := oe.DefaultExpr.E.Value(nil)
	if diags.HasErrors() {
		return
	}
	if _, err := convert.Convert(val, ne.DeclaredType.Cty); err != nil {
		return
	}
	*changes = append(*changes, Change{
		Kind:    Informational,
		Subject: oe.ID(),
		Detail:  "existing default value remains valid under the new type (callers using the default are unaffected)",
		OldPos:  oe.Pos,
		NewPos:  ne.Pos,
	})
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
			hint := ""
			if oldField != nil && oldField.Optional {
				// Callers may not have been passing it anyway.
				kind = NonBreaking
				detail = fmt.Sprintf("optional object field %q removed", name)
			} else {
				hint = fmt.Sprintf("deprecate first via optional(%s) before removing in a major release", typeStr(oldField))
			}
			*changes = append(*changes, Change{
				Kind:    kind,
				Subject: subject,
				Detail:  detail,
				Hint:    hint,
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
				Hint:    fmt.Sprintf("keep `optional(%s)`; only remove the wrapper if every caller already sets the field", typeStr(newField)),
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
		// Inner-type change (ignoring optionality). Classify via cty so
		// e.g. object({a=string}) → object({a=any}) is non-breaking.
		if !sameInnerType(oldField, newField) {
			kind := Breaking
			label := "type changed"
			switch compareTypes(oldField, newField) {
			case typeRelWidened:
				kind = NonBreaking
				label = "type widened"
			case typeRelEqual:
				// Cty considers them equivalent — skip.
				continue
			case typeRelNarrowed:
				kind = Breaking
				label = "type narrowed"
			}
			*changes = append(*changes, Change{
				Kind:    kind,
				Subject: subject,
				Detail: fmt.Sprintf("object field %q %s: %s → %s",
					name, label, typeStr(oldField), typeStr(newField)),
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
		hint := ""
		if newField != nil && newField.Optional {
			kind = NonBreaking
			detail = fmt.Sprintf("optional object field %q added", name)
		} else {
			hint = fmt.Sprintf("wrap with `optional(%s)` (Terraform 1.3+) so existing callers don't have to update their object literals", typeStr(newField))
		}
		*changes = append(*changes, Change{
			Kind:    kind,
			Subject: subject,
			Detail:  detail,
			Hint:    hint,
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
