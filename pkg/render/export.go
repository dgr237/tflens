package render

import (
	"encoding/json"
	"io"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/loader"
)

// ---- Experimental: export schema ----
//
// PROTOTYPE — shape is subject to change. The export emits the
// enriched entity model that tflens has already built up internally
// (entity types, defaults, evaluated values where statically
// resolvable, dependency graph, tracked-attribute markers) in a form
// suitable for downstream converters (kro, crossplane, Pulumi, etc.).
//
// Not yet emitted (deferred until a converter author tells us they
// need it): per-attribute resource bodies (only meta-args today),
// nested validation block contents, dynamic-block bodies, provider
// alias graph.
//
// The top-level _experimental flag and schema_version are explicit
// so consumers can detect breakage without guessing.

// 0.8.0-prototype adds five fields targeted at downstream converters
// that today re-implement the same classification logic on top of the
// raw AST. All are pure additions — old consumers ignore them and
// every prior fixture round-trips unchanged apart from the schema bump.
//
//   - ExportExpression.References: pre-computed reference index per
//     expression, replacing the per-converter AST walk that extracts
//     var.X / aws_T.Y / module.M.O / data.aws_T.X / local.L. Surfaces
//     the post-prefix attribute path and (for resources) any leading
//     index/splat so consumers building schemas know whether the
//     expression is per-instance or collection-shaped.
//
//   - ExportResource.CountKind / ExportModuleCall.CountKind: classifies
//     count = ... as include_when (cond ? 1 : 0 either order, with the
//     condition surfaced separately), scalar (literal number), or
//     expression (anything else). Collapses the pair of parallel
//     classifiers downstream converters keep in sync today.
//
//   - ExportResource.ForEachKind / ExportDynamicBlock.ForEachKind /
//     ExportModuleCall.ForEachKind: emits {kind: list|map|set|object|
//     tuple|invalid|unknown, reason?} from the same TFType the analyser
//     already infers, so converters don't redo the list-vs-map walk
//     against the per-RGD variableTypes map. Catches the
//     ternary-empty-fallback object pattern at the producer side.
//
//   - ExportConditionBlock.Folded (validations only): named-shape hint
//     for common validation patterns — enum (contains([...], var.X)),
//     min_length / max_length / length_range, minimum / maximum,
//     pattern (regex). Falls back to {kind: "complex"} so the AST is
//     still the source of truth.
//
//   - ExportModuleCallArgument.ChildVariableType: when the called
//     child module is loaded, each argument carries the child's
//     declared type constraint as cty/json — so converters can render
//     argument expressions correctly without resolving the child path
//     themselves. Omitted when the child isn't available (offline
//     runs / unresolved git or registry sources).
//
// 0.7.0-prototype adds ExportVariable.VariableTypeDefaults: a recursive
// tree of the per-attribute default values declared via the two-arg
// `optional(T, default)` form. The variable_type field already encodes
// *which* attributes are optional (third element of the object tuple);
// this new field gives consumers the corresponding default values so
// they can fill missing fields when generating downstream schemas.
// Pure addition — pruned when no defaults exist anywhere in the type
// tree, so existing fixtures without optional defaults round-trip
// unchanged apart from the schema bump.
//
// 0.6.0-prototype adds ExportVariable.VariableType: the *declared* type
// constraint encoded as cty/json's structural shape (`"string"`,
// `["map","string"]`, `["list",["object",{...}]]`, …). Sibling to the
// existing friendly Type ("map(string)") so consumers building a type
// tree (kro RGD schemas, Crossplane XRDs, Pulumi component schemas)
// don't have to re-parse the friendly form. Crucial when a default
// like `{}` would otherwise lose its declared shape: the runtime
// type of the empty literal is `object({})`, but the declared type
// might be `map(string)` or `list(object({...}))`. Pure addition.
//
// 0.5.0-prototype adds variable / output `description` strings as a
// top-level field on ExportVariable and ExportOutput. Only emitted
// when the source declared a constant-string description (omitempty
// drops it otherwise), so the field is purely additive — every prior
// 0.4.0 fixture round-trips unchanged.
//
// 0.4.0-prototype closes the last two "still deferred" items from
// the prototype's documented gap list:
//
//   - terraform.providers: top-level `provider "X" { alias = "...",
//     ... }` blocks now appear under terraform.providers (distinct
//     from required_providers, which is the version-constraint side).
//     Each entry carries Type, Alias?, and the per-attribute Config
//     map. Resources point at non-default instances via the existing
//     `provider` meta-arg → `provider = aws.east` matches the entry
//     with Type=aws + Alias=east.
//
//   - validation/precondition/postcondition body contents:
//     ExportVariable.Validations, ExportOutput.Preconditions /
//     Postconditions, ExportResource.Preconditions / Postconditions
//     each become a list of {condition, error_message?} entries
//     (each is a full {text, value?, ast?} expression triple).
//     Replaces the previous count + condition-text-list shape that
//     diff used internally for change detection.
//
// 0.3.0-prototype added dynamic_blocks: a recursive
// `{<name>: [{for_each, iterator?, content}, ...]}` map alongside
// the static blocks map on every resource/data body and on every
// nested block. Pure addition (no field renames) but the schema is
// bumped so consumers can detect the new surface.
//
// 0.2.0-prototype unified every expression-bearing field (count,
// for_each, depends_on, lifecycle, module-call arguments, locals,
// outputs, variable defaults, …) under the same {text, value?, ast?}
// shape that resource attributes use, so consumers see one shape
// wherever an HCL expression appears in the export. Schema bumped
// from 0.1.0-prototype because the *_text string fields were
// removed in favour of nested objects.
const ExportSchemaVersion = "0.8.0-prototype"

type Export struct {
	Experimental  bool       `json:"_experimental"`
	Warning       string     `json:"_warning"`
	SchemaVersion string     `json:"schema_version"`
	TflensVersion string     `json:"tflens_version,omitempty"`
	Root          ExportNode `json:"root"`
}

type ExportNode struct {
	Dir      string                `json:"dir"`
	Source   string                `json:"source,omitempty"`  // for child modules: the original module-call source string
	Version  string                `json:"version,omitempty"` // for child modules
	Module   ExportModule          `json:"module"`
	Children map[string]ExportNode `json:"children,omitempty"`
}

type ExportModule struct {
	Variables    []ExportVariable    `json:"variables"`
	Outputs      []ExportOutput      `json:"outputs"`
	Resources    []ExportResource    `json:"resources"`
	DataSources  []ExportResource    `json:"data_sources"`
	Locals       []ExportLocal       `json:"locals"`
	ModuleCalls  []ExportModuleCall  `json:"module_calls"`
	Terraform    ExportTerraform     `json:"terraform"`
	Tracked      []ExportTracked     `json:"tracked_attributes"`
	Dependencies map[string][]string `json:"dependencies"`
}

type ExportVariable struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	// VariableType is the *declared* type constraint encoded in the same
	// shape cty/json uses for value types — `"string"`, `["map","string"]`,
	// `["list",["object",{...}]]`, etc. Sibling to Type (the friendly
	// "map(string)" rendering) so consumers building a type tree don't
	// have to re-parse the friendly form. Crucial when a default like
	// `{}` would otherwise lose its declared shape: the runtime type of
	// the empty literal is `object({})`, but the declared type might be
	// `map(string)` or `list(object({...}))`. Emitted as RawMessage —
	// cty/json.MarshalType already produces valid JSON, so re-encoding
	// would double-quote.
	VariableType json.RawMessage `json:"variable_type,omitempty"`
	// VariableTypeDefaults captures the per-attribute default values
	// declared via the two-arg `optional(T, default)` form, as a
	// recursive tree that mirrors the type tree:
	//   - `values` — at object levels, attribute-name → default value
	//     (using the same {type, value} cty/json shape as evaluated
	//     expressions elsewhere in the export)
	//   - `attrs` — at object levels, attribute-name → recursive node
	//     for nested object attributes that themselves carry defaults
	//   - `element` — at list/set/map levels, the element-type's
	//     recursive defaults node
	// Nodes with no defaults at any depth are pruned, so an absent
	// `variable_type_defaults` field means "no per-attribute defaults
	// are declared anywhere in the type tree." Crucially, the Terraform-
	// level `default = ...` on the variable itself (the `default` field
	// above) is a separate concept from these per-attribute type defaults
	// and is unaffected.
	VariableTypeDefaults *ExportTypeDefaults    `json:"variable_type_defaults,omitempty"`
	Description          string                 `json:"description,omitempty"`
	HasDefault           bool                   `json:"has_default"`
	Default              *ExportExpression      `json:"default,omitempty"`
	Sensitive            bool                   `json:"sensitive,omitempty"`
	Ephemeral            bool                   `json:"ephemeral,omitempty"`
	Nullable             bool                   `json:"nullable"`
	Validations          []ExportConditionBlock `json:"validations,omitempty"`
	Location             string                 `json:"location,omitempty"`
}

// ExportTypeDefaults is one node of the recursive default-value tree
// surfaced under variable_type_defaults. See ExportVariable.VariableTypeDefaults
// for the shape. This is a translation of hashicorp's typeexpr.Defaults
// — same information, but the internal `Children` map (which keys
// nested-object attrs by name and list/set/map element-types by the
// empty string) is split into the dedicated `Attrs` and `Element`
// fields so consumers don't have to special-case the empty key.
type ExportTypeDefaults struct {
	Values  map[string]*ExportCtyJSON      `json:"values,omitempty"`
	Attrs   map[string]*ExportTypeDefaults `json:"attrs,omitempty"`
	Element *ExportTypeDefaults            `json:"element,omitempty"`
}

type ExportOutput struct {
	Name           string                 `json:"name"`
	Description    string                 `json:"description,omitempty"`
	Value          *ExportExpression      `json:"value,omitempty"`
	Sensitive      bool                   `json:"sensitive,omitempty"`
	Ephemeral      bool                   `json:"ephemeral,omitempty"`
	DependsOn      *ExportExpression      `json:"depends_on,omitempty"`
	Preconditions  []ExportConditionBlock `json:"preconditions,omitempty"`
	Postconditions []ExportConditionBlock `json:"postconditions,omitempty"`
	Location       string                 `json:"location,omitempty"`
}

// ExportConditionBlock is one validation / precondition / postcondition
// block surfaced under the entity that declared it. Both Condition and
// ErrorMessage use the standard {text, value?, ast?} shape so consumers
// (converters, generators, schema validators) can decide whether to
// translate the boolean check into the target's native validation
// primitive or keep the source text.
//
// Folded is set only on `validation` blocks (not preconditions /
// postconditions) and carries a named-shape hint for the common
// patterns downstream converters today re-derive from the AST: enum
// (`contains([…], var.X)`), min_length / max_length / length_range
// (length(x) bounds), minimum / maximum (numeric bounds), pattern
// (regex). Anything that doesn't match a recognised shape gets
// {kind: "complex"} so the AST stays the source of truth — Folded is
// purely a convenience field.
type ExportConditionBlock struct {
	Condition    *ExportExpression       `json:"condition,omitempty"`
	ErrorMessage *ExportExpression       `json:"error_message,omitempty"`
	Folded       *ExportValidationFolded `json:"folded,omitempty"`
	Location     string                  `json:"location,omitempty"`
}

type ExportResource struct {
	Type                string                          `json:"type"`
	Name                string                          `json:"name"`
	Provider            *ExportExpression               `json:"provider,omitempty"`
	Count               *ExportExpression               `json:"count,omitempty"`
	ForEach             *ExportExpression               `json:"for_each,omitempty"`
	CountKind           *ExportCountKind                `json:"count_kind,omitempty"`
	ForEachKind         *ExportForEachKind              `json:"for_each_kind,omitempty"`
	DependsOn           *ExportExpression               `json:"depends_on,omitempty"`
	PreventDestroy      bool                            `json:"prevent_destroy,omitempty"`
	CreateBeforeDestroy bool                            `json:"create_before_destroy,omitempty"`
	IgnoreChanges       *ExportExpression               `json:"ignore_changes,omitempty"`
	ReplaceTriggeredBy  *ExportExpression               `json:"replace_triggered_by,omitempty"`
	Attributes          map[string]ExportExpression     `json:"attributes,omitempty"`
	Blocks              map[string][]ExportBlock        `json:"blocks,omitempty"`
	Preconditions       []ExportConditionBlock          `json:"preconditions,omitempty"`
	Postconditions      []ExportConditionBlock          `json:"postconditions,omitempty"`
	DynamicBlocks       map[string][]ExportDynamicBlock `json:"dynamic_blocks,omitempty"`
	Location            string                          `json:"location,omitempty"`
	// Note: `lifecycle` is excluded from Blocks because its meta-arg
	// attributes are projected into dedicated parent-resource fields
	// (PreventDestroy / IgnoreChanges / …). `dynamic` blocks are
	// surfaced separately in DynamicBlocks rather than Blocks because
	// their semantics are different — they generate the named block
	// at plan time from a for_each iteration over a content template.
}

// ExportExpression is the shared shape for every HCL expression that
// appears in the export — resource attributes, count/for_each/
// depends_on, lifecycle ignore_changes / replace_triggered_by,
// module-call arguments, locals, outputs, variable defaults. Four
// complementary fields:
//
//   - text:  the source as written. Always present. Lossless for
//     consumers that just want to pass the expression through.
//   - value: the evaluated cty value when the curated stdlib resolves
//     the expression. Lets converters emit literal values directly
//     for the common case of literal-heavy expressions
//     (`tags = { Name = "web" }`, `count = 3`, …).
//   - ast:   the structural decomposition (function calls, traversals,
//     conditionals, …). Lets converters translate expressions into
//     the target language without re-parsing the text. Especially
//     valuable for non-Go consumers that don't want to embed an HCL
//     parser. See export_ast.go for the supported node kinds.
//   - references: pre-computed index of every var.X / aws_T.Y /
//     module.M.O / data.aws_T.X / local.L referenced anywhere in
//     this expression, with the post-prefix attribute path and (for
//     resources) any leading `[k]` index or splat. Lets consumers
//     do reference work without walking the AST. See export_refs.go.
//
// Renamed from ExportAttribute in 0.2.0-prototype: same shape, but
// "expression" describes its scope better now that count/for_each/
// arguments/locals/etc. all use it too.
type ExportExpression struct {
	Text       string            `json:"text"`
	Value      *ExportCtyJSON    `json:"value,omitempty"`
	AST        any               `json:"ast,omitempty"`
	References *ExportReferences `json:"references,omitempty"`
}

// ExportBlock is one nested block instance — recursive, since blocks
// can themselves contain blocks (e.g. aws_eks_cluster's
// `encryption_config { provider { key_arn = ... } }`). Repeated
// blocks (`ebs_block_device { ... }` × N) appear as multiple entries
// in the parent's []ExportBlock slice, in source order.
//
// DynamicBlocks captures `dynamic "<name>" { ... }` nested inside
// this block, so dynamic-inside-static (or dynamic-inside-dynamic
// via Content) recurses uniformly.
type ExportBlock struct {
	Attributes    map[string]ExportExpression     `json:"attributes,omitempty"`
	Blocks        map[string][]ExportBlock        `json:"blocks,omitempty"`
	DynamicBlocks map[string][]ExportDynamicBlock `json:"dynamic_blocks,omitempty"`
	Location      string                          `json:"location,omitempty"`
}

// ExportDynamicBlock is one `dynamic "<name>" { for_each = ...,
// iterator = ..., content { ... } }` instance. The block label (the
// "<name>" part) is the key in the parent's DynamicBlocks map and
// names the block type to generate at plan time.
//
// ForEach is the iteration source as an ExportExpression — the same
// {text, value?, ast?} shape used everywhere else, so converters
// can translate references to var.X / local.Y / resource.foo here.
//
// Iterator is the per-iteration variable name. When omitted in the
// source it defaults to the block label — emitted as empty here so
// consumers can choose whether to fold the default in. The content
// body's expressions reference iterator.value / iterator.key, so
// converters need this to map those refs to the target language's
// iteration variable.
//
// Content is the template body, reusing ExportBlock so dynamic-
// inside-dynamic (and static-inside-dynamic) work via the same
// recursion.
type ExportDynamicBlock struct {
	ForEach     *ExportExpression  `json:"for_each,omitempty"`
	ForEachKind *ExportForEachKind `json:"for_each_kind,omitempty"`
	Iterator    string             `json:"iterator,omitempty"`
	Content     ExportBlock        `json:"content"`
	Location    string             `json:"location,omitempty"`
}

type ExportLocal struct {
	Name     string            `json:"name"`
	Value    *ExportExpression `json:"value,omitempty"`
	Location string            `json:"location,omitempty"`
}

type ExportModuleCall struct {
	Name        string                              `json:"name"`
	Source      string                              `json:"source,omitempty"`
	Version     string                              `json:"version,omitempty"`
	Arguments   map[string]ExportModuleCallArgument `json:"arguments,omitempty"`
	Count       *ExportExpression                   `json:"count,omitempty"`
	ForEach     *ExportExpression                   `json:"for_each,omitempty"`
	CountKind   *ExportCountKind                    `json:"count_kind,omitempty"`
	ForEachKind *ExportForEachKind                  `json:"for_each_kind,omitempty"`
	Location    string                              `json:"location,omitempty"`
}

// ExportModuleCallArgument is one argument passed to a module call.
// Embeds ExportExpression so the {text, value?, ast?, references?}
// shape stays identical to every other expression in the export, then
// adds ChildVariableType: the called child module's declared type
// constraint for the argument's name (in the same cty/json shape used
// by ExportVariable.VariableType). Only populated when the called
// child is loaded — omitted otherwise so consumers can detect "child
// type unknown" vs "child has no such variable" via the field's
// presence rather than parsing a sentinel value.
//
// Marshalled as a flat object via json.Marshaler so consumers see
// `{ "text": ..., "child_variable_type": ... }` rather than a nested
// expression object.
type ExportModuleCallArgument struct {
	ExportExpression
	ChildVariableType json.RawMessage `json:"child_variable_type,omitempty"`
}

type ExportTerraform struct {
	RequiredVersion   string                       `json:"required_version,omitempty"`
	RequiredProviders map[string]ExportProviderReq `json:"required_providers,omitempty"`
	Backend           *ExportBackend               `json:"backend,omitempty"`
	// Providers is the list of top-level `provider "X" { ... }` blocks
	// in source order — distinct from required_providers (the version
	// declarations). Each entry carries Type, Alias?, and the
	// per-attribute Config map. Resources select non-default instances
	// via the existing `provider` meta-arg → `provider = aws.east`
	// matches Type=aws + Alias=east.
	Providers []ExportProvider `json:"providers,omitempty"`
}

type ExportProviderReq struct {
	Source             string `json:"source,omitempty"`
	VersionConstraint  string `json:"version_constraint,omitempty"`
	ConfigurationAlias string `json:"configuration_alias,omitempty"`
}

// ExportProvider is one top-level `provider "X" { ... }` block.
// Alias is empty for the default instance. Config holds the per-
// attribute body (excluding `alias`, which is promoted to its own
// field) as the standard {text, value?, ast?} expression triple.
type ExportProvider struct {
	Type     string                      `json:"type"`
	Alias    string                      `json:"alias,omitempty"`
	Config   map[string]ExportExpression `json:"config,omitempty"`
	Location string                      `json:"location,omitempty"`
}

type ExportBackend struct {
	Type string `json:"type"`
}

type ExportTracked struct {
	Subject string `json:"subject"`
	// Note: ExpressionText (not the unified ExportExpression) because
	// pkg/analysis.TrackedAttribute keeps the underlying *Expr private
	// — we only have the canonical text. Promoting to the {text, value?, ast?}
	// shape needs a small pkg/analysis API addition; deferred.
	ExpressionText string `json:"expression_text,omitempty"`
	Marker         string `json:"marker,omitempty"`
	Location       string `json:"location,omitempty"`
}

// ExportCtyJSON wraps a cty.Value as its JSON representation plus the
// type marker. Both fields are RawMessage because cty/json already
// emits valid JSON (including the surrounding quotes for string-shaped
// type tags) — re-encoding via Go's json package would double-quote.
// Consumers can use the value directly or reconstruct the cty.Value
// via cty/json.Unmarshal(value, cty/json.UnmarshalType(type)).
type ExportCtyJSON struct {
	Type  json.RawMessage `json:"type"`
	Value json.RawMessage `json:"value"`
}

func ctyToExport(v cty.Value) *ExportCtyJSON {
	if v == cty.NilVal || !v.IsKnown() || v.IsNull() {
		return nil
	}
	raw, err := ctyjson.Marshal(v, v.Type())
	if err != nil {
		return nil
	}
	typeRaw, err := ctyjson.MarshalType(v.Type())
	if err != nil {
		// Fallback: emit the friendly name as a JSON string so the
		// shape stays consistent (always valid JSON in the type field).
		typeRaw, _ = json.Marshal(v.Type().FriendlyName())
	}
	return &ExportCtyJSON{Type: typeRaw, Value: raw}
}

// BuildExport walks the project tree and produces the Export envelope.
// tflensVersion is optional (callers can pass "" if unknown).
func BuildExport(p *loader.Project, tflensVersion string) Export {
	exp := Export{
		Experimental:  true,
		Warning:       "Output shape is experimental and subject to change. Do not depend on field stability across minor versions.",
		SchemaVersion: ExportSchemaVersion,
		TflensVersion: tflensVersion,
	}
	if p == nil || p.Root == nil {
		return exp
	}
	exp.Root = exportNode(p.Root, "", "")
	return exp
}

func exportNode(n *loader.ModuleNode, source, version string) ExportNode {
	out := ExportNode{
		Dir:     n.Dir,
		Source:  source,
		Version: version,
		Module:  exportModule(n.Module, n.Children),
	}
	if len(n.Children) > 0 {
		out.Children = make(map[string]ExportNode, len(n.Children))
		for name, child := range n.Children {
			cs, cv := "", ""
			if n.Module != nil {
				cs = n.Module.ModuleSource(name)
				cv = n.Module.ModuleVersion(name)
			}
			out.Children[name] = exportNode(child, cs, cv)
		}
	}
	return out
}

func exportModule(m *analysis.Module, children map[string]*loader.ModuleNode) ExportModule {
	out := ExportModule{
		Variables:    []ExportVariable{},
		Outputs:      []ExportOutput{},
		Resources:    []ExportResource{},
		DataSources:  []ExportResource{},
		Locals:       []ExportLocal{},
		ModuleCalls:  []ExportModuleCall{},
		Tracked:      []ExportTracked{},
		Dependencies: map[string][]string{},
	}
	if m == nil {
		return out
	}
	rc := &renderCtx{m: m, ctx: m.EvalContext(), children: children}
	for _, e := range m.Entities() {
		switch e.Kind {
		case analysis.KindVariable:
			out.Variables = append(out.Variables, exportVariable(e, rc))
		case analysis.KindOutput:
			out.Outputs = append(out.Outputs, exportOutput(e, rc))
		case analysis.KindResource:
			out.Resources = append(out.Resources, exportResource(e, rc))
		case analysis.KindData:
			out.DataSources = append(out.DataSources, exportResource(e, rc))
		case analysis.KindLocal:
			out.Locals = append(out.Locals, exportLocal(e, rc))
		case analysis.KindModule:
			out.ModuleCalls = append(out.ModuleCalls, exportModuleCall(e, rc))
		}
		// Dependency adjacency — emitted for every entity, not just one
		// kind, so converters get the full graph.
		if deps := m.Dependencies(e.ID()); len(deps) > 0 {
			sorted := append([]string(nil), deps...)
			sort.Strings(sorted)
			out.Dependencies[e.ID()] = sorted
		}
	}
	out.Terraform = exportTerraform(rc)
	out.Tracked = exportTrackedAttributes(m)

	// Stable per-section ordering — converters depend on deterministic output.
	sortByName := func(less func(i, j int) bool) func(int, int) bool { return less }
	sort.Slice(out.Variables, sortByName(func(i, j int) bool { return out.Variables[i].Name < out.Variables[j].Name }))
	sort.Slice(out.Outputs, sortByName(func(i, j int) bool { return out.Outputs[i].Name < out.Outputs[j].Name }))
	sort.Slice(out.Locals, sortByName(func(i, j int) bool { return out.Locals[i].Name < out.Locals[j].Name }))
	sort.Slice(out.ModuleCalls, sortByName(func(i, j int) bool { return out.ModuleCalls[i].Name < out.ModuleCalls[j].Name }))
	sort.Slice(out.Resources, func(i, j int) bool {
		if out.Resources[i].Type != out.Resources[j].Type {
			return out.Resources[i].Type < out.Resources[j].Type
		}
		return out.Resources[i].Name < out.Resources[j].Name
	})
	sort.Slice(out.DataSources, func(i, j int) bool {
		if out.DataSources[i].Type != out.DataSources[j].Type {
			return out.DataSources[i].Type < out.DataSources[j].Type
		}
		return out.DataSources[i].Name < out.DataSources[j].Name
	})
	sort.Slice(out.Tracked, func(i, j int) bool { return out.Tracked[i].Subject < out.Tracked[j].Subject })
	return out
}

// renderCtx bundles the per-module context every helper in export.go
// needs: the analysis Module (for entity lookups in extractReferences,
// type inference in for_each_kind classification, child-module access
// via the loader children map), the curated EvalContext (so expression
// evaluation reuses one cached context per module), the loader
// children (used by exportModuleCall to pull the called child's
// declared variable types into argument bindings), and the per-scope
// dynamic-block iterator stack (consulted by resolveTraversalType so
// `<iter>.value(.path)` references inside dynamic content infer
// against the iterator's element type rather than falling through to
// "unknown").
//
// One renderCtx per module is built at the top of exportModule and
// threaded through every recursive helper. The iterator stack is
// pushed only when descending into a dynamic block's content (via
// pushIterator) and is left unchanged everywhere else, so threading a
// struct rather than separate parameters keeps the per-call surface
// stable while letting the iterator binding propagate down.
type renderCtx struct {
	m         *analysis.Module
	ctx       *hcl.EvalContext
	children  map[string]*loader.ModuleNode
	iterators []iteratorScope
}

// iteratorScope is one dynamic-block iterator binding: the iterator
// name (defaulting to the dynamic block's label when not explicitly
// renamed) and the element type of the for_each source. Only the
// element type is captured because `<iter>.value` is the iteration's
// per-step value (an element of the source); `<iter>.key` is always
// a string for map iteration so we don't bother typing it explicitly.
//
// Stacked innermost-last: classifyForEach walks the stack so a
// shadowing inner iterator wins over an outer one of the same name.
type iteratorScope struct {
	name        string
	elementType *analysis.TFType
}

// pushIterator returns a new renderCtx whose iterator stack ends with
// the given binding. Returns rc unchanged when scope.name is empty
// (malformed dynamic block) or scope.elementType is nil (we couldn't
// infer the for_each source's element type, in which case the
// binding wouldn't help downstream resolution anyway).
//
// The new stack is allocated independently so siblings don't share a
// growing append-target slice — important because exportBlock recurses
// into multiple branches that each need their own scope view.
func (rc *renderCtx) pushIterator(scope iteratorScope) *renderCtx {
	if scope.name == "" || scope.elementType == nil {
		return rc
	}
	stack := make([]iteratorScope, len(rc.iterators)+1)
	copy(stack, rc.iterators)
	stack[len(rc.iterators)] = scope
	out := *rc
	out.iterators = stack
	return &out
}

// exprToExport is the universal builder for ExportExpression — every
// place that emits an HCL expression goes through this so the {text,
// value?, ast?, references?} contract is identical across attributes,
// count, for_each, depends_on, lifecycle, module-call arguments,
// locals, outputs, variable defaults, etc. Returns nil for a nil/empty
// *Expr so callers can plug it directly into pointer-typed export
// fields (the zero result then disappears thanks to omitempty).
func exprToExport(e *analysis.Expr, rc *renderCtx) *ExportExpression {
	if e == nil || e.E == nil {
		return nil
	}
	return &ExportExpression{
		Text:       e.Text(),
		Value:      evalToExport(e, rc.ctx),
		AST:        astFor(e),
		References: extractReferences(e, rc.m),
	}
}

func exportVariable(e analysis.Entity, rc *renderCtx) ExportVariable {
	v := ExportVariable{
		Name:        e.Name,
		Description: e.Description,
		HasDefault:  e.HasDefault,
		Sensitive:   e.Sensitive,
		Ephemeral:   e.Ephemeral,
		Nullable:    !e.NonNullable,
		Validations: exportConditionBlocks(e.Validations, rc, true),
		Location:    e.Location(),
	}
	if e.DeclaredType != nil {
		v.Type = e.DeclaredType.String()
		if e.DeclaredType.HasCty() {
			if raw, err := ctyjson.MarshalType(e.DeclaredType.Cty); err == nil {
				v.VariableType = raw
			}
		}
		v.VariableTypeDefaults = exportTypeDefaults(e.DeclaredType.Defaults)
	}
	v.Default = exprToExport(e.DefaultExpr, rc)
	return v
}

// exportTypeDefaults walks hashicorp's typeexpr.Defaults tree into the
// export's ExportTypeDefaults shape. Returns nil when the input has no
// defaults at any depth — including the case where a Children entry
// recurses into a subtree whose own defaults are empty — so that the
// `omitempty` tag prunes empty branches all the way up to the root.
// Element-type defaults (Children keyed by the empty string for
// list/set/map types) are split out into the dedicated Element field
// so consumers don't have to special-case the empty key.
func exportTypeDefaults(d *typeexpr.Defaults) *ExportTypeDefaults {
	if d == nil {
		return nil
	}
	out := &ExportTypeDefaults{}
	if len(d.DefaultValues) > 0 {
		out.Values = make(map[string]*ExportCtyJSON, len(d.DefaultValues))
		for k, val := range d.DefaultValues {
			out.Values[k] = ctyToExport(val)
		}
	}
	for k, child := range d.Children {
		sub := exportTypeDefaults(child)
		if sub == nil {
			continue
		}
		if k == "" {
			out.Element = sub
			continue
		}
		if out.Attrs == nil {
			out.Attrs = make(map[string]*ExportTypeDefaults)
		}
		out.Attrs[k] = sub
	}
	if len(out.Values) == 0 && len(out.Attrs) == 0 && out.Element == nil {
		return nil
	}
	return out
}

func exportOutput(e analysis.Entity, rc *renderCtx) ExportOutput {
	return ExportOutput{
		Name:           e.Name,
		Description:    e.Description,
		Value:          exprToExport(e.ValueExpr, rc),
		Sensitive:      e.Sensitive,
		Ephemeral:      e.Ephemeral,
		DependsOn:      exprToExport(e.DependsOnExpr, rc),
		Preconditions:  exportConditionBlocks(e.Preconditions, rc, false),
		Postconditions: exportConditionBlocks(e.Postconditions, rc, false),
		Location:       e.Location(),
	}
}

// exportConditionBlocks converts the analysis-side ConditionBlock
// slice into the wire-format ExportConditionBlock list. Both inner
// expressions go through exprToExport so consumers get the same
// {text, value?, ast?, references?} shape they get for every other
// expression in the export. When isValidation is true, the per-block
// folded hint is computed (only validation blocks get the structured
// fold; precondition / postcondition blocks describe runtime invariants
// that don't map cleanly to a fixed schema vocabulary). Nil/empty
// input → nil output so the omitempty tag drops the whole field.
func exportConditionBlocks(blocks []analysis.ConditionBlock, rc *renderCtx, isValidation bool) []ExportConditionBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]ExportConditionBlock, len(blocks))
	for i, b := range blocks {
		entry := ExportConditionBlock{
			Condition:    exprToExport(b.Condition, rc),
			ErrorMessage: exprToExport(b.ErrorMessage, rc),
		}
		if isValidation {
			entry.Folded = foldValidation(b.Condition)
		}
		if b.Pos.File != "" {
			entry.Location = filepath.Base(b.Pos.File) + ":" + strconv.Itoa(b.Pos.Line)
		}
		out[i] = entry
	}
	return out
}

func exportResource(e analysis.Entity, rc *renderCtx) ExportResource {
	r := ExportResource{
		Type:                e.Type,
		Name:                e.Name,
		Provider:            exprToExport(e.ProviderExpr, rc),
		Count:               exprToExport(e.CountExpr, rc),
		ForEach:             exprToExport(e.ForEachExpr, rc),
		CountKind:           classifyCount(e.CountExpr),
		ForEachKind:         classifyForEach(e.ForEachExpr, rc),
		DependsOn:           exprToExport(e.DependsOnExpr, rc),
		PreventDestroy:      e.PreventDestroy,
		CreateBeforeDestroy: e.CreateBeforeDestroy,
		IgnoreChanges:       exprToExport(e.IgnoreChangesExpr, rc),
		ReplaceTriggeredBy:  exprToExport(e.ReplaceTriggeredByExpr, rc),
		Preconditions:       exportConditionBlocks(e.Preconditions, rc, false),
		Postconditions:      exportConditionBlocks(e.Postconditions, rc, false),
		Location:            e.Location(),
	}
	// Resource-level for_each binds `each.value` / `each.key` for the
	// entire resource body — including all nested dynamic blocks. Push
	// the binding so traversals like `each.value.foo` deep inside a
	// dynamic chain resolve through the same iterator-scope mechanism
	// dynamic blocks use.
	bodyRC := rc
	if e.ForEachExpr != nil && e.ForEachExpr.E != nil {
		if elem := iteratorElementType(e.ForEachExpr.E, rc); elem != nil {
			bodyRC = rc.pushIterator(iteratorScope{name: "each", elementType: elem})
		}
	}
	if len(e.BodyAttrs) > 0 {
		r.Attributes = make(map[string]ExportExpression, len(e.BodyAttrs))
		for name, expr := range e.BodyAttrs {
			r.Attributes[name] = *exprToExport(expr, bodyRC)
		}
	}
	if len(e.BodyBlocks) > 0 {
		r.Blocks = make(map[string][]ExportBlock, len(e.BodyBlocks))
		for name, instances := range e.BodyBlocks {
			out := make([]ExportBlock, 0, len(instances))
			for _, b := range instances {
				out = append(out, exportBlock(b, bodyRC))
			}
			r.Blocks[name] = out
		}
	}
	if len(e.BodyDynamicBlocks) > 0 {
		r.DynamicBlocks = make(map[string][]ExportDynamicBlock, len(e.BodyDynamicBlocks))
		for name, instances := range e.BodyDynamicBlocks {
			out := make([]ExportDynamicBlock, 0, len(instances))
			for _, d := range instances {
				out = append(out, exportDynamicBlock(d, bodyRC, name))
			}
			r.DynamicBlocks[name] = out
		}
	}
	return r
}

// exportBlock recursively converts a *analysis.BodyBlock into an
// ExportBlock. Mirrors the parent-resource shape (attributes +
// nested blocks) so consumers walk one type at every depth.
func exportBlock(b *analysis.BodyBlock, rc *renderCtx) ExportBlock {
	out := ExportBlock{}
	if b == nil {
		return out
	}
	if b.Pos.File != "" {
		out.Location = filepath.Base(b.Pos.File) + ":" + strconv.Itoa(b.Pos.Line)
	}
	if len(b.Attrs) > 0 {
		out.Attributes = make(map[string]ExportExpression, len(b.Attrs))
		for name, expr := range b.Attrs {
			out.Attributes[name] = *exprToExport(expr, rc)
		}
	}
	if len(b.Blocks) > 0 {
		out.Blocks = make(map[string][]ExportBlock, len(b.Blocks))
		for name, instances := range b.Blocks {
			children := make([]ExportBlock, 0, len(instances))
			for _, c := range instances {
				children = append(children, exportBlock(c, rc))
			}
			out.Blocks[name] = children
		}
	}
	if len(b.DynamicBlocks) > 0 {
		out.DynamicBlocks = make(map[string][]ExportDynamicBlock, len(b.DynamicBlocks))
		for name, instances := range b.DynamicBlocks {
			children := make([]ExportDynamicBlock, 0, len(instances))
			for _, d := range instances {
				children = append(children, exportDynamicBlock(d, rc, name))
			}
			out.DynamicBlocks[name] = children
		}
	}
	return out
}

// exportDynamicBlock converts a *analysis.BodyDynamicBlock into the
// wire-format ExportDynamicBlock. Content recurses through exportBlock
// so dynamic-inside-static and dynamic-inside-dynamic both work.
//
// The for_each expression goes through exprToExport like every other
// HCL expression (text + value + AST), even when it would resolve
// statically — converters need the structural shape to translate
// to the target system's iteration primitive (kro for-each, crossplane
// composition, …).
func exportDynamicBlock(d *analysis.BodyDynamicBlock, rc *renderCtx, label string) ExportDynamicBlock {
	out := ExportDynamicBlock{
		ForEach:     exprToExport(d.ForEach, rc),
		ForEachKind: classifyForEach(d.ForEach, rc),
		Iterator:    d.Iterator,
	}
	if d.Pos.File != "" {
		out.Location = filepath.Base(d.Pos.File) + ":" + strconv.Itoa(d.Pos.Line)
	}
	// The iterator name defaults to the block label when the source
	// doesn't explicitly rename via `iterator = <name>`. Push it onto
	// the renderCtx so references to `<iter>.value(.path)` inside the
	// content body resolve through the for_each source's element type.
	iterName := d.Iterator
	if iterName == "" {
		iterName = label
	}
	contentRC := rc
	if d.ForEach != nil && d.ForEach.E != nil {
		if elem := iteratorElementType(d.ForEach.E, rc); elem != nil {
			contentRC = rc.pushIterator(iteratorScope{name: iterName, elementType: elem})
		}
	}
	if d.Content != nil {
		out.Content = exportBlock(d.Content, contentRC)
	}
	return out
}

func exportLocal(e analysis.Entity, rc *renderCtx) ExportLocal {
	return ExportLocal{
		Name:     e.Name,
		Value:    exprToExport(e.LocalExpr, rc),
		Location: e.Location(),
	}
}

func exportModuleCall(e analysis.Entity, rc *renderCtx) ExportModuleCall {
	c := ExportModuleCall{
		Name:        e.Name,
		Source:      rc.m.ModuleSource(e.Name),
		Version:     rc.m.ModuleVersion(e.Name),
		Count:       exprToExport(e.CountExpr, rc),
		ForEach:     exprToExport(e.ForEachExpr, rc),
		CountKind:   classifyCount(e.CountExpr),
		ForEachKind: classifyForEach(e.ForEachExpr, rc),
		Location:    e.Location(),
	}
	// childMod is the analysed child module (when loaded) — used to
	// pull each argument's declared variable_type into the wire shape
	// so converters don't have to resolve module sources themselves.
	// Nil when the child wasn't loaded (offline runs / unresolvable
	// source), in which case child_variable_type is omitted per arg.
	var childMod *analysis.Module
	if rc.children != nil {
		if cn, ok := rc.children[e.Name]; ok && cn != nil {
			childMod = cn.Module
		}
	}
	if len(e.ModuleArgs) > 0 {
		c.Arguments = make(map[string]ExportModuleCallArgument, len(e.ModuleArgs))
		for k, v := range e.ModuleArgs {
			arg := ExportModuleCallArgument{ExportExpression: *exprToExport(v, rc)}
			if childMod != nil {
				if cv, ok := childMod.EntityByID((analysis.Entity{Kind: analysis.KindVariable, Name: k}).ID()); ok {
					if cv.DeclaredType != nil && cv.DeclaredType.HasCty() {
						if raw, err := ctyjson.MarshalType(cv.DeclaredType.Cty); err == nil {
							arg.ChildVariableType = raw
						}
					}
				}
			}
			c.Arguments[k] = arg
		}
	}
	return c
}

func exportTerraform(rc *renderCtx) ExportTerraform {
	m := rc.m
	t := ExportTerraform{
		RequiredVersion: m.RequiredVersion(),
	}
	if reqs := m.RequiredProviders(); len(reqs) > 0 {
		t.RequiredProviders = make(map[string]ExportProviderReq, len(reqs))
		for name, r := range reqs {
			t.RequiredProviders[name] = ExportProviderReq{
				Source:            r.Source,
				VersionConstraint: r.Version,
			}
		}
	}
	if b := m.Backend(); b != nil {
		t.Backend = &ExportBackend{Type: b.Type}
	}
	if provs := m.Providers(); len(provs) > 0 {
		t.Providers = make([]ExportProvider, 0, len(provs))
		for _, p := range provs {
			ep := ExportProvider{Type: p.Type, Alias: p.Alias}
			if p.Pos.File != "" {
				ep.Location = filepath.Base(p.Pos.File) + ":" + strconv.Itoa(p.Pos.Line)
			}
			if len(p.Config) > 0 {
				ep.Config = make(map[string]ExportExpression, len(p.Config))
				for name, expr := range p.Config {
					ep.Config[name] = *exprToExport(expr, rc)
				}
			}
			t.Providers = append(t.Providers, ep)
		}
	}
	return t
}

func exportTrackedAttributes(m *analysis.Module) []ExportTracked {
	// Note: TrackedAttribute exposes ExprText (canonical text) but not
	// the raw expression — that's an internal of pkg/analysis. The
	// export emits the text only; converters can re-parse if they want
	// re-evaluation, or use the per-local evaluated_value fields when
	// the tracked attribute references a local.
	tracked := m.TrackedAttributes()
	out := make([]ExportTracked, 0, len(tracked))
	for _, ta := range tracked {
		t := ExportTracked{
			Subject:        ta.Key(),
			ExpressionText: ta.ExprText,
			Marker:         ta.Description,
		}
		if ta.Pos.File != "" {
			// Match the basename convention used by Entity.Location()
			// (file:line, not abspath:line) so consumers see consistent
			// short locations across all entity kinds.
			t.Location = filepath.Base(ta.Pos.File) + ":" + strconv.Itoa(ta.Pos.Line)
		}
		out = append(out, t)
	}
	return out
}

// evalToExport tries to evaluate the expression against the module's
// EvalContext. Returns nil when the expression can't be statically
// resolved (e.g. references a data source, computed attribute, or
// non-curated function) — that's the conservative-fallback principle:
// converters get the text and can choose what to do with unevaluable
// expressions.
func evalToExport(e *analysis.Expr, ctx *hcl.EvalContext) *ExportCtyJSON {
	if e == nil || e.E == nil || ctx == nil {
		return nil
	}
	v, diags := e.E.Value(ctx)
	if diags.HasErrors() {
		return nil
	}
	return ctyToExport(v)
}

// WriteExport marshals exp as pretty-printed JSON to w.
func WriteExport(exp Export, w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(exp)
}
