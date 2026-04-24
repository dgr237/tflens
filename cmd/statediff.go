package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/tfstate"
)

var statediffCmd = &cobra.Command{
	Use:   "statediff [path]",
	Short: "Identify resources a PR may create, destroy, or re-instance",
	Long: `statediff compares the working tree in path (default cwd) against
a git ref at the resource identity level and surfaces changes that may
alter Terraform state when the PR is merged:

  1. Resource declarations added or removed between the two trees.
  2. Locals or variable defaults whose value expression changed AND
     whose dependency chain reaches a count or for_each meta-argument —
     the common way a seemingly-small edit silently destroys instances.
  3. Renames declared via ` + "`moved {}`" + ` blocks, recognised so the same
     resource under a new name is not double-reported as add + remove.
  4. When --state <file> is given: for every flagged resource, the
     instances currently in state (so a reviewer can see the concrete
     addresses that may be affected).

Exits non-zero when anything is flagged (renames and state orphans do
not count). Suitable for CI gating.

The ref defaults to 'auto', which resolves to @{upstream} → origin/HEAD
→ main → master.

What it does NOT do: attribute-level plan simulation. That needs
provider schemas and expression evaluation — run 'terraform plan' for
that. statediff is a static hazard detector, not a plan replacement.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "."
		if len(args) == 1 {
			path = args[0]
		}
		base, _ := cmd.Flags().GetString("ref")
		if base == RefAutoKeyword {
			auto, err := resolveAutoRef(path)
			if err != nil {
				return err
			}
			base = auto
		}
		statePath, _ := cmd.Flags().GetString("state")
		return runStatediff(cmd, path, base, statePath)
	},
}

func init() {
	statediffCmd.Flags().String("ref", RefAutoKeyword,
		"git ref to compare against (branch, tag, SHA, …); 'auto' detects @{upstream} → origin/HEAD → main → master")
	statediffCmd.Flags().String("state", "", "optional Terraform state v4 JSON file for instance cross-reference")
	rootCmd.AddCommand(statediffCmd)
}

func runStatediff(cmd *cobra.Command, path, baseRef, statePath string) error {
	newProj, err := loadProject(cmd, path)
	if err != nil {
		return fmt.Errorf("loading path: %w", err)
	}
	oldProj, cleanup, err := loadOldProjectForRef(cmd, path, baseRef)
	if err != nil {
		return err
	}
	defer cleanup()

	var state *tfstate.State
	if statePath != "" {
		state, err = tfstate.Parse(statePath)
		if err != nil {
			return fmt.Errorf("loading state: %w", err)
		}
	}

	result := analyzeStatediff(oldProj, newProj, state)
	result.BaseRef = baseRef
	result.Path = path

	if outputJSON(cmd) {
		exitJSON(result, diff.ExitCodeFor(result.flaggedCount()))
		return nil
	}
	printStatediff(&result)
	if result.flaggedCount() > 0 {
		os.Exit(1)
	}
	return nil
}

// ---- analysis core ----

// statediffResult is both the text-mode report aggregator and the JSON
// payload, so field tags matter.
type statediffResult struct {
	BaseRef           string            `json:"base_ref"`
	Path         string            `json:"path"`
	AddedResources    []resourceRef     `json:"added_resources"`
	RemovedResources  []resourceRef     `json:"removed_resources"`
	RenamedResources  []renamePair      `json:"renamed_resources,omitempty"`
	SensitiveChanges  []sensitiveChange `json:"sensitive_changes"`
	StateOrphans      []string          `json:"state_orphans,omitempty"`
}

func (r statediffResult) flaggedCount() int {
	// Renames and state orphans are noted but not counted as "flagged" —
	// renames are Terraform-handled, orphans are pre-existing drift
	// rather than changes introduced by this PR.
	return len(r.AddedResources) + len(r.RemovedResources) + len(r.SensitiveChanges)
}

type resourceRef struct {
	Module string `json:"module"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	Mode   string `json:"mode"`
}

func (r resourceRef) Address() string {
	if r.Module == "" {
		return r.Type + "." + r.Name
	}
	return r.Module + "." + r.Type + "." + r.Name
}

// renamePair records a moved-block-declared rename. The same module
// prefix applies to both from and to (moved blocks are scoped to one
// module), so it's carried once.
type renamePair struct {
	Module string `json:"module"`
	From   string `json:"from"` // entity ID, e.g. "resource.aws_vpc.old"
	To     string `json:"to"`   // entity ID, e.g. "resource.aws_vpc.new"
}

func (r renamePair) FromAddress() string { return formatEntityAddress(r.Module, r.From) }
func (r renamePair) ToAddress() string   { return formatEntityAddress(r.Module, r.To) }

// formatEntityAddress turns "resource.aws_vpc.main" + module prefix into
// a Terraform-style address "module.vpc.aws_vpc.main". Returns the raw
// ID if the shape is unfamiliar.
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

// sensitiveChange captures either a local's value change or a variable's
// default change that reaches a count/for_each expansion. Kind
// discriminates the two.
type sensitiveChange struct {
	Module            string             `json:"module"`
	Kind              string             `json:"kind"` // "local" or "variable"
	Name              string             `json:"name"`
	OldValue          string             `json:"old_value"`
	NewValue          string             `json:"new_value"`
	AffectedResources []affectedResource `json:"affected_resources"`
}

type affectedResource struct {
	Module         string   `json:"module"`
	Type           string   `json:"type"`
	Name           string   `json:"name"`
	MetaArg        string   `json:"meta_arg"` // "count" or "for_each"
	StateInstances []string `json:"state_instances,omitempty"`
}

func (a affectedResource) Address() string {
	base := a.Type + "." + a.Name
	if a.Module == "" {
		return base
	}
	return a.Module + "." + base
}

func analyzeStatediff(oldProj, newProj *loader.Project, state *tfstate.State) statediffResult {
	oldMods := walkAllModules(oldProj)
	newMods := walkAllModules(newProj)

	result := statediffResult{}
	result.AddedResources, result.RemovedResources, result.RenamedResources = diffResources(oldMods, newMods)
	result.SensitiveChanges = detectSensitiveChanges(oldMods, newMods, state)
	if state != nil {
		result.StateOrphans = detectStateOrphans(state, newMods)
	}
	return result
}

// walkAllModules returns every module in the project tree keyed by its
// dotted module path from the root (empty string for the root itself).
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

func diffResources(oldMods, newMods map[string]*loader.ModuleNode) (added, removed []resourceRef, renamed []renamePair) {
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
			// Skip module renames — statediff reports on resources.
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
				renamed = append(renamed, renamePair{Module: modPath, From: from, To: to})
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

// entityKey composes a unique map key combining module path with the
// canonical entity ID. Used so both collectResources and Moved() reads
// agree on how to address a resource.
func entityKey(modPath, entityID string) string {
	return modPath + "|" + entityID
}

func collectResources(mods map[string]*loader.ModuleNode) map[string]resourceRef {
	out := map[string]resourceRef{}
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
			r := resourceRef{Module: modPath, Type: e.Type, Name: e.Name, Mode: mode}
			out[entityKey(modPath, e.ID())] = r
		}
	}
	return out
}

func sortResourceRefs(rs []resourceRef) {
	sort.Slice(rs, func(i, j int) bool { return rs[i].Address() < rs[j].Address() })
}

// ---- sensitive-change detection (locals and variable defaults) ----

func detectSensitiveChanges(oldMods, newMods map[string]*loader.ModuleNode, state *tfstate.State) []sensitiveChange {
	var out []sensitiveChange
	// A "module" here is the whole sub-module at a given path. Locals
	// and variable defaults are scoped to their declaring module, so
	// we compare per-module.
	for modPath, newNode := range newMods {
		oldNode, ok := oldMods[modPath]
		if !ok || oldNode == nil || newNode == nil {
			continue // module added/removed is already reflected by resource diff
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

// valueChange records a changed local or variable default.
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

// collectSensitiveCandidates enumerates every local and variable in
// either tree whose stored value-expression text differs, including
// presence-only changes (added or removed on one side).
func collectSensitiveCandidates(oldMod, newMod *analysis.Module) []valueChange {
	var out []valueChange
	out = append(out, diffValues("local", localsMap(oldMod), localsMap(newMod))...)
	out = append(out, diffValues("variable", variableDefaultsMap(oldMod), variableDefaultsMap(newMod))...)
	return out
}

func diffValues(kind string, oldV, newV map[string]string) []valueChange {
	var out []valueChange
	for name, oldText := range oldV {
		newText, ok := newV[name]
		if !ok {
			out = append(out, valueChange{kind: kind, name: name, oldText: oldText})
			continue
		}
		if oldText != newText {
			out = append(out, valueChange{kind: kind, name: name, oldText: oldText, newText: newText})
		}
	}
	for name, newText := range newV {
		if _, ok := oldV[name]; !ok {
			out = append(out, valueChange{kind: kind, name: name, newText: newText})
		}
	}
	return out
}

// flagResource checks whether e's count/for_each expression depends on
// any candidate change. If so, it appends (or merges into) out one
// entry per (candidate, resource, meta-arg) pair.
func flagResource(
	modPath string,
	e analysis.Entity,
	mod *analysis.Module,
	candidates []valueChange,
	targetIDs map[string]bool,
	state *tfstate.State,
	out *[]sensitiveChange,
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
			affected := affectedResource{
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

// mergeSensitive groups affected resources under each (module, kind, name)
// triple so the report is compact.
func mergeSensitive(out *[]sensitiveChange, modPath string, cand valueChange, r affectedResource) {
	for i := range *out {
		if (*out)[i].Module == modPath && (*out)[i].Kind == cand.kind && (*out)[i].Name == cand.name {
			(*out)[i].AffectedResources = append((*out)[i].AffectedResources, r)
			return
		}
	}
	*out = append(*out, sensitiveChange{
		Module:            modPath,
		Kind:              cand.kind,
		Name:              cand.name,
		OldValue:          cand.oldText,
		NewValue:          cand.newText,
		AffectedResources: []affectedResource{r},
	})
}

// localsMap returns the printed value text for every local in mod.
// Using the printer normalises whitespace / comments so trivial
// formatting changes don't register as value changes.
func localsMap(m *analysis.Module) map[string]string {
	out := map[string]string{}
	if m == nil {
		return out
	}
	for _, e := range m.Filter(analysis.KindLocal) {
		if e.LocalExpr == nil {
			out[e.Name] = ""
			continue
		}
		out[e.Name] = e.LocalExpr.Text()
	}
	return out
}

// variableDefaultsMap returns the printed default text for every
// variable in mod. A variable without a default maps to an empty
// string; the presence-only case (default added or removed) then shows
// up as a change.
func variableDefaultsMap(m *analysis.Module) map[string]string {
	out := map[string]string{}
	if m == nil {
		return out
	}
	for _, e := range m.Filter(analysis.KindVariable) {
		if e.DefaultExpr == nil {
			out[e.Name] = ""
			continue
		}
		out[e.Name] = e.DefaultExpr.Text()
	}
	return out
}

// refsReachingTargets walks expr collecting every entity reference,
// then checks whether each ref (or any transitive dep of it via mod's
// dep graph) is in targets. Returns the matching target IDs.
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

// traversalToParts mirrors analysis.traversalParts but stays here so we
// don't take a dependency on unexported helpers across packages.
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

// transitivelyDependsOn is a BFS over mod's dep graph.
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

// matchingStateInstances returns the full state addresses of every
// instance of the given resource entity.
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

// detectStateOrphans lists state addresses that have no corresponding
// declaration in the new tree. Pre-existing drift, not changes this PR
// introduces — reported separately so it doesn't inflate the exit code.
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

// ---- text rendering ----

func printStatediff(r *statediffResult) {
	any := false

	if len(r.AddedResources) > 0 || len(r.RemovedResources) > 0 {
		any = true
		fmt.Printf("Resource identity changes vs %s:\n", r.BaseRef)
		for _, a := range r.AddedResources {
			fmt.Printf("  + %s (%s)\n", a.Address(), a.Mode)
		}
		for _, a := range r.RemovedResources {
			fmt.Printf("  - %s (%s)\n", a.Address(), a.Mode)
		}
	}

	if len(r.RenamedResources) > 0 {
		if any {
			fmt.Println()
		}
		any = true
		fmt.Println("Renames (moved block handled — no destroy/recreate):")
		for _, rn := range r.RenamedResources {
			fmt.Printf("  %s → %s\n", rn.FromAddress(), rn.ToAddress())
		}
	}

	if len(r.SensitiveChanges) > 0 {
		if any {
			fmt.Println()
		}
		any = true
		fmt.Println("Value changes that may alter count/for_each expansion:")
		for _, sc := range r.SensitiveChanges {
			prefix := sc.Kind + "." + sc.Name
			if sc.Module != "" {
				prefix = sc.Module + "." + prefix
			}
			fmt.Printf("  - %s\n", prefix)
			fmt.Printf("      old: %s\n", orDash(sc.OldValue))
			fmt.Printf("      new: %s\n", orDash(sc.NewValue))
			for _, ar := range sc.AffectedResources {
				fmt.Printf("    Affected: %s (%s)\n", ar.Address(), ar.MetaArg)
				for _, inst := range ar.StateInstances {
					fmt.Printf("      • state instance: %s\n", inst)
				}
			}
		}
	}

	if len(r.StateOrphans) > 0 {
		if any {
			fmt.Println()
		}
		fmt.Println("State drift — addresses in state but not declared in the new tree:")
		for _, o := range r.StateOrphans {
			fmt.Printf("  ? %s\n", o)
		}
	}

	if !any && len(r.StateOrphans) == 0 {
		fmt.Printf("No resource identity or sensitive-local changes detected vs %s.\n", r.BaseRef)
	}
}

func orDash(s string) string {
	if s == "" {
		return "(absent)"
	}
	return s
}
