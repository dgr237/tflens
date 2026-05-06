package render

import (
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"

	"github.com/dgr237/tflens/pkg/analysis"
)

// ExportReferences is the per-expression index of every entity this
// expression references — pre-computed at export time so converters
// (kro RGD, Crossplane composition, Pulumi component) can do reference
// work without walking the AST themselves.
//
// Each ref's `path` is the post-prefix attribute traversal: for
// `var.cluster.name` the variable is `cluster` and path is `["name"]`;
// for `aws_instance.web.id` the resource is `aws_instance.web` and
// path is `["id"]`. Index is the literal key (`"0"`, `"primary"`)
// when the reference has a `[k]` immediately after the entity prefix.
// Each is true when a `[*]` splat appears anywhere in the post-prefix
// chain (e.g. `aws_subnet.example[*].id` → Each: true, path: ["id"]).
//
// Refs are deduplicated and sorted within each kind, so a converter
// iterating References gets a deterministic ordering.
type ExportReferences struct {
	Variables   []ExportVarRef      `json:"variables,omitempty"`
	Resources   []ExportResourceRef `json:"resources,omitempty"`
	Modules     []ExportModuleRef   `json:"modules,omitempty"`
	DataSources []ExportDataRef     `json:"data_sources,omitempty"`
	Locals      []ExportLocalRef    `json:"locals,omitempty"`
}

// ExportVarRef is one `var.<name>(.path...)` reference.
type ExportVarRef struct {
	Name string   `json:"name"`
	Path []string `json:"path,omitempty"`
}

// ExportResourceRef is one `<type>.<name>([k])(.path...)` reference.
// Index captures the immediate `[k]` after the resource name when
// present, preserving the literal's JSON type — `aws_instance.web[0]`
// produces Index: 0 (number), `aws_instance.web["primary"]` produces
// Index: "primary" (string). Each is true when the post-prefix chain
// includes a splat — Terraform's `[*]` collection expansion.
// Index/Each/Path are mutually informative, not mutually exclusive:
// complex expressions like `aws_subnet.example[*].cidr_block` produce
// {Each: true, Path: ["cidr_block"]}.
type ExportResourceRef struct {
	Type  string   `json:"type"`
	Name  string   `json:"name"`
	Path  []string `json:"path,omitempty"`
	Index any      `json:"index,omitempty"`
	Each  bool     `json:"each,omitempty"`
}

// ExportModuleRef is one `module.<call>(.<output>(.path...))` reference.
// Bare `module.<call>` (no output) emits Output:"" and Path:nil — the
// `for_each = module.foo` case some downstream code relies on.
type ExportModuleRef struct {
	Call   string   `json:"call"`
	Output string   `json:"output,omitempty"`
	Path   []string `json:"path,omitempty"`
}

// ExportDataRef is one `data.<type>.<name>(.path...)` reference.
type ExportDataRef struct {
	Type string   `json:"type"`
	Name string   `json:"name"`
	Path []string `json:"path,omitempty"`
}

// ExportLocalRef is one `local.<name>(.path...)` reference.
type ExportLocalRef struct {
	Name string   `json:"name"`
	Path []string `json:"path,omitempty"`
}

// metaRoots are well-known traversal root names that aren't entity
// references and would otherwise be misclassified as resource refs by
// the default branch in extractReferences. each / count are iteration
// bindings; self appears in postconditions; path / terraform are
// built-in metadata namespaces.
//
// Terraform also lets users name `dynamic` block iterators arbitrarily;
// those would still be misclassified as resource refs when the parent
// renderCtx isn't carrying iterator-name context. The conservative
// fallback is the m.HasEntity check — when a Module is available we
// only emit a resource ref if the type.name pair maps to a real
// resource declaration in this module.
var metaRoots = map[string]bool{
	"each":      true,
	"count":     true,
	"self":      true,
	"path":      true,
	"terraform": true,
}

// extractReferences walks every traversal in expr (skipping bound
// for-expression iteration variables, which hcl.Variables() already
// excludes) and groups the results by entity kind. Returns nil when
// the expression has no qualifying references — so omitempty drops the
// whole field on the wire and consumers can use field presence as a
// "has any references" check.
//
// When m is non-nil, resource refs are filtered against m.HasEntity
// so a misclassified iterator name (e.g. dynamic-block label used as
// `iter.value`) doesn't become a false-positive resource reference.
// When m is nil, every traversal whose root isn't a known meta-root
// is emitted as a resource ref — the conservative interpretation when
// we can't verify entity existence.
func extractReferences(e *analysis.Expr, m *analysis.Module) *ExportReferences {
	if e == nil || e.E == nil {
		return nil
	}
	out := &ExportReferences{}
	seen := map[string]bool{}

	for _, trav := range e.E.Variables() {
		if len(trav) == 0 {
			continue
		}
		root, ok := trav[0].(hcl.TraverseRoot)
		if !ok {
			continue
		}
		switch root.Name {
		case "var":
			addVarRef(out, seen, trav)
		case "local":
			addLocalRef(out, seen, trav)
		case "module":
			addModuleRef(out, seen, trav)
		case "data":
			addDataRef(out, seen, trav)
		default:
			if metaRoots[root.Name] {
				continue
			}
			addResourceRef(out, seen, trav, m)
		}
	}

	if len(out.Variables) == 0 && len(out.Resources) == 0 && len(out.Modules) == 0 && len(out.DataSources) == 0 && len(out.Locals) == 0 {
		return nil
	}
	sortRefs(out)
	return out
}

func addVarRef(out *ExportReferences, seen map[string]bool, trav hcl.Traversal) {
	if len(trav) < 2 {
		return
	}
	name, ok := traverseAttrName(trav[1])
	if !ok {
		return
	}
	path, _, _ := tailToPath(trav[2:])
	key := "var:" + name + ":" + strings.Join(path, ".")
	if seen[key] {
		return
	}
	seen[key] = true
	out.Variables = append(out.Variables, ExportVarRef{Name: name, Path: path})
}

func addLocalRef(out *ExportReferences, seen map[string]bool, trav hcl.Traversal) {
	if len(trav) < 2 {
		return
	}
	name, ok := traverseAttrName(trav[1])
	if !ok {
		return
	}
	path, _, _ := tailToPath(trav[2:])
	key := "local:" + name + ":" + strings.Join(path, ".")
	if seen[key] {
		return
	}
	seen[key] = true
	out.Locals = append(out.Locals, ExportLocalRef{Name: name, Path: path})
}

func addModuleRef(out *ExportReferences, seen map[string]bool, trav hcl.Traversal) {
	if len(trav) < 2 {
		return
	}
	call, ok := traverseAttrName(trav[1])
	if !ok {
		return
	}
	output := ""
	var path []string
	if len(trav) >= 3 {
		if name, ok := traverseAttrName(trav[2]); ok {
			output = name
			path, _, _ = tailToPath(trav[3:])
		}
	}
	key := "module:" + call + ":" + output + ":" + strings.Join(path, ".")
	if seen[key] {
		return
	}
	seen[key] = true
	out.Modules = append(out.Modules, ExportModuleRef{Call: call, Output: output, Path: path})
}

func addDataRef(out *ExportReferences, seen map[string]bool, trav hcl.Traversal) {
	if len(trav) < 3 {
		return
	}
	typ, ok1 := traverseAttrName(trav[1])
	name, ok2 := traverseAttrName(trav[2])
	if !ok1 || !ok2 {
		return
	}
	path, _, _ := tailToPath(trav[3:])
	key := "data:" + typ + ":" + name + ":" + strings.Join(path, ".")
	if seen[key] {
		return
	}
	seen[key] = true
	out.DataSources = append(out.DataSources, ExportDataRef{Type: typ, Name: name, Path: path})
}

func addResourceRef(out *ExportReferences, seen map[string]bool, trav hcl.Traversal, m *analysis.Module) {
	if len(trav) < 2 {
		return
	}
	name, ok := traverseAttrName(trav[1])
	if !ok {
		return
	}
	if m != nil {
		id := (analysis.Entity{Kind: analysis.KindResource, Type: trav[0].(hcl.TraverseRoot).Name, Name: name}).ID()
		if !m.HasEntity(id) {
			return
		}
	}
	path, index, each := tailToPath(trav[2:])
	typ := trav[0].(hcl.TraverseRoot).Name
	eachKey := "0"
	if each {
		eachKey = "1"
	}
	key := "resource:" + typ + ":" + name + ":" + indexKey(index) + ":" + eachKey + ":" + strings.Join(path, ".")
	if seen[key] {
		return
	}
	seen[key] = true
	out.Resources = append(out.Resources, ExportResourceRef{
		Type:  typ,
		Name:  name,
		Path:  path,
		Index: index,
		Each:  each,
	})
}

// indexKey serialises an index value into a deduplication-key string.
// Used by addResourceRef so two refs with structurally-equal indices
// (string "primary" vs number 0) collapse into one entry. The wire
// shape keeps the polymorphic type via Index any — only the dedup
// key flattens to text.
func indexKey(v any) string {
	switch x := v.(type) {
	case string:
		return "s:" + x
	case float64:
		return "n:" + strconv.FormatFloat(x, 'f', -1, 64)
	case int64:
		return "n:" + strconv.FormatInt(x, 10)
	}
	return ""
}

// traverseAttrName returns the attribute name when s is a TraverseAttr;
// the second result reports whether the cast succeeded. Helper for the
// addXxxRef functions which need to walk the prefix steps that look
// like attrs (the entity name after `var`, the call name after
// `module`, etc.).
func traverseAttrName(s hcl.Traverser) (string, bool) {
	a, ok := s.(hcl.TraverseAttr)
	if !ok {
		return "", false
	}
	return a.Name, true
}

// tailToPath flattens the post-prefix steps of a traversal into the
// (path, index, each) triple the wire format uses. The first index
// step (when it appears immediately after the entity prefix, before
// any attr step) is captured as `index`; every TraverseAttr step
// becomes a path entry; any TraverseSplat anywhere flips Each on.
// Subsequent indices after the first are dropped — the wire shape
// only carries the leading instance selector, and consumers needing
// the full structural traversal should fall back to the AST field.
//
// index is returned as `any` so the JSON shape preserves the literal's
// natural type: numeric `[0]` emits as a JSON number, string `["k"]`
// emits as a JSON string. Returns nil when no qualifying index step
// is present, which the omitempty tag drops on the wire.
func tailToPath(steps []hcl.Traverser) (path []string, index any, each bool) {
	first := true
	for _, s := range steps {
		switch t := s.(type) {
		case hcl.TraverseAttr:
			path = append(path, t.Name)
			first = false
		case hcl.TraverseIndex:
			if first && index == nil {
				index = ctyKeyToInterface(t.Key)
			}
			first = false
		case hcl.TraverseSplat:
			each = true
			first = false
		}
	}
	return
}

// ctyKeyToInterface returns an index key value in its natural JSON
// form: float64 for numbers (so `[0]` becomes `0`), string for string
// keys (so `["primary"]` becomes `"primary"`). Returns nil for
// unknown / dynamic / null keys so the omitempty tag drops the index
// field — the AST stays the source of truth for those edge cases.
//
// Numbers emit as float64 specifically because that's encoding/json's
// default decode type for JSON numbers; round-tripping through cty
// → any → JSON → any → consumer code keeps the type stable.
func ctyKeyToInterface(v cty.Value) any {
	if v == cty.NilVal || v.IsNull() || !v.IsKnown() {
		return nil
	}
	t := v.Type()
	switch {
	case t == cty.String:
		return v.AsString()
	case t == cty.Number:
		f, _ := v.AsBigFloat().Float64()
		return f
	}
	return nil
}

// transitiveLocalRefs walks the dependency graph rooted at the given
// local entity, collecting every var / resource / data source / module
// / local reachable through chains of `local.X → local.Y → ...`.
// Returns the merged ExportReferences plus a bool that's true when the
// walk hit a real cycle.
//
// Cycle handling is classical DFS-coloured: inStack tracks names
// currently mid-recursion (push on entry, pop on return) — re-entering
// one is a back-edge and flips the cycle flag. fullyResolved marks
// names whose entire subtree has already been merged into out, so a
// diamond like `a → combined → shared` plus `a → shared` short-circuits
// the second visit instead of false-positiving as a cycle. Crucially,
// "ref to the same target local appearing twice in one expression"
// (e.g. `local.X.foo + local.X.bar`) is also a non-cycle: the second
// ref is filtered by fullyResolved after the first ref's recursion
// completes.
//
// Used by exportLocal to populate the DependsOn / Cycle fields on
// each ExportLocal so consumers (composegen's resolveLocal walker)
// don't have to recurse through sub-locals themselves.
func transitiveLocalRefs(root analysis.Entity, m *analysis.Module) (*ExportReferences, bool) {
	if root.Kind != analysis.KindLocal || root.LocalExpr == nil || root.LocalExpr.E == nil {
		return nil, false
	}
	out := &ExportReferences{}
	seen := map[string]bool{}
	inStack := map[string]bool{}
	fullyResolved := map[string]bool{}
	cycle := false
	visitLocal(root, m, out, seen, inStack, fullyResolved, &cycle)
	if len(out.Variables) == 0 && len(out.Resources) == 0 && len(out.Modules) == 0 && len(out.DataSources) == 0 && len(out.Locals) == 0 {
		return nil, cycle
	}
	sortRefs(out)
	return out, cycle
}

// visitLocal extracts direct refs from one local's expression and
// recurses into any local refs found, accumulating into the shared
// out / seen maps and threading the inStack / fullyResolved DFS state
// for cycle detection. The cycle bool is shared across the whole walk
// via pointer.
//
// Two short-circuit conditions on each child ref:
//
//   - inStack[child] → true cycle (back-edge into the active path);
//     flip *cycle and skip recursion.
//   - fullyResolved[child] → already-walked subtree; skip recursion
//     because every transitive ref is already in out via seen. NOT a
//     cycle — this is the diamond / sibling-multi-ref case.
//
// On entry: push e.Name onto inStack. On return (defer): pop from
// inStack and add to fullyResolved. The defer ordering matters —
// inStack must be popped before fullyResolved is set, so the next
// sibling sees the child as resolved rather than still-active.
func visitLocal(e analysis.Entity, m *analysis.Module, out *ExportReferences, seen map[string]bool, inStack map[string]bool, fullyResolved map[string]bool, cycle *bool) {
	if e.LocalExpr == nil || e.LocalExpr.E == nil {
		return
	}
	if fullyResolved[e.Name] {
		return
	}
	inStack[e.Name] = true
	defer func() {
		delete(inStack, e.Name)
		fullyResolved[e.Name] = true
	}()

	direct := extractReferences(e.LocalExpr, m)
	if direct == nil {
		return
	}
	for _, ref := range direct.Variables {
		key := "var:" + ref.Name + ":" + strings.Join(ref.Path, ".")
		if !seen[key] {
			seen[key] = true
			out.Variables = append(out.Variables, ref)
		}
	}
	for _, ref := range direct.Resources {
		key := "resource:" + ref.Type + ":" + ref.Name + ":" + indexKey(ref.Index) + ":" + strings.Join(ref.Path, ".")
		if !seen[key] {
			seen[key] = true
			out.Resources = append(out.Resources, ref)
		}
	}
	for _, ref := range direct.Modules {
		key := "module:" + ref.Call + ":" + ref.Output + ":" + strings.Join(ref.Path, ".")
		if !seen[key] {
			seen[key] = true
			out.Modules = append(out.Modules, ref)
		}
	}
	for _, ref := range direct.DataSources {
		key := "data:" + ref.Type + ":" + ref.Name + ":" + strings.Join(ref.Path, ".")
		if !seen[key] {
			seen[key] = true
			out.DataSources = append(out.DataSources, ref)
		}
	}
	for _, ref := range direct.Locals {
		key := "local:" + ref.Name + ":" + strings.Join(ref.Path, ".")
		if !seen[key] {
			seen[key] = true
			out.Locals = append(out.Locals, ref)
		}
		if inStack[ref.Name] {
			*cycle = true
			continue
		}
		if fullyResolved[ref.Name] {
			continue
		}
		if child, ok := m.EntityByID((analysis.Entity{Kind: analysis.KindLocal, Name: ref.Name}).ID()); ok {
			visitLocal(child, m, out, seen, inStack, fullyResolved, cycle)
		}
	}
}

func sortRefs(r *ExportReferences) {
	sort.Slice(r.Variables, func(i, j int) bool {
		if r.Variables[i].Name != r.Variables[j].Name {
			return r.Variables[i].Name < r.Variables[j].Name
		}
		return strings.Join(r.Variables[i].Path, ".") < strings.Join(r.Variables[j].Path, ".")
	})
	sort.Slice(r.Locals, func(i, j int) bool {
		if r.Locals[i].Name != r.Locals[j].Name {
			return r.Locals[i].Name < r.Locals[j].Name
		}
		return strings.Join(r.Locals[i].Path, ".") < strings.Join(r.Locals[j].Path, ".")
	})
	sort.Slice(r.Modules, func(i, j int) bool {
		a, b := r.Modules[i], r.Modules[j]
		if a.Call != b.Call {
			return a.Call < b.Call
		}
		if a.Output != b.Output {
			return a.Output < b.Output
		}
		return strings.Join(a.Path, ".") < strings.Join(b.Path, ".")
	})
	sort.Slice(r.DataSources, func(i, j int) bool {
		a, b := r.DataSources[i], r.DataSources[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return strings.Join(a.Path, ".") < strings.Join(b.Path, ".")
	})
	sort.Slice(r.Resources, func(i, j int) bool {
		a, b := r.Resources[i], r.Resources[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		ai, bi := indexKey(a.Index), indexKey(b.Index)
		if ai != bi {
			return ai < bi
		}
		return strings.Join(a.Path, ".") < strings.Join(b.Path, ".")
	})
}
