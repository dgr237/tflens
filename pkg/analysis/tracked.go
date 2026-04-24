package analysis

import (
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/dgr237/tflens/pkg/token"
)

// trackMarkerPrefix is the comment prefix that opts an attribute into the
// tracked-change pass. Recognised in both `# tflens:track[: description]`
// and `// tflens:track[: description]` forms.
const trackMarkerPrefix = "tflens:track"

// TrackedAttribute is one attribute that the author has annotated with a
// `# tflens:track` comment. The diff pass treats any change to ExprText —
// or to the canonical text of any transitively-referenced variable default
// or local value — as a Breaking change. Removing the marker itself is
// also Breaking, so the safety guard cannot be silently stripped.
type TrackedAttribute struct {
	// EntityID is the canonical ID of the containing entity, e.g.
	// "resource.aws_eks_cluster.this". Combined with AttrName it forms a
	// stable key across versions.
	EntityID string
	// AttrName is the attribute's name within the entity, e.g.
	// "cluster_version". For nested blocks the path is dot-joined,
	// e.g. "engine_config.version".
	AttrName string
	// ExprText is the canonical (hclwrite-formatted) text of the
	// attribute's value expression — the literal change detector.
	ExprText string
	// Description is the free-text after `tflens:track:` (empty when the
	// marker has no description). Surfaced as the diff hint.
	Description string
	// Pos is the source position of the attribute's name (so error
	// messages point at the attribute, not the comment).
	Pos token.Position
	// Refs holds the canonical text of every variable default and local
	// value the expression transitively depends on, keyed by the
	// referenced entity's ID (e.g. "variable.cluster_version" or
	// "local.cluster_version"). Order does not matter for comparison;
	// diff iterates the sorted union of keys from old + new.
	Refs map[string]string

	// expr is the attribute's value expression, captured at collection
	// time. Used by resolveTrackedRefs to walk for var/local references;
	// not exported because consumers should compare ExprText/Refs.
	expr *Expr
}

// Key is the stable identifier used to pair tracked attributes across
// two versions of the same module.
func (t TrackedAttribute) Key() string { return t.EntityID + "." + t.AttrName }

// TrackedAttributes returns a copy of the module's tracked attributes
// in declaration order.
func (m *Module) TrackedAttributes() []TrackedAttribute {
	out := make([]TrackedAttribute, len(m.tracked))
	copy(out, m.tracked)
	return out
}

// trackMarker is one occurrence of a `tflens:track` comment, classified as
// trailing (same line as the attribute it annotates) or own-line (the
// attribute on the next line is annotated).
type trackMarker struct {
	line        int
	trailing    bool
	description string
}

// scanTrackMarkers lexes file source and returns every recognised
// `tflens:track` marker keyed by the line that the marker annotates:
//   - trailing marker on line L → attribute on line L
//   - own-line marker on line L → attribute on line L+1
//
// Multiple markers targeting the same line are unusual but tolerated —
// the last one wins (deterministic because hclsyntax tokens are in source
// order).
func scanTrackMarkers(src []byte, filename string) map[int]trackMarker {
	tokens, _ := hclsyntax.LexConfig(src, filename, hcl.Pos{Line: 1, Column: 1, Byte: 0})
	out := map[int]trackMarker{}
	lineHasCode := map[int]bool{}
	for _, tok := range tokens {
		line := tok.Range.Start.Line
		if tok.Type == hclsyntax.TokenComment {
			desc, ok := parseTrackComment(tok.Bytes)
			if !ok {
				continue
			}
			m := trackMarker{line: line, description: desc, trailing: lineHasCode[line]}
			if m.trailing {
				out[line] = m
			} else {
				out[line+1] = m
			}
			continue
		}
		if tok.Type == hclsyntax.TokenNewline || tok.Type == hclsyntax.TokenEOF {
			continue
		}
		lineHasCode[line] = true
	}
	return out
}

// parseTrackComment returns the trimmed description after `tflens:track:`
// (empty when the marker is bare) and reports whether the comment is in
// fact a track marker. Recognises both `#` and `//` forms with arbitrary
// surrounding whitespace.
func parseTrackComment(raw []byte) (string, bool) {
	s := strings.TrimRight(string(raw), "\r\n")
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "#"):
		s = strings.TrimSpace(s[1:])
	case strings.HasPrefix(s, "//"):
		s = strings.TrimSpace(s[2:])
	default:
		return "", false
	}
	if !strings.HasPrefix(s, trackMarkerPrefix) {
		return "", false
	}
	rest := strings.TrimPrefix(s, trackMarkerPrefix)
	switch {
	case rest == "":
		return "", true
	case strings.HasPrefix(rest, ":"):
		return strings.TrimSpace(rest[1:]), true
	default:
		// e.g. "tflens:tracking" — superficially matches but isn't us.
		return "", false
	}
}

// collectTrackedAttributes scans file for `tflens:track` markers and binds
// each one to the attribute it annotates, returning a flat list of
// TrackedAttribute records with Refs left empty (refs are resolved later,
// once every entity is known).
func collectTrackedAttributes(file *File) []TrackedAttribute {
	if file == nil || file.Body == nil {
		return nil
	}
	markers := scanTrackMarkers(file.Source, file.Filename)
	if len(markers) == 0 {
		return nil
	}
	var out []TrackedAttribute
	walkBodyForTracked(file.Body, "", "", file.Source, markers, &out)
	return out
}

// walkBodyForTracked recurses through body, collecting tracked attributes.
// entityID is the containing entity (resource/data/module/output/variable)
// — empty until we descend into one. attrPrefix is the dotted path of
// nested blocks within that entity.
func walkBodyForTracked(body *hclsyntax.Body, entityID, attrPrefix string, src []byte, markers map[int]trackMarker, out *[]TrackedAttribute) {
	for _, attr := range sortedAttrs(body) {
		if entityID == "" {
			continue
		}
		line := attr.NameRange.Start.Line
		mk, ok := markers[line]
		if !ok {
			continue
		}
		name := attr.Name
		if attrPrefix != "" {
			name = attrPrefix + "." + attr.Name
		}
		exprWrap := &Expr{E: attr.Expr, Source: src}
		*out = append(*out, TrackedAttribute{
			EntityID:    entityID,
			AttrName:    name,
			ExprText:    exprWrap.Text(),
			Description: mk.description,
			Pos:         posFromRange(attr.NameRange),
			expr:        exprWrap,
		})
	}
	for _, child := range body.Blocks {
		// `locals { }` is a flat container: each attribute is itself a
		// top-level entity (local.<name>). Bind markers attribute-by-
		// attribute rather than recursing — there's no enclosing entity
		// for a marker on the locals block to attach to.
		if entityID == "" && child.Type == "locals" && child.Body != nil {
			collectLocalsBlockMarkers(child.Body, src, markers, out)
			continue
		}
		nextEntity := entityID
		nextPrefix := attrPrefix
		if entityID == "" {
			nextEntity = topLevelEntityID(child)
			nextPrefix = ""
		} else {
			if nextPrefix == "" {
				nextPrefix = child.Type
			} else {
				nextPrefix = nextPrefix + "." + child.Type
			}
		}
		if child.Body != nil {
			walkBodyForTracked(child.Body, nextEntity, nextPrefix, src, markers, out)
		}
	}
}

// collectLocalsBlockMarkers binds markers to attributes inside a
// `locals { }` block. Each attribute is its own entity (local.<name>),
// so we record one TrackedAttribute per marked attribute with
// EntityID="local.<name>" and AttrName="value" — mirroring the
// output-block convention where the value expression is the thing
// being tracked.
func collectLocalsBlockMarkers(body *hclsyntax.Body, src []byte, markers map[int]trackMarker, out *[]TrackedAttribute) {
	for _, attr := range sortedAttrs(body) {
		line := attr.NameRange.Start.Line
		mk, ok := markers[line]
		if !ok {
			continue
		}
		exprWrap := &Expr{E: attr.Expr, Source: src}
		*out = append(*out, TrackedAttribute{
			EntityID:    (Entity{Kind: KindLocal, Name: attr.Name}).ID(),
			AttrName:    "value",
			ExprText:    exprWrap.Text(),
			Description: mk.description,
			Pos:         posFromRange(attr.NameRange),
			expr:        exprWrap,
		})
	}
}

// topLevelEntityID returns the canonical ID for a top-level Terraform
// block, or "" for blocks (locals/terraform/moved/removed) whose contents
// don't form a single entity that an attribute can hang off.
func topLevelEntityID(block *hclsyntax.Block) string {
	switch block.Type {
	case "resource":
		if len(block.Labels) == 2 {
			return (Entity{Kind: KindResource, Type: block.Labels[0], Name: block.Labels[1]}).ID()
		}
	case "data":
		if len(block.Labels) == 2 {
			return (Entity{Kind: KindData, Type: block.Labels[0], Name: block.Labels[1]}).ID()
		}
	case "module":
		if len(block.Labels) == 1 {
			return (Entity{Kind: KindModule, Name: block.Labels[0]}).ID()
		}
	case "output":
		if len(block.Labels) == 1 {
			return (Entity{Kind: KindOutput, Name: block.Labels[0]}).ID()
		}
	case "variable":
		if len(block.Labels) == 1 {
			return (Entity{Kind: KindVariable, Name: block.Labels[0]}).ID()
		}
	}
	return ""
}

// resolveTrackedRefs walks each tracked attribute's captured expression
// to find var.X / local.X references and records the canonical text of
// each referenced variable's default or local's value. Recurses through
// locals (which can themselves reference other locals/vars) with cycle
// protection. Variables don't transitively reference other vars in their
// default expression — Terraform forbids it — so we don't recurse past
// the variable's default.
func (m *Module) resolveTrackedRefs() {
	for i := range m.tracked {
		t := &m.tracked[i]
		if t.expr == nil || t.expr.E == nil {
			continue
		}
		t.Refs = map[string]string{}
		visited := map[string]bool{}
		m.gatherRefs(t.expr.E, t.Refs, visited, 0)
	}
}

// gatherRefs walks expr's variable references and records the canonical
// text of every var default and local value reached. depth caps recursion
// at 16 to bound surprise; visited prevents cycles between locals.
func (m *Module) gatherRefs(expr hclsyntax.Expression, refs map[string]string, visited map[string]bool, depth int) {
	if expr == nil || depth > 16 {
		return
	}
	for _, trav := range expr.Variables() {
		parts := traversalParts(trav)
		if len(parts) < 2 {
			continue
		}
		switch parts[0] {
		case "var":
			id := (Entity{Kind: KindVariable, Name: parts[1]}).ID()
			if visited[id] {
				continue
			}
			visited[id] = true
			v, ok := m.byID[id]
			if !ok {
				continue
			}
			refs[id] = textOrEmpty(v.DefaultExpr)
		case "local":
			id := (Entity{Kind: KindLocal, Name: parts[1]}).ID()
			if visited[id] {
				continue
			}
			visited[id] = true
			l, ok := m.byID[id]
			if !ok {
				continue
			}
			refs[id] = textOrEmpty(l.LocalExpr)
			if l.LocalExpr != nil {
				m.gatherRefs(l.LocalExpr.E, refs, visited, depth+1)
			}
		}
	}
}

func textOrEmpty(e *Expr) string {
	if e == nil {
		return ""
	}
	return e.Text()
}

// LookupAttrText returns the canonical text of the attribute named
// attrName on the entity entityID, if it can be located on the entity's
// recorded fields. Returns ("", false) when the attribute isn't one of
// the named fields (e.g. an arbitrary attribute on a resource block —
// those aren't cached on Entity).
//
// Used by the diff to compare the OLD value of a now-tracked attribute
// when the marker was added in this same PR — so a "marker added" case
// where the underlying value also changed gets promoted from
// Informational to Breaking.
func (m *Module) LookupAttrText(entityID, attrName string) (string, bool) {
	e, ok := m.byID[entityID]
	if !ok {
		return "", false
	}
	switch e.Kind {
	case KindLocal:
		if attrName == "value" || attrName == e.Name {
			return textOrEmpty(e.LocalExpr), e.LocalExpr != nil
		}
	case KindOutput:
		if attrName == "value" {
			return textOrEmpty(e.ValueExpr), e.ValueExpr != nil
		}
	case KindVariable:
		if attrName == "default" {
			return textOrEmpty(e.DefaultExpr), e.DefaultExpr != nil
		}
	case KindModule:
		if x, ok := e.ModuleArgs[attrName]; ok {
			return textOrEmpty(x), true
		}
	}
	switch attrName {
	case "for_each":
		return textOrEmpty(e.ForEachExpr), e.ForEachExpr != nil
	case "count":
		return textOrEmpty(e.CountExpr), e.CountExpr != nil
	case "depends_on":
		return textOrEmpty(e.DependsOnExpr), e.DependsOnExpr != nil
	case "provider":
		return textOrEmpty(e.ProviderExpr), e.ProviderExpr != nil
	case "ignore_changes":
		return textOrEmpty(e.IgnoreChangesExpr), e.IgnoreChangesExpr != nil
	case "replace_triggered_by":
		return textOrEmpty(e.ReplaceTriggeredByExpr), e.ReplaceTriggeredByExpr != nil
	}
	return "", false
}

// SortedRefIDs returns t.Refs's keys in deterministic order, for use by
// callers that need a stable iteration.
func (t TrackedAttribute) SortedRefIDs() []string {
	out := make([]string, 0, len(t.Refs))
	for k := range t.Refs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
