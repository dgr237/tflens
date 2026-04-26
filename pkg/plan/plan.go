// Package plan loads and inspects the JSON shape produced by
// `terraform show -json <plan>` (or `terraform plan -json` when piped
// through `jq -s '.[] | select(.type == "planned_change")'`-style
// extraction). Used by `tflens diff --enrich-with-plan plan.json` to
// fold attribute-level deltas into the source-side breaking-change
// detection — bridging the static-analyser / plan-analyser gap that
// otherwise leaves attribute changes (`cidr_block = "10.0.0.0/16"` →
// `"10.1.0.0/16"`) invisible to tflens because we don't embed
// provider schemas.
//
// Format reference: https://developer.hashicorp.com/terraform/internals/json-format
//
// Supported format versions: 1.x. Terraform 1.x has used "1.0" / "1.1"
// / "1.2" — the loader is permissive on minor versions and rejects
// anything outside the 1.x major.
package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
)

// Plan is the top-level wire shape. We only model the fields tflens
// uses; unknown fields are silently dropped by encoding/json.
type Plan struct {
	FormatVersion    string           `json:"format_version"`
	TerraformVersion string           `json:"terraform_version"`
	ResourceChanges  []ResourceChange `json:"resource_changes"`
}

// ResourceChange is one entry under resource_changes[]. Address is
// the full Terraform-style identifier (e.g. "module.network.aws_vpc.main[0]");
// ModuleAddress / Type / Name / Index are pre-parsed convenience
// fields the loader populates from Address. Mode distinguishes
// managed resources from data sources.
type ResourceChange struct {
	Address       string `json:"address"`
	ModuleAddress string `json:"module_address,omitempty"`
	Mode          string `json:"mode"` // "managed" or "data"
	Type          string `json:"type"`
	Name          string `json:"name"`
	// Index carries the count/for_each index when the resource has one.
	// Terraform emits an int for count, a string for for_each, or omits
	// the field for non-iterating resources.
	Index any `json:"index,omitempty"`
	// Change is the actual diff payload — actions list + before/after
	// values + force-new path markers.
	Change ChangeSet `json:"change"`
}

// ChangeSet captures the per-resource change. Actions describes the
// operation kind; before/after carry the full attribute trees;
// ReplacePaths lists the attribute paths whose modification triggers
// destroy + recreate (i.e. force-new attributes).
type ChangeSet struct {
	// Actions is one of:
	//   ["no-op"]                — no change
	//   ["create"]               — new resource
	//   ["read"]                 — data-source-only refresh
	//   ["update"]               — in-place attribute change
	//   ["delete"]               — removal
	//   ["delete", "create"]     — destroy + recreate (replace)
	//   ["create", "delete"]     — create-before-destroy replace
	Actions []string `json:"actions"`
	// Before is the attribute tree as it currently exists. nil for
	// pure creates.
	Before json.RawMessage `json:"before"`
	// After is the attribute tree the plan will produce. nil for
	// pure deletes; may have placeholder values where After is
	// "(known after apply)" — those attributes appear in AfterUnknown.
	After json.RawMessage `json:"after"`
	// ReplacePaths is the list of attribute paths whose modification
	// triggers destroy+recreate. Each path is a list of (string | int)
	// steps (e.g. [["lifecycle", "0", "ignore_changes"]] or
	// [["cidr_block"]]).
	ReplacePaths [][]any `json:"replace_paths,omitempty"`
	// BeforeSensitive / AfterSensitive shadow Before / After with
	// boolean leaves indicating which attribute paths are sensitive.
	// Terraform marks anything that flowed through a sensitive
	// variable, a sensitive output, or a `sensitive = true` resource
	// attribute. AttrDeltas consults these to redact values before
	// they reach the renderer — without redaction a `tflens diff
	// --enrich-with-plan` against a plan touching e.g. an RDS
	// password would print the password into CI logs.
	BeforeSensitive json.RawMessage `json:"before_sensitive,omitempty"`
	AfterSensitive  json.RawMessage `json:"after_sensitive,omitempty"`
	// AfterUnknown shadows After with boolean leaves indicating which
	// attribute values are still `(known after apply)` — i.e.
	// computed by the provider during apply and not present in the
	// plan. Without this we'd render those attributes as nil, which
	// looks like the attribute is being unset rather than just
	// pending.
	AfterUnknown json.RawMessage `json:"after_unknown,omitempty"`
}

// IsNoOp reports whether the change is a refresh-only entry (Actions
// is exactly ["no-op"]). Filtered out before enrichment so the diff
// doesn't get a stream of empty rows for unchanged resources.
func (c ChangeSet) IsNoOp() bool {
	return len(c.Actions) == 1 && c.Actions[0] == "no-op"
}

// IsReplace reports whether the change destroys and recreates the
// resource. Both ordering variants ("delete" + "create" and "create"
// + "delete") count.
func (c ChangeSet) IsReplace() bool {
	if len(c.Actions) != 2 {
		return false
	}
	a, b := c.Actions[0], c.Actions[1]
	return (a == "delete" && b == "create") || (a == "create" && b == "delete")
}

// IsCreate reports whether the resource is being created fresh
// (Actions == ["create"]).
func (c ChangeSet) IsCreate() bool {
	return len(c.Actions) == 1 && c.Actions[0] == "create"
}

// IsDelete reports whether the resource is being deleted (Actions ==
// ["delete"]). Excludes the replace cases — those are IsReplace.
func (c ChangeSet) IsDelete() bool {
	return len(c.Actions) == 1 && c.Actions[0] == "delete"
}

// IsUpdate reports whether the resource is being updated in place
// (Actions == ["update"]). Excludes replace cases.
func (c ChangeSet) IsUpdate() bool {
	return len(c.Actions) == 1 && c.Actions[0] == "update"
}

// AttrDelta is one attribute-level difference between the before and
// after states. Path is the dot-separated attribute path
// (`tags.Name`, `cidr_block`, `vpc_config.0.subnet_ids`); Before /
// After are the JSON-decoded values; ForceNew is true when this
// path appears in the parent ChangeSet's ReplacePaths.
//
// BeforeSensitive / AfterSensitive mark whether the value at that
// path was tagged sensitive in the corresponding sensitive-shadow
// tree. Renderers must redact the value (print "(sensitive)" instead
// of the raw before/after) when the corresponding flag is set.
//
// AfterUnknown marks a value that's still `(known after apply)` —
// the provider will fill it in during apply, so the plan's After is
// just a placeholder (typically null) rather than a real value being
// written. Renderers should print "(known after apply)" instead of
// the raw After in that case.
type AttrDelta struct {
	Path            string
	Before          any
	After           any
	ForceNew        bool
	BeforeSensitive bool
	AfterSensitive  bool
	AfterUnknown    bool
}

// AttrDeltas walks the before/after attribute trees and returns the
// flat list of differences. Recursion handles nested maps/lists;
// equal subtrees are skipped. The order of returned deltas follows
// alphabetical Path within each level.
//
// nil before (pure create) → every after attribute becomes a delta
// with Before=nil. nil after (pure delete) → every before attribute
// becomes a delta with After=nil. The IsNoOp short-circuit upstream
// avoids calling AttrDeltas on no-op changes.
//
// Sensitive-shadow trees (BeforeSensitive / AfterSensitive) and the
// AfterUnknown shadow are walked alongside the data. At each leaf,
// the corresponding bool from the shadow surfaces as a flag on the
// emitted AttrDelta. Subtree-wide markers (a `true` in the shadow
// at a non-leaf position) emit a single AttrDelta at that level
// rather than descending — there's no per-leaf data to compare when
// the entire subtree is opaque.
func (c ChangeSet) AttrDeltas() []AttrDelta {
	var before, after any
	if len(c.Before) > 0 && string(c.Before) != "null" {
		_ = json.Unmarshal(c.Before, &before)
	}
	if len(c.After) > 0 && string(c.After) != "null" {
		_ = json.Unmarshal(c.After, &after)
	}
	beforeSens := decodeShadow(c.BeforeSensitive)
	afterSens := decodeShadow(c.AfterSensitive)
	afterUnk := decodeShadow(c.AfterUnknown)
	forceNew := pathSet(c.ReplacePaths)
	var out []AttrDelta
	walkDelta(before, after, beforeSens, afterSens, afterUnk, "", forceNew, &out)
	return out
}

// decodeShadow JSON-decodes a sensitive/unknown shadow tree. Returns
// nil when the shadow is empty or the literal "null" — a missing
// shadow means "nothing in this subtree is sensitive/unknown" rather
// than some kind of error.
func decodeShadow(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var v any
	_ = json.Unmarshal(raw, &v)
	return v
}

// pathSet flattens the [][]any ReplacePaths into a string set keyed
// by the same dot-joined Path form AttrDelta uses. Avoids quadratic
// scans during walkDelta.
func pathSet(paths [][]any) map[string]bool {
	out := map[string]bool{}
	for _, p := range paths {
		out[joinPath(p)] = true
	}
	return out
}

// joinPath converts a Terraform path ([]any of string | float64) into
// the dot-joined form ("vpc_config.0.subnet_ids"). Numeric steps
// are truncated to int (json.Unmarshal gives float64 by default).
func joinPath(steps []any) string {
	parts := make([]string, len(steps))
	for i, s := range steps {
		switch v := s.(type) {
		case string:
			parts[i] = v
		case float64:
			parts[i] = strconv.Itoa(int(v))
		case int:
			parts[i] = strconv.Itoa(v)
		default:
			parts[i] = fmt.Sprintf("%v", v)
		}
	}
	return strings.Join(parts, ".")
}

// walkDelta recursively compares before and after, appending an
// AttrDelta for every leaf or container difference. The path
// accumulator starts empty and grows with "key" / "<index>" segments
// as recursion descends. Force-new flag is looked up by the final
// dot-joined path string.
//
// The three shadow trees (beforeSens / afterSens / afterUnk) are
// drilled in parallel: at each step shadowKey/shadowIdx returns the
// matching child shadow, which may be a bool (subtree-wide marker),
// a map / list (continue drilling), or nil (no marker). When a
// shadow is `true` at the current level we stop descending and emit
// a single AttrDelta carrying the marker — terraform represents a
// fully sensitive/unknown subtree this way (the data side is opaque
// or empty), so there's no inner content worth diffing.
func walkDelta(before, after, beforeSens, afterSens, afterUnk any,
	path string, forceNew map[string]bool, out *[]AttrDelta) {
	beforeSensBool := isShadowTrue(beforeSens)
	afterSensBool := isShadowTrue(afterSens)
	afterUnkBool := isShadowTrue(afterUnk)
	// A shadow `true` at a structured (map/list) position is terraform
	// saying "the whole subtree is sensitive/unknown" — the inner
	// contents are either elided or just placeholder values. Emit one
	// leaf here carrying the marker rather than descending and
	// generating a spray of opaque per-attribute deltas.
	shadowMasksSubtree := (afterUnkBool || afterSensBool || beforeSensBool) &&
		(hasStructuralChildren(before) || hasStructuralChildren(after))
	if shadowMasksSubtree {
		// Sensitive markers alone don't constitute a change — only emit
		// when the data side actually differs OR the after is unknown
		// (since unknown means a write is happening, even if before/
		// after compare equal because both are placeholder nulls).
		if reflect.DeepEqual(before, after) && !afterUnkBool {
			return
		}
		*out = append(*out, AttrDelta{
			Path:            path,
			Before:          before,
			After:           after,
			ForceNew:        forceNew[path],
			BeforeSensitive: beforeSensBool,
			AfterSensitive:  afterSensBool,
			AfterUnknown:    afterUnkBool,
		})
		return
	}

	// Equal — nothing to emit; recursion stops here. Exception: if
	// after-is-unknown, we still want to surface the change (the
	// values look equal because both are placeholder nulls, but the
	// provider IS going to compute a new value).
	if reflect.DeepEqual(before, after) && !afterUnkBool {
		return
	}
	bm, bIsMap := before.(map[string]any)
	am, aIsMap := after.(map[string]any)
	if bIsMap || aIsMap {
		// Either side is a map → recurse into the union of keys.
		// Sorted iteration keeps the output deterministic.
		keys := mergeMapKeys(bm, am)
		for _, k := range keys {
			child := path
			if child == "" {
				child = k
			} else {
				child = child + "." + k
			}
			walkDelta(bm[k], am[k],
				shadowKey(beforeSens, k), shadowKey(afterSens, k), shadowKey(afterUnk, k),
				child, forceNew, out)
		}
		return
	}
	bs, bIsSlice := before.([]any)
	as, aIsSlice := after.([]any)
	if bIsSlice || aIsSlice {
		// Lists: recurse positionally up to the longer side. Index
		// becomes a string segment to match Terraform's path encoding.
		n := max(len(bs), len(as))
		for i := range n {
			var bv, av any
			if i < len(bs) {
				bv = bs[i]
			}
			if i < len(as) {
				av = as[i]
			}
			child := path
			idx := strconv.Itoa(i)
			if child == "" {
				child = idx
			} else {
				child = child + "." + idx
			}
			walkDelta(bv, av,
				shadowIdx(beforeSens, i), shadowIdx(afterSens, i), shadowIdx(afterUnk, i),
				child, forceNew, out)
		}
		return
	}
	// Leaf — emit the delta. Force-new lookup uses the final path;
	// shadow flags carry whatever sensitivity / unknown markers
	// applied at this exact path.
	*out = append(*out, AttrDelta{
		Path:            path,
		Before:          before,
		After:           after,
		ForceNew:        forceNew[path],
		BeforeSensitive: beforeSensBool,
		AfterSensitive:  afterSensBool,
		AfterUnknown:    afterUnkBool,
	})
}

// isShadowTrue returns true when a shadow value is the literal `true`
// — i.e. "this entire subtree is sensitive/unknown". A nested map or
// list shadow returns false (the markers are deeper); nil returns
// false (no marker).
func isShadowTrue(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

// shadowKey looks up a key in a map-shaped shadow value. Returns nil
// when the shadow isn't a map or doesn't have the key — both mean
// "no marker at this child".
func shadowKey(shadow any, k string) any {
	m, ok := shadow.(map[string]any)
	if !ok {
		return nil
	}
	return m[k]
}

// shadowIdx looks up an index in a list-shaped shadow value. Returns
// nil when the shadow isn't a list or the index is out of bounds.
func shadowIdx(shadow any, i int) any {
	s, ok := shadow.([]any)
	if !ok {
		return nil
	}
	if i < 0 || i >= len(s) {
		return nil
	}
	return s[i]
}

// hasStructuralChildren reports whether a value is a non-empty map or
// list — i.e. has children we could descend into. Used to decide
// whether a sensitive marker at this level should mask the subtree
// (no children to descend, so emit one leaf) or be inherited as the
// caller drills into the children themselves (which is the normal
// per-leaf path).
func hasStructuralChildren(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		return len(x) > 0
	case []any:
		return len(x) > 0
	}
	return false
}

// mergeMapKeys returns the sorted union of keys in two maps. Used by
// walkDelta to iterate before/after deterministically.
func mergeMapKeys(a, b map[string]any) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	// Inline sort — small slices, no need to import sort.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Load reads + parses a terraform plan JSON file. Validates the
// format_version is in the 1.x major series; errors otherwise so
// callers don't silently use a plan they can't interpret. Also
// post-parses each ResourceChange's Address into ModuleAddress /
// Type / Name / Index when those fields aren't already populated
// (older Terraform versions emit address-only).
func Load(path string) (*Plan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plan %s: %w", path, err)
	}
	var p Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parse plan %s: %w", path, err)
	}
	if !strings.HasPrefix(p.FormatVersion, "1.") {
		return nil, fmt.Errorf("unsupported plan format_version %q (need 1.x); rerun with a Terraform 1.x plan",
			p.FormatVersion)
	}
	for i := range p.ResourceChanges {
		// Always run parseAddress to fill any field the JSON omitted —
		// Terraform's older versions inconsistently include module_address
		// even when type/name are present, and parseAddress is idempotent
		// (won't overwrite already-populated fields).
		parseAddress(&p.ResourceChanges[i])
	}
	return &p, nil
}

// parseAddress is the fallback that fills ModuleAddress / Type / Name
// / Index from Address for plan dialects that emit only the address
// string. The grammar we support is:
//
//	[module.X[.Y...]] [data.] type.name [ "[" index "]" ]
//
// Index may be a bare integer (count) or a quoted string (for_each
// key). Bracketed indices on module segments (rare) are tolerated
// but kept inside ModuleAddress as a string.
func parseAddress(rc *ResourceChange) {
	addr := rc.Address
	// Split off the optional module prefix. Always populate
	// ModuleAddress when missing (Terraform sometimes omits it on
	// non-default-module resources in older format versions).
	if strings.HasPrefix(addr, "module.") {
		parts := strings.Split(addr, ".")
		modParts := []string{}
		i := 0
		for i+1 < len(parts) && parts[i] == "module" {
			modParts = append(modParts, parts[i], parts[i+1])
			i += 2
		}
		if rc.ModuleAddress == "" {
			rc.ModuleAddress = strings.Join(modParts, ".")
		}
		addr = strings.Join(parts[i:], ".")
	}
	// data. prefix marks data sources. Honour an explicit Mode if
	// JSON already set one (don't downgrade managed to data on a
	// JSON shape that disagrees with the address).
	if strings.HasPrefix(addr, "data.") {
		if rc.Mode == "" {
			rc.Mode = "data"
		}
		addr = addr[len("data."):]
	} else if rc.Mode == "" {
		rc.Mode = "managed"
	}
	// Strip trailing [index]. Only set Index when JSON didn't
	// already populate it.
	if i := strings.LastIndex(addr, "["); i > 0 && strings.HasSuffix(addr, "]") {
		idxStr := addr[i+1 : len(addr)-1]
		addr = addr[:i]
		if rc.Index == nil {
			if len(idxStr) >= 2 && idxStr[0] == '"' && idxStr[len(idxStr)-1] == '"' {
				rc.Index = idxStr[1 : len(idxStr)-1]
			} else if n, err := strconv.Atoi(idxStr); err == nil {
				rc.Index = n
			} else {
				rc.Index = idxStr
			}
		}
	}
	// What's left is `type.name`. Don't overwrite if JSON already
	// populated.
	if dot := strings.Index(addr, "."); dot > 0 {
		if rc.Type == "" {
			rc.Type = addr[:dot]
		}
		if rc.Name == "" {
			rc.Name = addr[dot+1:]
		}
	}
}

// EntityID returns the canonical pkg/analysis Entity ID for this
// resource_change ("resource.<type>.<name>" or "data.<type>.<name>").
// Used by the diff enrichment to look up the matching source-side
// entity.
//
// Note: this does NOT include the module prefix or the count/for_each
// index. Module matching happens at the project-tree level (one
// Plan resource_change can match exactly one Module's entity);
// indexed instances aren't yet supported at the matching layer (the
// first matching resource is enriched, regardless of which instance
// the plan describes).
func (rc *ResourceChange) EntityID() string {
	kind := "resource"
	if rc.Mode == "data" {
		kind = "data"
	}
	return fmt.Sprintf("%s.%s.%s", kind, rc.Type, rc.Name)
}
