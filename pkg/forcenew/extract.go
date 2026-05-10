package forcenew

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// SchemaVersion is the on-disk shape's version label. Bumped when the
// table's wire format changes incompatibly. Embedded into every Table
// emitted by WriteJSON so consumers can detect format drift.
const SchemaVersion = "0.1"

// Extract reads a Crossplane runtime IR JSON stream and produces the
// minimal force-new Table tflens uses for breaking-change
// classification. Source is the provenance label baked into the
// output (e.g. "crossplane-runtime-ir-v2.5.0-9fb84fc37179").
//
// Two sources contribute paths:
//
//  1. specSchema entries with forceNew: true — walked recursively
//     across nested children, paths joined from each node's tfName.
//  2. externalName.identifierFields and externalName.omittedFields —
//     fields Crossplane elides from specSchema and routes through
//     metadata.annotations[crossplane.io/external-name]. Including
//     these closes the gap on identity attributes like
//     aws_eks_cluster.name (ForceNew in the underlying TF provider
//     but absent from specSchema).
//
// Filters: "region" (Crossplane-only modeling, not a TF resource
// attribute), empty strings, and the bare token "_prefix" (malformed
// IR entries) are dropped.
func Extract(r io.Reader, source string) (*Table, error) {
	var entries []irEntry
	if err := json.NewDecoder(r).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode IR: %w", err)
	}
	t := &Table{
		Source:    source,
		Resources: make(map[string][][]string, len(entries)),
	}
	for _, e := range entries {
		if e.TerraformType == "" {
			continue
		}
		var paths [][]string
		for _, attr := range e.SpecSchema {
			walkSchema(attr, nil, &paths)
		}
		paths = mergeExternalNamePaths(paths, e.ExternalName)
		if len(paths) == 0 {
			continue
		}
		sort.Slice(paths, func(i, j int) bool {
			return strings.Join(paths[i], ".") < strings.Join(paths[j], ".")
		})
		// Multiple IR entries can share the same terraformType (observed:
		// versioned variants — e.g. two aws_eks_cluster entries with
		// identical force-new sets). Last-write-wins is fine because the
		// sets are equivalent in practice; if a future IR diverges them,
		// the extractor refresh PR diff will surface it.
		t.Resources[e.TerraformType] = paths
	}
	return t, nil
}

// WriteJSON marshals t as pretty-printed JSON suitable for embedding
// or PR review. ExtractedAt is stamped at write time; SchemaVersion
// is the package constant.
func WriteJSON(w io.Writer, t *Table) error {
	wire := tableToWire(t)
	wire.SchemaVersion = SchemaVersion
	wire.ExtractedAt = time.Now().UTC().Format(time.RFC3339)
	pretty, err := json.MarshalIndent(wire, "", "  ")
	if err != nil {
		return err
	}
	pretty = append(pretty, '\n')
	_, err = w.Write(pretty)
	return err
}

// ReadJSON parses a Table previously produced by WriteJSON (or a
// hand-edited override file with the same shape). Unknown fields in
// the JSON are tolerated so the on-disk shape can grow additively
// without breaking older binaries.
func ReadJSON(r io.Reader) (*Table, error) {
	var wire tableWire
	if err := json.NewDecoder(r).Decode(&wire); err != nil {
		return nil, fmt.Errorf("parse force-new table: %w", err)
	}
	return tableFromWire(&wire), nil
}

// Merge unions src's resources into t in place. For overlapping
// resource types, paths are deduplicated and joined; src never
// removes embedded paths. New resource types are added. Use this to
// apply user-supplied overrides over the embedded table.
//
// No-op when src is nil or has no resources.
func (t *Table) Merge(src *Table) {
	if src == nil || len(src.Resources) == 0 {
		return
	}
	for tfType, srcPaths := range src.Resources {
		t.Resources[tfType] = unionPaths(t.Resources[tfType], srcPaths)
	}
}

// unionPaths merges b into a, deduplicating on dotted-path equality
// (since two paths that differ only in slice-vs-slice identity are
// semantically the same). Order is sorted ascending for deterministic
// output.
func unionPaths(a, b [][]string) [][]string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([][]string, 0, len(a)+len(b))
	add := func(p []string) {
		key := strings.Join(p, "\x00")
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	for _, p := range a {
		add(p)
	}
	for _, p := range b {
		add(p)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.Join(out[i], ".") < strings.Join(out[j], ".")
	})
	return out
}

// ---- IR walk helpers ----

// irEntry is one resource in the runtime IR. Only the fields Extract
// uses are decoded; everything else (kind, shortGroup, fieldMapping,
// statusSchema, …) is left out so we aren't coupled to fields that
// drift between Crossplane versions.
type irEntry struct {
	TerraformType string                     `json:"terraformType"`
	SpecSchema    map[string]json.RawMessage `json:"specSchema"`
	ExternalName  *irExternalName            `json:"externalName,omitempty"`
}

type irExternalName struct {
	IdentifierFields []string `json:"identifierFields"`
	OmittedFields    []string `json:"omittedFields"`
}

// irAttr is one attribute node in specSchema. Children is RawMessage
// because the IR sometimes wraps a child in a [a, a]-shaped array
// (versioned variants); walkSchema handles both encodings.
type irAttr struct {
	TfName   string                     `json:"tfName"`
	ForceNew bool                       `json:"forceNew"`
	Children map[string]json.RawMessage `json:"children,omitempty"`
}

// walkSchema descends one IR node, recording any forceNew path it
// finds. The path is built from each node's tfName (NOT the parent
// map key), so we're robust to whether the IR keys children by
// tfName or camelCase — observed: top-level uses tfName, but
// children's keying has varied across upjet versions.
func walkSchema(raw json.RawMessage, prefix []string, out *[][]string) {
	// IR variant: some entries arrive as duplicated [attr, attr] arrays.
	// The two entries are equivalent in every observed case; take the
	// first and move on.
	var arr []irAttr
	if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
		walkAttr(arr[0], prefix, out)
		return
	}
	var single irAttr
	if json.Unmarshal(raw, &single) == nil {
		walkAttr(single, prefix, out)
	}
}

func walkAttr(a irAttr, prefix []string, out *[][]string) {
	if a.TfName == "" {
		return
	}
	path := append(append([]string(nil), prefix...), a.TfName)
	if a.ForceNew {
		*out = append(*out, path)
	}
	for _, child := range a.Children {
		walkSchema(child, path, out)
	}
}

// mergeExternalNamePaths unions Crossplane's identifierFields /
// omittedFields into the spec-walk paths, treating each as a top-level
// (single-segment) Terraform attribute that's force-new.
//
// Filters:
//   - "region" appears in every IR entry's identifierFields (2008/2008)
//     as Crossplane's per-resource region modeling, but it's not a
//     Terraform resource attribute — region comes from the provider in
//     HCL. Including it would never match anything tflens sees in a
//     resource diff.
//   - Empty strings and the bare token "_prefix" appear ~90× across the
//     IR as malformed entries — skip both.
//   - Dedup against existing paths so a field present in both
//     spec_schema (with forceNew) and externalName.identifierFields
//     doesn't appear twice.
//
// All external-name paths are single-segment because Crossplane only
// elides top-level fields — nested attributes stay in spec_schema.
func mergeExternalNamePaths(paths [][]string, ext *irExternalName) [][]string {
	if ext == nil {
		return paths
	}
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		seen[strings.Join(p, "\x00")] = struct{}{}
	}
	add := func(name string) {
		if name == "" || name == "_prefix" || name == "region" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		paths = append(paths, []string{name})
	}
	for _, f := range ext.IdentifierFields {
		add(f)
	}
	for _, f := range ext.OmittedFields {
		add(f)
	}
	return paths
}

// ---- wire format ----

// tableWire is the on-disk JSON shape. Keeping it separate from Table
// (the in-memory shape) lets callers work with the convenient
// resource-type → paths map directly without dealing with the
// resourceWire wrapping.
type tableWire struct {
	SchemaVersion string                  `json:"schema_version"`
	Source        string                  `json:"source"`
	ExtractedAt   string                  `json:"extracted_at,omitempty"`
	Resources     map[string]resourceWire `json:"resources"`
}

type resourceWire struct {
	ForceNewPaths [][]string `json:"force_new_paths,omitempty"`
}

func tableToWire(t *Table) *tableWire {
	if t == nil {
		return &tableWire{Resources: map[string]resourceWire{}}
	}
	w := &tableWire{
		Source:    t.Source,
		Resources: make(map[string]resourceWire, len(t.Resources)),
	}
	for k, paths := range t.Resources {
		w.Resources[k] = resourceWire{ForceNewPaths: paths}
	}
	return w
}

func tableFromWire(w *tableWire) *Table {
	if w == nil {
		return &Table{Resources: map[string][][]string{}}
	}
	t := &Table{
		Source:    w.Source,
		Resources: make(map[string][][]string, len(w.Resources)),
	}
	for k, v := range w.Resources {
		t.Resources[k] = v.ForceNewPaths
	}
	return t
}
