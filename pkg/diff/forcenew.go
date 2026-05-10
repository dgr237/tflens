package diff

import (
	"fmt"
	"strings"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/forcenew"
)

// diffResourceForceNew compares attribute bodies between two versions
// of the same resource (or data source) and emits a Breaking change
// for any attribute whose text differs AND that the embedded force-new
// table classifies as immutable for this resource type.
//
// Scope cuts in this initial wiring:
//   - Compares attribute text only (no eval-context value awareness),
//     so a cosmetic refactor that changes text but not the resolved
//     value still emits a finding. Conservative-by-design — the tool
//     surfacing "this looks like a force-replace" lets the reviewer
//     confirm the value really didn't change.
//   - Walks static nested blocks recursively but pairs only the first
//     instance per block name (covers the common case of singletons
//     like encryption_config / kubernetes_network_config; multi-
//     instance pairing for blocks like ebs_block_device would need a
//     key-based match).
//   - Skips dynamic blocks — their iteration semantics don't map to a
//     single attribute path.
//   - Only attributes present in BOTH old and new are compared.
//     Pure additions / removals are caught earlier by Terraform's
//     own validation or are non-events for force-new purposes.
//
// Findings are tagged Source: "force-new" so renderers can decorate
// them distinctly from text-diff or plan-derived findings.
func diffResourceForceNew(id string, oe, ne analysis.Entity, changes *[]Change) {
	if oe.Kind != analysis.KindResource && oe.Kind != analysis.KindData {
		return
	}
	walkBodyForceNew(id, oe.Type, nil, oe.BodyAttrs, ne.BodyAttrs, oe.BodyBlocks, ne.BodyBlocks, oe, ne, changes)
}

func walkBodyForceNew(id, resourceType string, prefix []string,
	oldAttrs, newAttrs map[string]*analysis.Expr,
	oldBlocks, newBlocks map[string][]*analysis.BodyBlock,
	oe, ne analysis.Entity,
	changes *[]Change,
) {
	for name, oldExpr := range oldAttrs {
		newExpr, present := newAttrs[name]
		if !present {
			continue
		}
		if oldExpr.Text() == newExpr.Text() {
			continue
		}
		path := append(append([]string(nil), prefix...), name)
		fn, _ := forcenew.IsForceNew(resourceType, path)
		if !fn {
			continue
		}
		*changes = append(*changes, Change{
			Kind:    Breaking,
			Subject: id,
			Detail: fmt.Sprintf("force-replace attribute changed: %s = %s → %s (destroy + recreate)",
				strings.Join(path, "."), oldExpr.Text(), newExpr.Text()),
			Hint:   "this attribute is immutable on the underlying resource; expect a destroy+create on apply",
			OldPos: oe.Pos,
			NewPos: ne.Pos,
			Source: "force-new",
		})
	}

	for name, oldInsts := range oldBlocks {
		newInsts, ok := newBlocks[name]
		if !ok || len(oldInsts) == 0 || len(newInsts) == 0 {
			continue
		}
		path := append(append([]string(nil), prefix...), name)
		walkBodyForceNew(id, resourceType, path,
			oldInsts[0].Attrs, newInsts[0].Attrs,
			oldInsts[0].Blocks, newInsts[0].Blocks,
			oe, ne, changes)
	}
}
