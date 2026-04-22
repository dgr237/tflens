// Package analysis builds an entity inventory and dependency graph from a
// parsed Terraform file. It operates purely on the AST with no network or
// filesystem access.
package analysis

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"github.com/dgr237/tflens/pkg/ast"
	"github.com/dgr237/tflens/pkg/token"
)

// EntityKind classifies a top-level Terraform entity.
type EntityKind string

const (
	KindResource EntityKind = "resource"
	KindData     EntityKind = "data"
	KindVariable EntityKind = "variable"
	KindLocal    EntityKind = "local"
	KindOutput   EntityKind = "output"
	KindModule   EntityKind = "module"
)

// Entity is a single named thing declared in a Terraform module.
type Entity struct {
	Kind           EntityKind
	Type           string // non-empty for resource and data source
	Name           string
	Pos            token.Position      // source location of the declaration
	DeclaredType   *TFType             // parsed type constraint; non-nil only for variables
	HasDefault     bool                // variables: a default value was declared
	DefaultExpr    ast.Expr            // variables: the default value expression (for indirect diffing)
	HasCount       bool                // resource/data/module: count meta-argument used
	HasForEach     bool                // resource/data/module: for_each meta-argument used
	NonNullable    bool                // variables: `nullable = false` explicitly set
	Sensitive      bool                // variables/outputs: `sensitive = true` set
	Validations    int                 // variables: number of `validation {}` blocks
	Preconditions  int                 // variables/outputs/resources: number of precondition blocks
	Postconditions int                 // variables/outputs/resources: number of postcondition blocks
	ValueExpr      ast.Expr            // outputs: the value expression (for shape-diffing)
	ProviderExpr   ast.Expr            // resource/data: value of `provider` attribute
	ModuleArgs     map[string]ast.Expr // module blocks: argument-name → expression (excludes meta-args)
	LocalExpr      ast.Expr            // locals: the local's value expression (for indirect diffing)
	ForEachExpr    ast.Expr            // resource/data/module: value of `for_each`
	CountExpr      ast.Expr            // resource/data/module: value of `count`
	DependsOnExpr  ast.Expr            // resource/data/module/output: value of `depends_on`

	// Lifecycle block (resources only)
	PreventDestroy         bool     // `prevent_destroy = true`
	CreateBeforeDestroy    bool     // `create_before_destroy = true`
	IgnoreChangesExpr      ast.Expr // value of `ignore_changes`
	ReplaceTriggeredByExpr ast.Expr // value of `replace_triggered_by`
}

// Location returns a short "file:line" string suitable for display.
// Returns an empty string when no position information is available.
func (e Entity) Location() string {
	if e.Pos.File == "" && e.Pos.Line == 0 {
		return ""
	}
	if e.Pos.File == "" {
		return fmt.Sprintf("%d", e.Pos.Line)
	}
	return fmt.Sprintf("%s:%d", filepath.Base(e.Pos.File), e.Pos.Line)
}

// ID returns the canonical string identifier used as graph node key.
func (e Entity) ID() string {
	switch e.Kind {
	case KindResource, KindData:
		return fmt.Sprintf("%s.%s.%s", e.Kind, e.Type, e.Name)
	default:
		return fmt.Sprintf("%s.%s", e.Kind, e.Name)
	}
}

func (e Entity) String() string { return e.ID() }

// ValidationError records a reference in entity EntityID that points to an
// undeclared Terraform entity, or another validation failure.
type ValidationError struct {
	EntityID string         // entity that contains the reference
	Ref      string         // canonical ID of the missing entity, e.g. "variable.typo"
	Pos      token.Position // source location of the reference
	Msg      string         // if non-empty, replaces the default format in Error()
}

func (e ValidationError) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("%s: %s", e.Pos, e.Msg)
	}
	return fmt.Sprintf("%s: %s is undefined (referenced by %s)", e.Pos, e.Ref, e.EntityID)
}

// builtinPrefixes are the first identifier parts of built-in Terraform
// references that are not user-declared entities.
var builtinPrefixes = map[string]bool{
	"count":     true,
	"each":      true,
	"path":      true,
	"self":      true,
	"terraform": true,
}

// ProviderRequirement describes one entry inside required_providers {}.
type ProviderRequirement struct {
	Source  string
	Version string
}

// Module is the analysis result for one Terraform module (one directory).
// A module may be built from one or more source files.
type Module struct {
	entities          []Entity
	byID              map[string]Entity
	deps              map[string]map[string]bool // from-ID → set of to-IDs
	moduleSources     map[string]string          // module call name → source attribute value
	moduleVersions    map[string]string          // module call name → version constraint
	valErrs           []ValidationError
	typeErrs          []TypeCheckError
	moved             map[string]string // from-ID → to-ID (from `moved` blocks)
	removedIDs        map[string]bool   // IDs declared in `removed` blocks
	requiredVersion   string            // from terraform { required_version = ... }
	requiredProviders map[string]ProviderRequirement
}

func newModule() *Module {
	return &Module{
		byID:              make(map[string]Entity),
		deps:              make(map[string]map[string]bool),
		moduleSources:     make(map[string]string),
		moduleVersions:    make(map[string]string),
		moved:             make(map[string]string),
		removedIDs:        make(map[string]bool),
		requiredProviders: make(map[string]ProviderRequirement),
	}
}

// RequiredVersion returns the Terraform CLI version constraint declared by
// a terraform { required_version = ... } block, or an empty string if none.
func (m *Module) RequiredVersion() string {
	return m.requiredVersion
}

// RequiredProviders returns a copy of the provider requirements declared in
// terraform { required_providers { ... } }.
func (m *Module) RequiredProviders() map[string]ProviderRequirement {
	out := make(map[string]ProviderRequirement, len(m.requiredProviders))
	for k, v := range m.requiredProviders {
		out[k] = v
	}
	return out
}

// Moved returns the rename pairs declared by `moved` blocks in this module,
// as a map from old entity ID to new entity ID.
func (m *Module) Moved() map[string]string {
	out := make(map[string]string, len(m.moved))
	for k, v := range m.moved {
		out[k] = v
	}
	return out
}

// RemovedDeclared reports whether id was declared by a `removed` block.
func (m *Module) RemovedDeclared(id string) bool {
	return m.removedIDs[id]
}

// Validate returns all undefined-reference errors found during analysis,
// sorted by source location.
func (m *Module) Validate() []ValidationError {
	out := make([]ValidationError, len(m.valErrs))
	copy(out, m.valErrs)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Pos, out[j].Pos
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})
	return out
}

// ModuleSource returns the value of the source attribute for a module call,
// or an empty string if the module was not found or has no source attribute.
func (m *Module) ModuleSource(name string) string {
	return m.moduleSources[name]
}

// ModuleVersion returns the version constraint for a module call, or an
// empty string if not pinned.
func (m *Module) ModuleVersion(name string) string {
	return m.moduleVersions[name]
}

func (m *Module) addEntity(e Entity) {
	if _, exists := m.byID[e.ID()]; !exists {
		m.entities = append(m.entities, e)
		m.byID[e.ID()] = e
	}
}

func (m *Module) addDep(from, to string) {
	if from == to {
		return
	}
	if _, ok := m.deps[from]; !ok {
		m.deps[from] = make(map[string]bool)
	}
	m.deps[from][to] = true
}

// Entities returns all declared entities in declaration order.
func (m *Module) Entities() []Entity { return m.entities }

// Filter returns all entities of the given kind, in declaration order.
func (m *Module) Filter(kind EntityKind) []Entity {
	var out []Entity
	for _, e := range m.entities {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// Dependencies returns the sorted list of entity IDs that id directly depends on.
func (m *Module) Dependencies(id string) []string {
	set := m.deps[id]
	out := make([]string, 0, len(set))
	for dep := range set {
		out = append(out, dep)
	}
	sort.Strings(out)
	return out
}

// Dependents returns the sorted list of entity IDs that directly depend on id.
func (m *Module) Dependents(id string) []string {
	var out []string
	for from, toSet := range m.deps {
		if toSet[id] {
			out = append(out, from)
		}
	}
	sort.Strings(out)
	return out
}

// HasDep reports whether from directly depends on to.
func (m *Module) HasDep(from, to string) bool {
	return m.deps[from][to]
}

// ToDOT returns the dependency graph in Graphviz DOT format.
func (m *Module) ToDOT() string {
	var sb strings.Builder
	sb.WriteString("digraph terraform {\n")
	sb.WriteString("  rankdir=LR;\n")
	sb.WriteString("  node [shape=box fontname=monospace];\n\n")

	// One node per entity, grouped by kind.
	kindColour := map[EntityKind]string{
		KindResource: "#aed6f1",
		KindData:     "#a9dfbf",
		KindVariable: "#f9e79f",
		KindLocal:    "#f5cba7",
		KindOutput:   "#d7bde2",
		KindModule:   "#fadbd8",
	}
	for _, e := range m.entities {
		colour := kindColour[e.Kind]
		sb.WriteString(fmt.Sprintf("  %q [label=%q style=filled fillcolor=%q];\n",
			e.ID(), e.ID(), colour))
	}
	sb.WriteByte('\n')

	// Edges.
	froms := make([]string, 0, len(m.deps))
	for from := range m.deps {
		froms = append(froms, from)
	}
	sort.Strings(froms)
	for _, from := range froms {
		tos := m.Dependencies(from)
		for _, to := range tos {
			sb.WriteString(fmt.Sprintf("  %q -> %q;\n", from, to))
		}
	}
	sb.WriteString("}\n")
	return sb.String()
}

// ---- analysis passes ----

// Analyse collects all entities and dependency edges from a single parsed file.
func Analyse(file *ast.File) *Module {
	return AnalyseFiles([]*ast.File{file})
}

// AnalyseFiles merges multiple parsed files into a single module analysis.
// All entity declarations are collected before any dependency edges are
// resolved, so cross-file references work correctly.
func AnalyseFiles(files []*ast.File) *Module {
	m := newModule()
	for _, f := range files {
		collectEntities(m, f.Body)
	}
	for _, f := range files {
		collectDependencies(m, f.Body)
	}
	for _, f := range files {
		typeCheckBodies(m, f.Body)
	}
	checkSensitivePropagation(m)
	return m
}

// checkSensitivePropagation emits a ValidationError for every output whose
// value expression references a sensitive variable without being marked
// sensitive itself. This mirrors the error Terraform itself produces at plan
// time — catching it early is useful.
func checkSensitivePropagation(m *Module) {
	for _, e := range m.entities {
		if e.Kind != KindOutput || e.Sensitive || e.ValueExpr == nil {
			continue
		}
		// Deduplicate within one output (same var referenced multiple times).
		seen := make(map[string]bool)
		ast.Inspect(e.ValueExpr, func(n ast.Node) bool {
			ref, ok := n.(*ast.RefExpr)
			if !ok {
				return true
			}
			if len(ref.Parts) < 2 || ref.Parts[0] != "var" {
				return true
			}
			varID := (Entity{Kind: KindVariable, Name: ref.Parts[1]}).ID()
			if seen[varID] {
				return true
			}
			v, ok := m.byID[varID]
			if !ok || !v.Sensitive {
				return true
			}
			seen[varID] = true
			m.valErrs = append(m.valErrs, ValidationError{
				EntityID: e.ID(),
				Ref:      varID,
				Pos:      ast.NodePos(ref),
				Msg: fmt.Sprintf("%s references sensitive %s but is not itself marked sensitive (Terraform will reject the plan)",
					e.ID(), varID),
			})
			return true
		})
	}
}

// collectEntities performs a first pass to register every declared entity.
func collectEntities(m *Module, body *ast.Body) {
	for _, node := range body.Nodes {
		block, ok := node.(*ast.Block)
		if !ok {
			continue
		}
		switch block.Type {
		case "resource":
			if len(block.Labels) == 2 {
				e := Entity{Kind: KindResource, Type: block.Labels[0], Name: block.Labels[1], Pos: block.Pos}
				scanMetaArgs(&e, block.Body)
				m.addEntity(e)
			}
		case "data":
			if len(block.Labels) == 2 {
				e := Entity{Kind: KindData, Type: block.Labels[0], Name: block.Labels[1], Pos: block.Pos}
				scanMetaArgs(&e, block.Body)
				m.addEntity(e)
			}
		case "variable":
			if len(block.Labels) == 1 {
				e := Entity{Kind: KindVariable, Name: block.Labels[0], Pos: block.Pos}
				var defaultExpr ast.Expr
				for _, n := range block.Body.Nodes {
					switch child := n.(type) {
					case *ast.Attribute:
						switch child.Name {
						case "type":
							e.DeclaredType = ParseType(child.Value)
						case "default":
							defaultExpr = child.Value
							e.HasDefault = true
							e.DefaultExpr = child.Value
						case "nullable":
							if lit, ok := child.Value.(*ast.LiteralExpr); ok {
								if b, ok := lit.Value.(bool); ok && !b {
									e.NonNullable = true
								}
							}
						case "sensitive":
							if lit, ok := child.Value.(*ast.LiteralExpr); ok {
								if b, ok := lit.Value.(bool); ok && b {
									e.Sensitive = true
								}
							}
						}
					case *ast.Block:
						switch child.Type {
						case "validation":
							e.Validations++
						case "precondition":
							e.Preconditions++
						case "postcondition":
							e.Postconditions++
						}
					}
				}
				m.addEntity(e)
				if e.DeclaredType != nil && defaultExpr != nil {
					inferred := InferLiteralType(defaultExpr)
					if !isCompatible(e.DeclaredType, inferred) {
						m.typeErrs = append(m.typeErrs, TypeCheckError{
							EntityID: e.ID(),
							Attr:     "default",
							Pos:      ast.NodePos(defaultExpr),
							Msg: fmt.Sprintf("default value for %s has type %s, want %s",
								e.ID(), inferred, e.DeclaredType),
						})
					}
				}
			}
		case "output":
			if len(block.Labels) == 1 {
				e := Entity{Kind: KindOutput, Name: block.Labels[0], Pos: block.Pos}
				for _, n := range block.Body.Nodes {
					switch child := n.(type) {
					case *ast.Attribute:
						switch child.Name {
						case "sensitive":
							if lit, ok := child.Value.(*ast.LiteralExpr); ok {
								if b, ok := lit.Value.(bool); ok && b {
									e.Sensitive = true
								}
							}
						case "value":
							e.ValueExpr = child.Value
						case "depends_on":
							e.DependsOnExpr = child.Value
						}
					case *ast.Block:
						switch child.Type {
						case "precondition":
							e.Preconditions++
						case "postcondition":
							e.Postconditions++
						}
					}
				}
				m.addEntity(e)
			}
		case "module":
			if len(block.Labels) == 1 {
				name := block.Labels[0]
				e := Entity{Kind: KindModule, Name: name, Pos: block.Pos, ModuleArgs: map[string]ast.Expr{}}
				scanMetaArgs(&e, block.Body)
				// Capture module args (exclude meta-args and reserved names).
				for _, n := range block.Body.Nodes {
					attr, ok := n.(*ast.Attribute)
					if !ok {
						continue
					}
					if isModuleMetaArg(attr.Name) {
						// Still capture source/version separately for module-source diffs.
						if lit, ok := attr.Value.(*ast.LiteralExpr); ok {
							if s, ok := lit.Value.(string); ok {
								switch attr.Name {
								case "source":
									m.moduleSources[name] = s
								case "version":
									m.moduleVersions[name] = s
								}
							}
						}
						continue
					}
					e.ModuleArgs[attr.Name] = attr.Value
				}
				m.addEntity(e)
			}
		case "locals":
			for _, n := range block.Body.Nodes {
				if attr, ok := n.(*ast.Attribute); ok {
					m.addEntity(Entity{Kind: KindLocal, Name: attr.Name, Pos: attr.Pos, LocalExpr: attr.Value})
				}
			}
		case "moved":
			from, to := extractFromTo(block.Body)
			if from != "" && to != "" {
				m.moved[from] = to
			}
		case "removed":
			from, _ := extractFromTo(block.Body)
			if from != "" {
				m.removedIDs[from] = true
			}
		case "terraform":
			scanTerraformBlock(m, block.Body)
		}
	}
}

// scanTerraformBlock reads the attributes and sub-blocks of a top-level
// terraform {} block into the Module's requiredVersion and requiredProviders
// fields. The older form `required_providers = { aws = "~> 4.0" }` is not
// supported; modern block form is.
func scanTerraformBlock(m *Module, body *ast.Body) {
	for _, n := range body.Nodes {
		switch child := n.(type) {
		case *ast.Attribute:
			if child.Name == "required_version" {
				if lit, ok := child.Value.(*ast.LiteralExpr); ok {
					if s, ok := lit.Value.(string); ok {
						m.requiredVersion = s
					}
				}
			}
		case *ast.Block:
			if child.Type != "required_providers" {
				continue
			}
			for _, pn := range child.Body.Nodes {
				attr, ok := pn.(*ast.Attribute)
				if !ok {
					continue
				}
				obj, ok := attr.Value.(*ast.ObjectExpr)
				if !ok {
					continue
				}
				var req ProviderRequirement
				for _, item := range obj.Items {
					key := objectKeyName(item.Key)
					lit, ok := item.Value.(*ast.LiteralExpr)
					if !ok {
						continue
					}
					s, ok := lit.Value.(string)
					if !ok {
						continue
					}
					switch key {
					case "source":
						req.Source = s
					case "version":
						req.Version = s
					}
				}
				m.requiredProviders[attr.Name] = req
			}
		}
	}
}

// extractFromTo reads the `from` and `to` attributes of a moved/removed block
// body. Each is an expression like `aws_vpc.main` or `module.net`; both are
// converted to canonical entity IDs. Either return value may be empty.
func extractFromTo(body *ast.Body) (from, to string) {
	for _, n := range body.Nodes {
		attr, ok := n.(*ast.Attribute)
		if !ok {
			continue
		}
		ref, ok := attr.Value.(*ast.RefExpr)
		if !ok {
			continue
		}
		switch attr.Name {
		case "from":
			from = refPartsToEntityID(ref.Parts)
		case "to":
			to = refPartsToEntityID(ref.Parts)
		}
	}
	return from, to
}

// refPartsToEntityID converts a reference's parts (e.g. ["aws_vpc","main"])
// to a canonical entity ID (e.g. "resource.aws_vpc.main"). Returns an empty
// string if the reference shape is not recognised.
func refPartsToEntityID(parts []string) string {
	if len(parts) < 2 {
		return ""
	}
	switch parts[0] {
	case "module":
		return (Entity{Kind: KindModule, Name: parts[1]}).ID()
	case "data":
		if len(parts) < 3 {
			return ""
		}
		return (Entity{Kind: KindData, Type: parts[1], Name: parts[2]}).ID()
	}
	// Default: resource style, type.name
	return (Entity{Kind: KindResource, Type: parts[0], Name: parts[1]}).ID()
}

// isModuleMetaArg reports whether a named attribute is a Terraform-reserved
// meta-argument on a module block rather than an argument to pass to the child
// module.
func isModuleMetaArg(name string) bool {
	switch name {
	case "source", "version", "count", "for_each", "providers", "depends_on", "provider":
		return true
	}
	return false
}

// scanMetaArgs sets meta-argument and lifecycle fields on e by inspecting the
// direct children of a resource/data/module block body.
func scanMetaArgs(e *Entity, body *ast.Body) {
	for _, n := range body.Nodes {
		switch child := n.(type) {
		case *ast.Attribute:
			switch child.Name {
			case "count":
				e.HasCount = true
				e.CountExpr = child.Value
			case "for_each":
				e.HasForEach = true
				e.ForEachExpr = child.Value
			case "provider":
				e.ProviderExpr = child.Value
			case "depends_on":
				e.DependsOnExpr = child.Value
			}
		case *ast.Block:
			if child.Type == "lifecycle" {
				scanLifecycleBlock(e, child.Body)
			}
		}
	}
}

// scanLifecycleBlock populates the lifecycle fields of e from the body of a
// `lifecycle {}` block.
func scanLifecycleBlock(e *Entity, body *ast.Body) {
	for _, n := range body.Nodes {
		switch child := n.(type) {
		case *ast.Attribute:
			switch child.Name {
			case "prevent_destroy":
				if lit, ok := child.Value.(*ast.LiteralExpr); ok {
					if b, ok := lit.Value.(bool); ok {
						e.PreventDestroy = b
					}
				}
			case "create_before_destroy":
				if lit, ok := child.Value.(*ast.LiteralExpr); ok {
					if b, ok := lit.Value.(bool); ok {
						e.CreateBeforeDestroy = b
					}
				}
			case "ignore_changes":
				e.IgnoreChangesExpr = child.Value
			case "replace_triggered_by":
				e.ReplaceTriggeredByExpr = child.Value
			}
		case *ast.Block:
			switch child.Type {
			case "precondition":
				e.Preconditions++
			case "postcondition":
				e.Postconditions++
			}
		}
	}
}

// collectDependencies performs a second pass to build dependency edges.
// It must run after collectEntities so that resource references can be
// distinguished from unrelated identifiers by checking m.byID.
func collectDependencies(m *Module, body *ast.Body) {
	for _, node := range body.Nodes {
		block, ok := node.(*ast.Block)
		if !ok {
			continue
		}
		switch block.Type {
		case "resource":
			if len(block.Labels) == 2 {
				id := Entity{Kind: KindResource, Type: block.Labels[0], Name: block.Labels[1]}.ID()
				m.scanBody(id, block.Body)
			}
		case "data":
			if len(block.Labels) == 2 {
				id := Entity{Kind: KindData, Type: block.Labels[0], Name: block.Labels[1]}.ID()
				m.scanBody(id, block.Body)
			}
		case "output":
			if len(block.Labels) == 1 {
				id := Entity{Kind: KindOutput, Name: block.Labels[0]}.ID()
				m.scanBody(id, block.Body)
			}
		case "module":
			if len(block.Labels) == 1 {
				id := Entity{Kind: KindModule, Name: block.Labels[0]}.ID()
				m.scanBody(id, block.Body)
			}
		case "locals":
			// Each local attribute is its own entity — track deps per attribute.
			for _, n := range block.Body.Nodes {
				if attr, ok := n.(*ast.Attribute); ok {
					id := Entity{Kind: KindLocal, Name: attr.Name}.ID()
					m.scanExpr(id, attr.Value)
				}
			}
		}
	}
}

// scanBody walks every expression inside body and records entity references
// as dependency edges and validation errors.
func (m *Module) scanBody(fromID string, body *ast.Body) {
	ast.Inspect(body, func(n ast.Node) bool {
		if ref, ok := n.(*ast.RefExpr); ok {
			m.recordRef(fromID, ref)
		}
		return true
	})
}

// scanExpr walks a single expression and records entity references.
func (m *Module) scanExpr(fromID string, expr ast.Expr) {
	ast.Inspect(expr, func(n ast.Node) bool {
		if ref, ok := n.(*ast.RefExpr); ok {
			m.recordRef(fromID, ref)
		}
		return true
	})
}

// recordRef resolves one reference: adds a dep edge if the target is a known
// entity, or appends a ValidationError if the prefix is a user-space keyword
// (var/local/module/data) that must resolve to a declared entity.
// Resource-style refs (type.name) are not validated here to avoid false
// positives from for-expression iteration variables.
func (m *Module) recordRef(fromID string, ref *ast.RefExpr) {
	parts := ref.Parts
	if dep, found := m.classifyRef(parts); found {
		m.addDep(fromID, dep.ID())
		return
	}
	if len(parts) < 2 {
		return
	}
	var missing string
	switch parts[0] {
	case "var":
		missing = Entity{Kind: KindVariable, Name: parts[1]}.ID()
	case "local":
		missing = Entity{Kind: KindLocal, Name: parts[1]}.ID()
	case "module":
		missing = Entity{Kind: KindModule, Name: parts[1]}.ID()
	case "data":
		if len(parts) >= 3 {
			missing = Entity{Kind: KindData, Type: parts[1], Name: parts[2]}.ID()
		}
	}
	if missing != "" {
		m.valErrs = append(m.valErrs, ValidationError{
			EntityID: fromID,
			Ref:      missing,
			Pos:      ref.Pos,
		})
	}
}

// classifyRef interprets a RefExpr's parts as a Terraform entity reference.
// Returns the referenced entity and true if the reference points to a known
// entity, false otherwise.
func (m *Module) classifyRef(parts []string) (Entity, bool) {
	if len(parts) < 2 {
		return Entity{}, false
	}
	switch parts[0] {
	case "var":
		e := Entity{Kind: KindVariable, Name: parts[1]}
		if _, ok := m.byID[e.ID()]; ok {
			return e, true
		}
	case "local":
		e := Entity{Kind: KindLocal, Name: parts[1]}
		if _, ok := m.byID[e.ID()]; ok {
			return e, true
		}
	case "module":
		e := Entity{Kind: KindModule, Name: parts[1]}
		if _, ok := m.byID[e.ID()]; ok {
			return e, true
		}
	case "data":
		if len(parts) >= 3 {
			e := Entity{Kind: KindData, Type: parts[1], Name: parts[2]}
			if _, ok := m.byID[e.ID()]; ok {
				return e, true
			}
		}
	default:
		// Potential resource reference: type.name (e.g. aws_instance.web.id)
		if len(parts) >= 2 {
			e := Entity{Kind: KindResource, Type: parts[0], Name: parts[1]}
			if _, ok := m.byID[e.ID()]; ok {
				return e, true
			}
		}
	}
	return Entity{}, false
}
