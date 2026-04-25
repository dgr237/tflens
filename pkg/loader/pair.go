package loader

import (
	"strings"

	"github.com/dgr237/tflens/pkg/analysis"
)

// ModuleCallStatus classifies a module call by its presence across two
// loaded Projects (typically the working tree and the same workspace
// at a base ref).
type ModuleCallStatus int

const (
	// StatusChanged: present in both projects. May or may not have
	// content-level differences — the caller diffs the modules to find
	// out.
	StatusChanged ModuleCallStatus = iota
	// StatusAdded: present in the new project, absent in the old.
	StatusAdded
	// StatusRemoved: present in the old project, absent in the new.
	StatusRemoved
)

// String returns a stable lowercase label suitable for JSON output.
func (s ModuleCallStatus) String() string {
	switch s {
	case StatusAdded:
		return "added"
	case StatusRemoved:
		return "removed"
	default:
		return "changed"
	}
}

// ModuleCallPair describes one module call's state across the two
// projects. Calls are identified by their dotted key path from the
// project root — e.g. "vpc" for a root-level call, "vpc.sg" for a
// sub-module's call to "sg".
//
// Old/New fields are populated according to Status:
//   - StatusAdded:   only New* fields are non-zero
//   - StatusRemoved: only Old* fields are non-zero
//   - StatusChanged: both sides populated
type ModuleCallPair struct {
	// Key is the dotted-path identifier ("vpc", "vpc.sg").
	Key string
	// LocalName is the leaf segment of Key — the call name as
	// declared in its parent ("sg" for "vpc.sg", "vpc" for "vpc").
	LocalName string
	Status    ModuleCallStatus

	OldSource, NewSource   string
	OldVersion, NewVersion string

	// OldParent / NewParent are the ModuleNodes containing the call
	// (the modules with the `module "<LocalName>" {}` block). Used
	// for cross-validation. Nil when the call doesn't exist on that
	// side.
	OldParent, NewParent *ModuleNode
	// OldNode / NewNode are the resolved child module nodes on each
	// side. Nil when the call doesn't exist, or when the source did
	// not resolve (e.g. --offline against a registry source).
	OldNode, NewNode *ModuleNode
}

// PairModuleCalls joins every module call in both projects by dotted
// key. Covers the entire tree: a change to a sub-sub-module is still
// reported. Either project may be nil — the result will be a series
// of all-Added or all-Removed pairs.
//
// Iteration order of the returned slice is map-iteration order, i.e.
// non-deterministic; callers that care about output ordering should
// sort by Key.
func PairModuleCalls(oldProj, newProj *Project) []ModuleCallPair {
	oldCalls := collectModuleCalls(oldProj)
	newCalls := collectModuleCalls(newProj)

	keys := map[string]struct{}{}
	for k := range oldCalls {
		keys[k] = struct{}{}
	}
	for k := range newCalls {
		keys[k] = struct{}{}
	}

	out := make([]ModuleCallPair, 0, len(keys))
	for key := range keys {
		oldC, hasOld := oldCalls[key]
		newC, hasNew := newCalls[key]
		p := ModuleCallPair{Key: key, LocalName: leafSegment(key)}
		switch {
		case !hasOld:
			p.Status = StatusAdded
			p.NewSource = newC.source
			p.NewVersion = newC.version
			p.NewParent = newC.parent
			p.NewNode = newC.child
		case !hasNew:
			p.Status = StatusRemoved
			p.OldSource = oldC.source
			p.OldVersion = oldC.version
			p.OldParent = oldC.parent
			p.OldNode = oldC.child
		default:
			p.Status = StatusChanged
			p.OldSource = oldC.source
			p.OldVersion = oldC.version
			p.OldParent = oldC.parent
			p.OldNode = oldC.child
			p.NewSource = newC.source
			p.NewVersion = newC.version
			p.NewParent = newC.parent
			p.NewNode = newC.child
		}
		out = append(out, p)
	}
	return out
}

// leafSegment returns the trailing dotted segment of s — "sg" for
// "vpc.sg", "vpc" for "vpc" (no dot), "" for "" (no input). Internal
// helper for PairModuleCalls.
func leafSegment(s string) string {
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return s
}

// collectedCall bundles everything PairModuleCalls records per module
// call while walking a project tree. Internal — callers see the
// flatter ModuleCallPair after pairing.
type collectedCall struct {
	source, version string
	parent          *ModuleNode
	child           *ModuleNode // nil when the call did not resolve
}

// collectModuleCalls walks the project's module tree and returns
// every module call keyed by its dotted path from the root.
func collectModuleCalls(p *Project) map[string]collectedCall {
	out := map[string]collectedCall{}
	if p == nil || p.Root == nil {
		return out
	}
	var walk func(prefix string, node *ModuleNode)
	walk = func(prefix string, node *ModuleNode) {
		if node == nil || node.Module == nil {
			return
		}
		for _, e := range node.Module.Filter(analysis.KindModule) {
			key := e.Name
			if prefix != "" {
				key = prefix + "." + e.Name
			}
			child := node.Children[e.Name]
			out[key] = collectedCall{
				source:  node.Module.ModuleSource(e.Name),
				version: node.Module.ModuleVersion(e.Name),
				parent:  node,
				child:   child,
			}
			if child != nil {
				walk(key, child)
			}
		}
	}
	walk("", p.Root)
	return out
}
