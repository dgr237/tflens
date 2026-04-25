// Package statediff is a static hazard detector for Terraform PRs.
// It compares two loaded projects (old vs new) at the resource-
// identity level and surfaces:
//
//   - Resources added or removed between the two trees
//   - Locals or variable defaults whose value changed AND whose
//     dependency chain reaches a count or for_each meta-argument
//   - Renames declared via `moved {}` blocks (to suppress the
//     paired add+remove that would otherwise look like destroy+
//     recreate)
//   - When a Terraform state file is supplied, the concrete
//     state addresses each flagged resource may affect
//
// Pure analysis — no Terraform execution, no provider schemas, no
// network. Suitable for CI gating via Result.FlaggedCount().
package statediff

import (
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/tfstate"
)

// Result aggregates everything Analyze produces. Field tags are part
// of the public CLI JSON contract; don't rename them without bumping
// a major version.
type Result struct {
	BaseRef          string            `json:"base_ref"`
	Path             string            `json:"path"`
	AddedResources   []ResourceRef     `json:"added_resources"`
	RemovedResources []ResourceRef     `json:"removed_resources"`
	RenamedResources []RenamePair      `json:"renamed_resources,omitempty"`
	SensitiveChanges []SensitiveChange `json:"sensitive_changes"`
	StateOrphans     []string          `json:"state_orphans,omitempty"`
}

// FlaggedCount returns the number of findings that count toward the
// CI exit code: added + removed resources + sensitive changes.
// Renames are Terraform-handled, and state orphans are pre-existing
// drift rather than changes introduced by the PR — both are noted
// but excluded from the gate.
func (r Result) FlaggedCount() int {
	return len(r.AddedResources) + len(r.RemovedResources) + len(r.SensitiveChanges)
}

// ResourceRef identifies one resource declaration by its module
// path + type + name. Mode distinguishes managed resources ("managed")
// from data sources ("data").
type ResourceRef struct {
	Module string `json:"module"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	Mode   string `json:"mode"`
}

// Address returns the Terraform-style fully-qualified address —
// `module.x.aws_vpc.main` or `aws_vpc.main` for the root module.
func (r ResourceRef) Address() string {
	if r.Module == "" {
		return r.Type + "." + r.Name
	}
	return r.Module + "." + r.Type + "." + r.Name
}

// RenamePair records a `moved { from = X, to = Y }` block recognised
// as a real rename (i.e. the from-side really disappeared and the
// to-side really appeared). Module is the dotted path the moved block
// lives in — both sides share that prefix.
type RenamePair struct {
	Module string `json:"module"`
	From   string `json:"from"` // entity ID, e.g. "resource.aws_vpc.old"
	To     string `json:"to"`   // entity ID, e.g. "resource.aws_vpc.new"
}

// FromAddress returns the from-side as a Terraform address.
func (r RenamePair) FromAddress() string { return formatEntityAddress(r.Module, r.From) }

// ToAddress returns the to-side as a Terraform address.
func (r RenamePair) ToAddress() string { return formatEntityAddress(r.Module, r.To) }

// SensitiveChange captures either a local's value change or a
// variable's default change that reaches a count/for_each expansion
// — the silent class of bug where editing a list quietly changes how
// many resource instances exist.
type SensitiveChange struct {
	Module            string             `json:"module"`
	Kind              string             `json:"kind"` // "local" or "variable"
	Name              string             `json:"name"`
	OldValue          string             `json:"old_value"`
	NewValue          string             `json:"new_value"`
	AffectedResources []AffectedResource `json:"affected_resources"`
}

// AffectedResource is one resource expansion (count / for_each) whose
// underlying expression reaches a SensitiveChange. StateInstances is
// populated when Analyze was given a state — these are the concrete
// addresses currently tracked in state.
type AffectedResource struct {
	Module         string   `json:"module"`
	Type           string   `json:"type"`
	Name           string   `json:"name"`
	MetaArg        string   `json:"meta_arg"` // "count" or "for_each"
	StateInstances []string `json:"state_instances,omitempty"`
}

// Address returns the resource's Terraform-style address (without
// instance index — those are in StateInstances).
func (a AffectedResource) Address() string {
	base := a.Type + "." + a.Name
	if a.Module == "" {
		return base
	}
	return a.Module + "." + base
}

// Analyze produces the Result for one (oldProj, newProj) diff.
// state is optional — when nil, AffectedResource.StateInstances is
// always empty and Result.StateOrphans is always nil.
func Analyze(oldProj, newProj *loader.Project, state *tfstate.State) Result {
	oldMods := walkAllModules(oldProj)
	newMods := walkAllModules(newProj)

	result := Result{}
	result.AddedResources, result.RemovedResources, result.RenamedResources = diffResources(oldMods, newMods)
	result.SensitiveChanges = detectSensitiveChanges(oldMods, newMods, state)
	if state != nil {
		result.StateOrphans = detectStateOrphans(state, newMods)
	}
	return result
}

// formatEntityAddress turns "resource.aws_vpc.main" + module prefix
// into a Terraform-style address "module.vpc.aws_vpc.main". Returns
// the raw ID if the shape is unfamiliar.
func formatEntityAddress(modPath, entityID string) string {
	addr := entityID
	if strings.HasPrefix(entityID, "resource.") {
		addr = strings.TrimPrefix(entityID, "resource.")
	}
	if modPath == "" {
		return addr
	}
	return modPath + "." + addr
}

// walkAllModules returns every module in the project tree keyed by
// its dotted module path from the root (empty string for the root
// itself).
func walkAllModules(p *loader.Project) map[string]*loader.ModuleNode {
	out := map[string]*loader.ModuleNode{}
	if p == nil || p.Root == nil {
		return out
	}
	var walk func(prefix string, n *loader.ModuleNode)
	walk = func(prefix string, n *loader.ModuleNode) {
		if n == nil {
			return
		}
		out[prefix] = n
		for name, child := range n.Children {
			key := "module." + name
			if prefix != "" {
				key = prefix + ".module." + name
			}
			walk(key, child)
		}
	}
	walk("", p.Root)
	return out
}

// ---- resource identity diff ----

func diffResources(oldMods, newMods map[string]*loader.ModuleNode) (added, removed []ResourceRef, renamed []RenamePair) {
	oldSet := collectResources(oldMods)
	newSet := collectResources(newMods)

	// A rename declared by a `moved { from = X, to = Y }` block in the
	// new tree is only genuine when:
	//   - old tree has `from`, new tree doesn't (we really lost the old name)
	//   - new tree has `to`, old tree doesn't (the new name really is new)
	// Dangling moved blocks (to still absent, or from still present)
	// are ignored so we don't hide real adds/removes behind them.
	handled := map[string]bool{}
	for modPath, node := range newMods {
		if node == nil || node.Module == nil {
			continue
		}
		for from, to := range node.Module.Moved() {
			if !strings.HasPrefix(from, "resource.") || !strings.HasPrefix(to, "resource.") {
				continue
			}
			fromKey := entityKey(modPath, from)
			toKey := entityKey(modPath, to)
			_, oldHasFrom := oldSet[fromKey]
			_, oldHasTo := oldSet[toKey]
			_, newHasFrom := newSet[fromKey]
			_, newHasTo := newSet[toKey]
			if oldHasFrom && newHasTo && !oldHasTo && !newHasFrom {
				handled[fromKey] = true
				handled[toKey] = true
				renamed = append(renamed, RenamePair{Module: modPath, From: from, To: to})
			}
		}
	}

	for k, r := range newSet {
		if handled[k] {
			continue
		}
		if _, ok := oldSet[k]; !ok {
			added = append(added, r)
		}
	}
	for k, r := range oldSet {
		if handled[k] {
			continue
		}
		if _, ok := newSet[k]; !ok {
			removed = append(removed, r)
		}
	}
	sortResourceRefs(added)
	sortResourceRefs(removed)
	sort.Slice(renamed, func(i, j int) bool {
		if renamed[i].Module != renamed[j].Module {
			return renamed[i].Module < renamed[j].Module
		}
		return renamed[i].From < renamed[j].From
	})
	return added, removed, renamed
}

func entityKey(modPath, entityID string) string {
	return modPath + "|" + entityID
}

func collectResources(mods map[string]*loader.ModuleNode) map[string]ResourceRef {
	out := map[string]ResourceRef{}
	for modPath, node := range mods {
		if node == nil || node.Module == nil {
			continue
		}
		for _, e := range node.Module.Entities() {
			if e.Kind != analysis.KindResource && e.Kind != analysis.KindData {
				continue
			}
			mode := "managed"
			if e.Kind == analysis.KindData {
				mode = "data"
			}
			r := ResourceRef{Module: modPath, Type: e.Type, Name: e.Name, Mode: mode}
			out[entityKey(modPath, e.ID())] = r
		}
	}
	return out
}

func sortResourceRefs(rs []ResourceRef) {
	sort.Slice(rs, func(i, j int) bool { return rs[i].Address() < rs[j].Address() })
}

// ---- sensitive-change detection ----

func detectSensitiveChanges(oldMods, newMods map[string]*loader.ModuleNode, state *tfstate.State) []SensitiveChange {
	var out []SensitiveChange
	for modPath, newNode := range newMods {
		oldNode, ok := oldMods[modPath]
		if !ok || oldNode == nil || newNode == nil {
			continue
		}
		changes := collectSensitiveCandidates(oldNode.Module, newNode.Module)
		if len(changes) == 0 {
			continue
		}
		targetIDs := map[string]bool{}
		for _, c := range changes {
			targetIDs[c.entityID()] = true
		}
		for _, e := range newNode.Module.Filter(analysis.KindResource) {
			flagResource(modPath, e, newNode.Module, changes, targetIDs, state, &out)
		}
		for _, e := range newNode.Module.Filter(analysis.KindData) {
			flagResource(modPath, e, newNode.Module, changes, targetIDs, state, &out)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Module != out[j].Module {
			return out[i].Module < out[j].Module
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// valueChange records a changed local or variable default. Internal:
// callers see SensitiveChange after merging.
type valueChange struct {
	kind             string // "local" or "variable"
	name             string
	oldText, newText string
}

func (c valueChange) entityID() string {
	if c.kind == "variable" {
		return "variable." + c.name
	}
	return "local." + c.name
}

func collectSensitiveCandidates(oldMod, newMod *analysis.Module) []valueChange {
	var out []valueChange
	out = append(out, diffValues("local", localsMap(oldMod), localsMap(newMod))...)
	out = append(out, diffValues("variable", variableDefaultsMap(oldMod), variableDefaultsMap(newMod))...)
	return out
}

// diffValues pairs same-named entries from oldV and newV and emits
// one valueChange per real difference. Comparison is text-first
// (cheap, conservative); when texts differ but BOTH sides evaluated
// to a cty.Value cleanly, value equality is the tiebreaker — text-
// different / value-identical pairs are suppressed (e.g. a literal
// list compared to sort() of the same elements).
//
// When evaluation isn't possible on either side (references
// something outside the EvalContext, uses an out-of-stdlib function),
// the value-equality check is skipped and the conservative text-only
// behaviour applies. That keeps the false-positive bias intact for
// expressions tflens can't reason about statically.
func diffValues(kind string, oldV, newV map[string]valueRef) []valueChange {
	var out []valueChange
	for name, old := range oldV {
		neu, ok := newV[name]
		if !ok {
			out = append(out, valueChange{kind: kind, name: name, oldText: old.text})
			continue
		}
		if old.text == neu.text {
			continue
		}
		if old.ok && neu.ok && analysis.ValueEquivalent(old.val, neu.val) {
			continue
		}
		out = append(out, valueChange{kind: kind, name: name, oldText: old.text, newText: neu.text})
	}
	for name, neu := range newV {
		if _, ok := oldV[name]; !ok {
			out = append(out, valueChange{kind: kind, name: name, newText: neu.text})
		}
	}
	return out
}

func flagResource(
	modPath string,
	e analysis.Entity,
	mod *analysis.Module,
	candidates []valueChange,
	targetIDs map[string]bool,
	state *tfstate.State,
	out *[]SensitiveChange,
) {
	for _, metaArg := range []struct {
		name string
		expr *analysis.Expr
	}{
		{"count", e.CountExpr},
		{"for_each", e.ForEachExpr},
	} {
		if metaArg.expr == nil {
			continue
		}
		triggered := refsReachingTargets(mod, metaArg.expr, targetIDs)
		for _, cand := range candidates {
			if !triggered[cand.entityID()] {
				continue
			}
			affected := AffectedResource{
				Module:  modPath,
				Type:    e.Type,
				Name:    e.Name,
				MetaArg: metaArg.name,
			}
			if state != nil {
				affected.StateInstances = matchingStateInstances(state, modPath, e)
			}
			mergeSensitive(out, modPath, cand, affected)
		}
	}
}

func mergeSensitive(out *[]SensitiveChange, modPath string, cand valueChange, r AffectedResource) {
	for i := range *out {
		if (*out)[i].Module == modPath && (*out)[i].Kind == cand.kind && (*out)[i].Name == cand.name {
			(*out)[i].AffectedResources = append((*out)[i].AffectedResources, r)
			return
		}
	}
	*out = append(*out, SensitiveChange{
		Module:            modPath,
		Kind:              cand.kind,
		Name:              cand.name,
		OldValue:          cand.oldText,
		NewValue:          cand.newText,
		AffectedResources: []AffectedResource{r},
	})
}

// valueRef pairs an expression's canonical text with its
// statically-evaluated cty.Value (when evaluation succeeds). The
// text is what diffValues compares first; the value enables a
// secondary equality check that suppresses false positives when
// text differs but the effective value doesn't (e.g.
// `["a","b"]` vs `sort(["b","a"])`).
//
// ok is false when the expression couldn't be evaluated cleanly —
// references something not in the EvalContext, uses a Terraform
// function not in the curated stdlib set, etc. In that case
// diffValues falls back to text-only comparison (the conservative
// path: flag the change if texts differ).
type valueRef struct {
	text string
	val  cty.Value
	ok   bool
}

func localsMap(m *analysis.Module) map[string]valueRef {
	out := map[string]valueRef{}
	if m == nil {
		return out
	}
	ctx := m.EvalContext()
	for _, e := range m.Filter(analysis.KindLocal) {
		ref := valueRef{}
		if e.LocalExpr != nil {
			ref.text = e.LocalExpr.Text()
			if v, diags := e.LocalExpr.E.Value(ctx); !diags.HasErrors() {
				ref.val = v
				ref.ok = true
			}
		}
		out[e.Name] = ref
	}
	return out
}

func variableDefaultsMap(m *analysis.Module) map[string]valueRef {
	out := map[string]valueRef{}
	if m == nil {
		return out
	}
	ctx := m.EvalContext()
	for _, e := range m.Filter(analysis.KindVariable) {
		ref := valueRef{}
		if e.DefaultExpr != nil {
			ref.text = e.DefaultExpr.Text()
			if v, diags := e.DefaultExpr.E.Value(ctx); !diags.HasErrors() {
				ref.val = v
				ref.ok = true
			}
		}
		out[e.Name] = ref
	}
	return out
}

func refsReachingTargets(mod *analysis.Module, expr *analysis.Expr, targets map[string]bool) map[string]bool {
	hits := map[string]bool{}
	if expr == nil || expr.E == nil {
		return hits
	}
	for _, trav := range expr.E.Variables() {
		parts := traversalToParts(trav)
		id := refToEntityID(parts)
		if id == "" {
			continue
		}
		if targets[id] {
			hits[id] = true
			continue
		}
		for t := range targets {
			if transitivelyDependsOn(mod, id, t) {
				hits[t] = true
			}
		}
	}
	return hits
}

func traversalToParts(trav hcl.Traversal) []string {
	if len(trav) == 0 {
		return nil
	}
	var parts []string
	for i, step := range trav {
		switch s := step.(type) {
		case hcl.TraverseRoot:
			if i != 0 {
				return parts
			}
			parts = append(parts, s.Name)
		case hcl.TraverseAttr:
			parts = append(parts, s.Name)
		default:
			return parts
		}
	}
	return parts
}

func refToEntityID(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	switch parts[0] {
	case "local":
		if len(parts) >= 2 {
			return "local." + parts[1]
		}
	case "var":
		if len(parts) >= 2 {
			return "variable." + parts[1]
		}
	case "module":
		if len(parts) >= 2 {
			return "module." + parts[1]
		}
	case "data":
		if len(parts) >= 3 {
			return "data." + parts[1] + "." + parts[2]
		}
	default:
		if len(parts) >= 2 {
			return "resource." + parts[0] + "." + parts[1]
		}
	}
	return ""
}

func transitivelyDependsOn(mod *analysis.Module, from, to string) bool {
	if from == to {
		return true
	}
	seen := map[string]bool{from: true}
	frontier := mod.Dependencies(from)
	for len(frontier) > 0 {
		next := frontier[0]
		frontier = frontier[1:]
		if seen[next] {
			continue
		}
		seen[next] = true
		if next == to {
			return true
		}
		frontier = append(frontier, mod.Dependencies(next)...)
	}
	return false
}

func matchingStateInstances(state *tfstate.State, modPath string, e analysis.Entity) []string {
	mode := tfstate.ModeManaged
	if e.Kind == analysis.KindData {
		mode = tfstate.ModeData
	}
	idx := state.Index()
	r := idx[tfstate.AddressKey{Module: modPath, Mode: mode, Type: e.Type, Name: e.Name}]
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.Instances))
	for _, inst := range r.Instances {
		out = append(out, r.FullAddress(inst))
	}
	sort.Strings(out)
	return out
}

func detectStateOrphans(state *tfstate.State, newMods map[string]*loader.ModuleNode) []string {
	declared := map[tfstate.AddressKey]bool{}
	for modPath, node := range newMods {
		if node == nil || node.Module == nil {
			continue
		}
		for _, e := range node.Module.Entities() {
			if e.Kind != analysis.KindResource && e.Kind != analysis.KindData {
				continue
			}
			mode := tfstate.ModeManaged
			if e.Kind == analysis.KindData {
				mode = tfstate.ModeData
			}
			declared[tfstate.AddressKey{Module: modPath, Mode: mode, Type: e.Type, Name: e.Name}] = true
		}
	}
	var out []string
	for _, r := range state.Resources {
		key := tfstate.AddressKey{Module: r.Module, Mode: r.Mode, Type: r.Type, Name: r.Name}
		if declared[key] {
			continue
		}
		for _, inst := range r.Instances {
			out = append(out, r.FullAddress(inst))
		}
	}
	sort.Strings(out)
	return out
}
