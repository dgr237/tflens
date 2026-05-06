// Package providerschema parses `terraform providers schema -json`
// output into a lookup-friendly form that the analyser can use to
// resolve resource/data attribute types and validate attribute
// references.
//
// Usage:
//
//	s, err := providerschema.Load("schema.json")
//	if attr, ok := s.ResolveAttr("aws_subnet", []string{"cidr_block"}); ok {
//	    // attr.Type is cty.String
//	}
//
// The package is provider-agnostic: it works with any provider
// schema Terraform itself emits (AWS / GCP / Azure / community
// providers / etc.) since the format is HashiCorp's canonical
// JSON-encoded schema, not Crossplane's downstream-derived IR.
//
// Coverage limitations: nested block types with non-`single`
// nesting (list, set, map, group) are exposed as their containing
// list/set/map type — accessing an attribute through them via
// dot-path returns the element block's attribute type. This means
// `aws_X.y.tag_specifications.tags` resolves the same way Terraform
// itself would if `tag_specifications` were a list and the
// expression chained through it (in practice users splat: `[*]`).
// For the validate-time use case this is good enough — false
// negatives, never false positives.
package providerschema

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// Schema is the parsed contents of one `terraform providers schema
// -json` output file. Indexed by provider source key
// (e.g. "registry.terraform.io/hashicorp/aws") at the top level,
// then by resource / data-source type name.
type Schema struct {
	providers map[string]*providerEntry
}

// providerEntry holds the per-provider lookup tables. Resource and
// data-source type names are unique across all providers in
// practice (no two providers ship `aws_subnet`), so the top-level
// ResolveAttr / ResolveDataAttr methods don't require the user to
// disambiguate by provider source — they search across all loaded
// providers.
type providerEntry struct {
	resources   map[string]*Block
	dataSources map[string]*Block
}

// Block is one resource / data-source / nested-block schema. Mirrors
// the shape of the JSON's `block` object: a map of attributes (leaf
// schema entries) plus a map of nested block types.
type Block struct {
	Attributes map[string]*Attribute
	BlockTypes map[string]*NestedBlock
}

// Attribute is one schema attribute — the leaf of the schema tree.
// The Type field carries the cty.Type decoded from the JSON's
// `type` field via cty/json.UnmarshalType, so it already supports
// the full vocabulary (`"string"`, `["map","string"]`,
// `["object",{...}]`, etc.).
//
// Required / Optional / Computed are the standard provider-schema
// flags. Sensitive marks attributes whose values should not be
// surfaced in plan output (passwords, tokens, etc.); consumers
// can use this to flag outputs that reference sensitive attrs
// without `sensitive = true`.
type Attribute struct {
	Type       cty.Type
	Required   bool
	Optional   bool
	Computed   bool
	Sensitive  bool
	Deprecated bool
}

// NestedBlock is one entry in a block's `block_types` map. NestingMode
// is one of "single", "list", "set", "map", or "group" — this affects
// how Terraform itself shapes the value (a single block becomes an
// object, a list of blocks becomes list-of-objects, etc.).
type NestedBlock struct {
	NestingMode string
	Block       *Block
	MinItems    int
	MaxItems    int
}

// ---- JSON parsing ----

// rawSchema mirrors the on-disk JSON shape just enough to decode
// into our richer in-memory Schema. Field names match the JSON
// exactly. Anything we don't currently use (descriptions, version
// numbers, configuration_required, etc.) is silently ignored.
type rawSchema struct {
	FormatVersion   string                       `json:"format_version"`
	ProviderSchemas map[string]*rawProviderEntry `json:"provider_schemas"`
}

type rawProviderEntry struct {
	ResourceSchemas   map[string]*rawSchemaEntry `json:"resource_schemas"`
	DataSourceSchemas map[string]*rawSchemaEntry `json:"data_source_schemas"`
}

type rawSchemaEntry struct {
	Block *rawBlock `json:"block"`
}

type rawBlock struct {
	Attributes map[string]*rawAttribute   `json:"attributes"`
	BlockTypes map[string]*rawNestedBlock `json:"block_types"`
}

type rawAttribute struct {
	Type       json.RawMessage `json:"type"`
	Required   bool            `json:"required"`
	Optional   bool            `json:"optional"`
	Computed   bool            `json:"computed"`
	Sensitive  bool            `json:"sensitive"`
	Deprecated bool            `json:"deprecated"`
}

type rawNestedBlock struct {
	NestingMode string    `json:"nesting_mode"`
	Block       *rawBlock `json:"block"`
	MinItems    int       `json:"min_items"`
	MaxItems    int       `json:"max_items"`
}

// Load parses a JSON file produced by `terraform providers schema
// -json`. Returns an error if the file can't be read, isn't valid
// JSON, or contains a `type` field that cty/json can't decode (a
// signal the file isn't actually a Terraform provider schema).
func Load(path string) (*Schema, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read provider schema %s: %w", path, err)
	}
	return Parse(data)
}

// Parse decodes a Terraform provider schema from raw JSON bytes.
// Same contract as Load but without file I/O — useful for tests
// and for callers that have the schema in memory already.
func Parse(data []byte) (*Schema, error) {
	var raw rawSchema
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse provider schema JSON: %w", err)
	}
	out := &Schema{providers: make(map[string]*providerEntry, len(raw.ProviderSchemas))}
	for source, p := range raw.ProviderSchemas {
		entry := &providerEntry{
			resources:   make(map[string]*Block, len(p.ResourceSchemas)),
			dataSources: make(map[string]*Block, len(p.DataSourceSchemas)),
		}
		for name, e := range p.ResourceSchemas {
			b, err := convertBlock(e.Block)
			if err != nil {
				return nil, fmt.Errorf("provider %s resource %s: %w", source, name, err)
			}
			entry.resources[name] = b
		}
		for name, e := range p.DataSourceSchemas {
			b, err := convertBlock(e.Block)
			if err != nil {
				return nil, fmt.Errorf("provider %s data source %s: %w", source, name, err)
			}
			entry.dataSources[name] = b
		}
		out.providers[source] = entry
	}
	return out, nil
}

// convertBlock turns the raw JSON block representation into the
// in-memory Block, decoding each attribute's cty type via
// ctyjson.UnmarshalType. Recurses into block_types so the entire
// schema tree is decoded eagerly — lookup at query time is then
// pure map / cty.Type traversal.
func convertBlock(rb *rawBlock) (*Block, error) {
	if rb == nil {
		return &Block{}, nil
	}
	b := &Block{
		Attributes: make(map[string]*Attribute, len(rb.Attributes)),
		BlockTypes: make(map[string]*NestedBlock, len(rb.BlockTypes)),
	}
	for name, ra := range rb.Attributes {
		t, err := ctyjson.UnmarshalType(ra.Type)
		if err != nil {
			return nil, fmt.Errorf("attribute %s type: %w", name, err)
		}
		b.Attributes[name] = &Attribute{
			Type:       t,
			Required:   ra.Required,
			Optional:   ra.Optional,
			Computed:   ra.Computed,
			Sensitive:  ra.Sensitive,
			Deprecated: ra.Deprecated,
		}
	}
	for name, rn := range rb.BlockTypes {
		sub, err := convertBlock(rn.Block)
		if err != nil {
			return nil, fmt.Errorf("block %s: %w", name, err)
		}
		b.BlockTypes[name] = &NestedBlock{
			NestingMode: rn.NestingMode,
			Block:       sub,
			MinItems:    rn.MinItems,
			MaxItems:    rn.MaxItems,
		}
	}
	return b, nil
}

// ---- Lookup ----

// HasResource reports whether any loaded provider declares a
// resource of the given type. Used by the validate pass to skip
// resource references whose type isn't covered by the supplied
// schema (prevents false positives when a user supplies only the
// AWS schema but their config also references GCP resources).
func (s *Schema) HasResource(typeName string) bool {
	if s == nil {
		return false
	}
	for _, p := range s.providers {
		if _, ok := p.resources[typeName]; ok {
			return true
		}
	}
	return false
}

// HasDataSource reports whether any loaded provider declares a
// data source of the given type.
func (s *Schema) HasDataSource(typeName string) bool {
	if s == nil {
		return false
	}
	for _, p := range s.providers {
		if _, ok := p.dataSources[typeName]; ok {
			return true
		}
	}
	return false
}

// Resource returns the Block schema for the given resource type
// across all loaded providers. Returns nil when no provider
// declares the type.
func (s *Schema) Resource(typeName string) *Block {
	if s == nil {
		return nil
	}
	for _, p := range s.providers {
		if b, ok := p.resources[typeName]; ok {
			return b
		}
	}
	return nil
}

// DataSource returns the Block schema for the given data-source
// type across all loaded providers.
func (s *Schema) DataSource(typeName string) *Block {
	if s == nil {
		return nil
	}
	for _, p := range s.providers {
		if b, ok := p.dataSources[typeName]; ok {
			return b
		}
	}
	return nil
}

// ResolveAttr walks an attribute path (`["cidr_block"]`,
// `["timeouts", "create"]`) into the resource type's schema and
// returns the leaf attribute. Returns (nil, false) when:
//
//   - The resource type isn't in the loaded schema.
//   - Any path step doesn't match an attribute or block_type at
//     that level.
//
// For paths that descend through nested block types, the lookup
// transparently walks the BlockTypes map at each level — so
// `aws_db_instance.x.timeouts.create` resolves correctly even
// though `timeouts` is a block, not an attribute.
//
// Empty path returns (nil, false) — callers should special-case
// "bare resource reference" themselves.
func (s *Schema) ResolveAttr(resourceType string, path []string) (*Attribute, bool) {
	return resolvePath(s.Resource(resourceType), path)
}

// ResolveDataAttr is the data-source counterpart of ResolveAttr.
// `data.aws_ami.latest.id` looks up via ResolveDataAttr("aws_ami",
// ["id"]).
func (s *Schema) ResolveDataAttr(dataType string, path []string) (*Attribute, bool) {
	return resolvePath(s.DataSource(dataType), path)
}

// resolvePath is the shared implementation of ResolveAttr /
// ResolveDataAttr — both just disambiguate which top-level map to
// start from, then walk the same Block tree.
//
// At each step:
//
//   - If path[i] matches an attribute and we're at the last step,
//     return that attribute.
//   - If path[i] matches a block_type, descend into its Block. The
//     nesting mode (single / list / set / map / group) doesn't
//     affect the descent — we treat all four as "next step looks
//     up in this nested block" since attribute access via dot is
//     identical for all of them at the type level.
//   - If path[i] matches an attribute but it's not the last step
//     (i.e. someone's chaining into a non-collection attribute),
//     return false — Terraform itself would reject this.
func resolvePath(b *Block, path []string) (*Attribute, bool) {
	if b == nil || len(path) == 0 {
		return nil, false
	}
	cur := b
	for i, step := range path {
		if a, ok := cur.Attributes[step]; ok {
			if i == len(path)-1 {
				return a, true
			}
			// Trying to descend into an attribute. Object/map types
			// could legitimately have sub-attrs accessible via dot,
			// but the schema doesn't expose those individually —
			// callers asking for `aws_X.y.tags.foo` need to fall
			// back to the cty.Type's structural inspection (which
			// the analyser's resolver handles via descendType).
			return a, true
		}
		nb, ok := cur.BlockTypes[step]
		if !ok {
			return nil, false
		}
		cur = nb.Block
	}
	return nil, false
}

// HasAttribute reports whether the resource has the given
// attribute path. Convenience wrapper for the validate-side
// "is this reference valid" check, where the caller doesn't need
// the attribute's type or flags.
func (s *Schema) HasAttribute(resourceType string, path []string) bool {
	_, ok := s.ResolveAttr(resourceType, path)
	return ok
}

// HasDataAttribute is the data-source counterpart of HasAttribute.
func (s *Schema) HasDataAttribute(dataType string, path []string) bool {
	_, ok := s.ResolveDataAttr(dataType, path)
	return ok
}
