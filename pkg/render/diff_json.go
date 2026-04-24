package render

import (
	"github.com/dgr237/tflens/pkg/diff"
)

// JSONSummary is the per-section breakdown of changes by Kind.
// Embedded into both diff and whatif top-level output so the wire
// format keeps a stable shape across subcommands.
type JSONSummary struct {
	Breaking      int `json:"breaking"`
	NonBreaking   int `json:"non_breaking"`
	Informational int `json:"informational"`
}

// Add increments the appropriate field for c's Kind. Convenience for
// builders that want to keep a running tally without re-implementing
// the switch.
func (s *JSONSummary) Add(c diff.Change) {
	switch c.Kind {
	case diff.Breaking:
		s.Breaking++
	case diff.NonBreaking:
		s.NonBreaking++
	case diff.Informational:
		s.Informational++
	}
}

// JSONDiffOutput is the top-level wire form of `tflens diff
// --format=json`. Modules covers paired module calls; RootChanges
// covers the root module (which isn't itself a call). Summary holds
// the totals across both.
type JSONDiffOutput struct {
	BaseRef     string            `json:"base_ref"`
	Path        string            `json:"path"`
	Modules     []JSONDiffModule  `json:"modules"`
	RootChanges []JSONChange      `json:"root_changes,omitempty"`
	Summary     JSONSummary       `json:"summary"`
}

// JSONDiffModule is one paired module call's wire form. Source /
// version fields are emitted only when populated (omitempty), since
// added/removed pairs only carry one side. Summary is per-module.
type JSONDiffModule struct {
	Name       string       `json:"name"`
	Status     string       `json:"status"`
	OldSource  string       `json:"old_source,omitempty"`
	OldVersion string       `json:"old_version,omitempty"`
	NewSource  string       `json:"new_source,omitempty"`
	NewVersion string       `json:"new_version,omitempty"`
	Changes    []JSONChange `json:"changes,omitempty"`
	Summary    JSONSummary  `json:"summary"`
}

// BuildJSONDiff composes the full --format=json output for `tflens
// diff` from the analysis results. results comes from the per-module
// pairing; rootChanges covers the root module's API + tracked diff.
//
// Pairs that aren't Interesting() are filtered out — same rule the
// text renderer uses, so json output and text output describe the
// same set of changes.
func BuildJSONDiff(baseRef, path string, results []diff.PairResult, rootChanges []diff.Change) JSONDiffOutput {
	out := JSONDiffOutput{BaseRef: baseRef, Path: path}
	for _, c := range rootChanges {
		out.RootChanges = append(out.RootChanges, JSONChg(c))
		out.Summary.Add(c)
	}
	for _, r := range results {
		if !r.Interesting() {
			continue
		}
		entry := JSONDiffModule{
			Name:       r.Pair.Key,
			Status:     r.Pair.Status.String(),
			OldSource:  r.Pair.OldSource,
			OldVersion: r.Pair.OldVersion,
			NewSource:  r.Pair.NewSource,
			NewVersion: r.Pair.NewVersion,
		}
		for _, c := range r.Changes {
			entry.Changes = append(entry.Changes, JSONChg(c))
			entry.Summary.Add(c)
			out.Summary.Add(c)
		}
		out.Modules = append(out.Modules, entry)
	}
	return out
}

// JSONWhatifOutput is the top-level wire form of `tflens whatif
// --format=json`. Calls are the per-pair simulation results;
// Summary aggregates DirectImpact + per-Kind change counts.
type JSONWhatifOutput struct {
	BaseRef string             `json:"base_ref"`
	Path    string             `json:"path"`
	Calls   []JSONWhatifCall   `json:"calls"`
	Summary JSONWhatifSummary  `json:"summary"`
}

// JSONWhatifCall is one paired call's simulation result on the wire.
// DirectImpact is the cross-validate findings (parent vs new child);
// APIChanges is the full diff between old and new child for context.
type JSONWhatifCall struct {
	Name         string                `json:"name"`
	Status       string                `json:"status"`
	DirectImpact []JSONValidationError `json:"direct_impact"`
	APIChanges   []JSONChange          `json:"api_changes,omitempty"`
}

// JSONWhatifSummary embeds the diff JSONSummary and adds
// DirectImpact, the cross-validate finding count. The embed keeps
// the marshalled shape flat — direct_impact, breaking, non_breaking,
// informational all appear at the same level.
type JSONWhatifSummary struct {
	DirectImpact int `json:"direct_impact"`
	JSONSummary
}

// BuildJSONWhatif composes the full --format=json output for `tflens
// whatif` from the analysis results.
func BuildJSONWhatif(baseRef, path string, calls []diff.WhatifResult) JSONWhatifOutput {
	out := JSONWhatifOutput{BaseRef: baseRef, Path: path}
	for _, r := range calls {
		entry := JSONWhatifCall{
			Name:   r.Pair.Key,
			Status: r.Pair.Status.String(),
		}
		for _, e := range r.DirectImpact {
			entry.DirectImpact = append(entry.DirectImpact, JSONValErr(e))
			out.Summary.DirectImpact++
		}
		for _, c := range r.APIChanges {
			entry.APIChanges = append(entry.APIChanges, JSONChg(c))
			out.Summary.JSONSummary.Add(c)
		}
		out.Calls = append(out.Calls, entry)
	}
	return out
}
