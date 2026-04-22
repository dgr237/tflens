package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/ast"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/printer"
	"github.com/dgr237/tflens/pkg/tfstate"
)

var statediffCmd = &cobra.Command{
	Use:   "statediff --branch <base> [workspace]",
	Short: "Identify resources a PR may create, destroy, or re-instance",
	Long: `statediff compares two branches at the resource identity level and
surfaces changes that may alter Terraform state when the PR is merged:

  1. Resource declarations added or removed between branches.
  2. Locals whose value expression changed AND whose dependency chain
     reaches a count or for_each meta-argument — the common way a
     seemingly-small edit silently destroys instances.
  3. When --state <file> is given: for every flagged resource, the
     instances currently in state (so a reviewer can see the concrete
     addresses that may be affected).

Exits non-zero when anything is flagged, for CI gating.

What it does NOT do: attribute-level plan simulation. That needs
provider schemas and expression evaluation — run 'terraform plan' for
that. statediff is a static hazard detector, not a plan replacement.`,
	Args: cobra.RangeArgs(0, 1),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, _ := cmd.Flags().GetString("branch")
		if base == "" {
			return fmt.Errorf("statediff requires --branch <base>")
		}
		ws := "."
		if len(args) == 1 {
			ws = args[0]
		}
		if base == BranchAutoKeyword {
			auto, err := resolveAutoBase(ws)
			if err != nil {
				return err
			}
			base = auto
		}
		statePath, _ := cmd.Flags().GetString("state")
		return runStatediff(cmd, ws, base, statePath)
	},
}

func init() {
	statediffCmd.Flags().String("branch", "", "base git ref to compare against; pass 'auto' to detect")
	statediffCmd.Flags().String("state", "", "optional Terraform state v4 JSON file for instance cross-reference")
	rootCmd.AddCommand(statediffCmd)
}

func runStatediff(cmd *cobra.Command, workspace, baseRef, statePath string) error {
	newProj, err := loadProject(cmd, workspace)
	if err != nil {
		return fmt.Errorf("loading workspace: %w", err)
	}
	oldProj, cleanup, err := loadOldProjectForBranch(cmd, workspace, baseRef)
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
	result.Workspace = workspace

	if outputJSON(cmd) {
		exitJSON(result, exitCodeFor(result.flaggedCount()))
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
	BaseRef           string                `json:"base_ref"`
	Workspace         string                `json:"workspace"`
	AddedResources    []resourceRef         `json:"added_resources"`
	RemovedResources  []resourceRef         `json:"removed_resources"`
	SensitiveLocals   []sensitiveLocal      `json:"sensitive_locals"`
	StateOrphans      []string              `json:"state_orphans,omitempty"`
}

func (r statediffResult) flaggedCount() int {
	n := len(r.AddedResources) + len(r.RemovedResources) + len(r.SensitiveLocals)
	// State orphans are noted but not counted as "flagged" — they
	// indicate pre-existing drift, not changes introduced by this PR.
	return n
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

type sensitiveLocal struct {
	Module             string            `json:"module"`
	Name               string            `json:"name"`
	OldValue           string            `json:"old_value"`
	NewValue           string            `json:"new_value"`
	AffectedResources  []affectedResource `json:"affected_resources"`
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
	result.AddedResources, result.RemovedResources = diffResources(oldMods, newMods)
	result.SensitiveLocals = detectSensitiveLocals(oldMods, newMods, state)
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

func diffResources(oldMods, newMods map[string]*loader.ModuleNode) (added, removed []resourceRef) {
	oldSet := collectResources(oldMods)
	newSet := collectResources(newMods)
	for k, r := range newSet {
		if _, ok := oldSet[k]; !ok {
			added = append(added, r)
		}
	}
	for k, r := range oldSet {
		if _, ok := newSet[k]; !ok {
			removed = append(removed, r)
		}
	}
	sortResourceRefs(added)
	sortResourceRefs(removed)
	return added, removed
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
			out[modPath+"|"+mode+"|"+e.Type+"|"+e.Name] = r
		}
	}
	return out
}

func sortResourceRefs(rs []resourceRef) {
	sort.Slice(rs, func(i, j int) bool { return rs[i].Address() < rs[j].Address() })
}

// ---- sensitive-locals detection ----

func detectSensitiveLocals(oldMods, newMods map[string]*loader.ModuleNode, state *tfstate.State) []sensitiveLocal {
	var out []sensitiveLocal
	// A "module" here is the whole sub-module at a given path. Locals
	// live inside their own module, so we compare per-module.
	for modPath, newNode := range newMods {
		oldNode, ok := oldMods[modPath]
		if !ok || oldNode == nil || newNode == nil {
			continue // module added/removed is already reflected by resource diff
		}
		changed := changedLocalsIn(oldNode.Module, newNode.Module)
		if len(changed) == 0 {
			continue
		}
		changedSet := map[string]bool{}
		for name := range changed {
			changedSet["local."+name] = true
		}
		for _, e := range newNode.Module.Filter(analysis.KindResource) {
			flagResource(modPath, e, newNode.Module, changed, changedSet, state, &out)
		}
		for _, e := range newNode.Module.Filter(analysis.KindData) {
			flagResource(modPath, e, newNode.Module, changed, changedSet, state, &out)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Module != out[j].Module {
			return out[i].Module < out[j].Module
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// flagResource checks whether e's count/for_each expression depends on
// any local in changedSet. If so, it appends (or merges into) out one
// entry per affected (local, resource) pair.
func flagResource(
	modPath string,
	e analysis.Entity,
	mod *analysis.Module,
	changed map[string]localChange,
	changedSet map[string]bool,
	state *tfstate.State,
	out *[]sensitiveLocal,
) {
	for _, metaArg := range []struct {
		name string
		expr ast.Expr
	}{
		{"count", e.CountExpr},
		{"for_each", e.ForEachExpr},
	} {
		if metaArg.expr == nil {
			continue
		}
		triggered := refsReachingTargets(mod, metaArg.expr, changedSet)
		for localName := range triggered {
			// The triggered set contains entity IDs; we want the bare
			// local name.
			name := strings.TrimPrefix(localName, "local.")
			if !changed[name].valid() {
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
			mergeSensitive(out, modPath, name, changed[name], affected)
		}
	}
}

// mergeSensitive groups affected resources under each (module, local)
// pair. Keeps output compact.
func mergeSensitive(out *[]sensitiveLocal, modPath, localName string, change localChange, r affectedResource) {
	for i := range *out {
		if (*out)[i].Module == modPath && (*out)[i].Name == localName {
			(*out)[i].AffectedResources = append((*out)[i].AffectedResources, r)
			return
		}
	}
	*out = append(*out, sensitiveLocal{
		Module:            modPath,
		Name:              localName,
		OldValue:          change.oldText,
		NewValue:          change.newText,
		AffectedResources: []affectedResource{r},
	})
}

type localChange struct {
	oldText, newText string
}

func (c localChange) valid() bool { return c.oldText != "" || c.newText != "" }

// changedLocalsIn returns every local whose value expression differs
// between oldMod and newMod, keyed by local name. A local present on
// only one side counts as changed (old or new text will be empty).
func changedLocalsIn(oldMod, newMod *analysis.Module) map[string]localChange {
	oldLocals := localsMap(oldMod)
	newLocals := localsMap(newMod)
	out := map[string]localChange{}
	for name, oldText := range oldLocals {
		newText, ok := newLocals[name]
		if !ok {
			out[name] = localChange{oldText: oldText}
			continue
		}
		if oldText != newText {
			out[name] = localChange{oldText: oldText, newText: newText}
		}
	}
	for name, newText := range newLocals {
		if _, ok := oldLocals[name]; !ok {
			out[name] = localChange{newText: newText}
		}
	}
	return out
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
		out[e.Name] = printer.PrintExpr(e.LocalExpr)
	}
	return out
}

// refsReachingTargets walks expr collecting every entity reference,
// then checks whether each ref (or any transitive dep of it via mod's
// dep graph) is in targets. Returns the matching target IDs.
func refsReachingTargets(mod *analysis.Module, expr ast.Expr, targets map[string]bool) map[string]bool {
	hits := map[string]bool{}
	if expr == nil {
		return hits
	}
	ast.Inspect(expr, func(n ast.Node) bool {
		ref, ok := n.(*ast.RefExpr)
		if !ok {
			return true
		}
		id := refToEntityID(ref.Parts)
		if id == "" {
			return true
		}
		if targets[id] {
			hits[id] = true
			return true
		}
		for t := range targets {
			if transitivelyDependsOn(mod, id, t) {
				hits[t] = true
			}
		}
		return true
	})
	return hits
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

	if len(r.SensitiveLocals) > 0 {
		if any {
			fmt.Println()
		}
		any = true
		fmt.Printf("Locals whose value change may alter count/for_each expansion:\n")
		for _, sl := range r.SensitiveLocals {
			prefix := "local." + sl.Name
			if sl.Module != "" {
				prefix = sl.Module + "." + prefix
			}
			fmt.Printf("  - %s\n", prefix)
			fmt.Printf("      old: %s\n", orDash(sl.OldValue))
			fmt.Printf("      new: %s\n", orDash(sl.NewValue))
			for _, ar := range sl.AffectedResources {
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
