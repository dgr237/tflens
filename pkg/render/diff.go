package render

import (
	"fmt"
	"io"

	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
)

// WriteDiffResults emits the full text-mode output for `tflens diff`:
// a "Root module:" section (when rootChanges is non-empty) followed
// by a section per interesting paired call. When neither has any
// content, writes a single "No changes detected vs <baseRef>." line.
//
// Sections are separated by a blank line. Each call's per-Kind
// breakdown reuses WriteChangesByKind with the canonical "  " / "    "
// indents.
func WriteDiffResults(w io.Writer, baseRef string, results []diff.PairResult, rootChanges []diff.Change) {
	any := false
	if len(rootChanges) > 0 {
		WriteRootChanges(w, rootChanges)
		any = true
	}
	for _, r := range results {
		if !r.Interesting() {
			continue
		}
		if any {
			fmt.Fprintln(w)
		}
		any = true
		WritePairResult(w, r)
	}
	if !any {
		fmt.Fprintf(w, "No changes detected vs %s.\n", baseRef)
	}
}

// WriteRootChanges emits the API + tracked-attribute changes for the
// root module under a "Root module:" heading. The root isn't a module
// call, so it doesn't show up in the per-module section — but a new
// required root variable, a removed output, etc. still matter.
func WriteRootChanges(w io.Writer, changes []diff.Change) {
	fmt.Fprintln(w, "Root module:")
	WriteChangesByKind(w, "  ", "    ", changes)
}

// WritePairResult emits one paired call's text section: a heading
// describing the change kind (added / removed / changed with optional
// source-or-version delta), followed by either "(no API changes)" or
// the bucketed change list.
func WritePairResult(w io.Writer, r diff.PairResult) {
	switch r.Pair.Status {
	case loader.StatusAdded:
		fmt.Fprintf(w, "Module %q: ADDED (source=%s", r.Pair.Key, r.Pair.NewSource)
		if r.Pair.NewVersion != "" {
			fmt.Fprintf(w, ", version=%s", r.Pair.NewVersion)
		}
		fmt.Fprintln(w, ")")
		return
	case loader.StatusRemoved:
		fmt.Fprintf(w, "Module %q: REMOVED (was source=%s", r.Pair.Key, r.Pair.OldSource)
		if r.Pair.OldVersion != "" {
			fmt.Fprintf(w, ", version=%s", r.Pair.OldVersion)
		}
		fmt.Fprintln(w, ")")
		return
	}

	// changed
	fmt.Fprintf(w, "Module %q:", r.Pair.Key)
	if r.Pair.OldSource != r.Pair.NewSource {
		fmt.Fprintf(w, " source %s → %s", r.Pair.OldSource, r.Pair.NewSource)
	}
	if r.Pair.OldVersion != r.Pair.NewVersion {
		sep := " "
		if r.Pair.OldSource != r.Pair.NewSource {
			sep = ", "
		}
		fmt.Fprintf(w, "%sversion %q → %q", sep, r.Pair.OldVersion, r.Pair.NewVersion)
	}
	if !r.AttrsChanged() {
		fmt.Fprintf(w, " (content changed)")
	}
	fmt.Fprintln(w)

	if len(r.Changes) == 0 {
		fmt.Fprintln(w, "  (no API changes)")
		return
	}
	WriteChangesByKind(w, "  ", "    ", r.Changes)
}

// WriteWhatifResults emits the full text-mode output for `tflens
// whatif`: one section per simulated call, separated by blank lines.
// When calls is empty, writes a single "No upgraded module calls
// to simulate (path vs <baseRef>)." line.
func WriteWhatifResults(w io.Writer, baseRef, path string, calls []diff.WhatifResult) {
	if len(calls) == 0 {
		fmt.Fprintf(w, "No upgraded module calls to simulate (path vs %s).\n", baseRef)
		return
	}
	for i, r := range calls {
		if i > 0 {
			fmt.Fprintln(w)
		}
		WriteWhatifCall(w, path, r)
	}
}

// WriteWhatifCall emits one simulated call's section. Removed calls
// get a single REMOVED line; everything else gets a Direct-impact
// block followed by an optional API-changes block.
func WriteWhatifCall(w io.Writer, path string, r diff.WhatifResult) {
	if r.Pair.Status == loader.StatusRemoved {
		fmt.Fprintf(w, "module.%s: REMOVED (was source=%s, version=%q)\n",
			r.Pair.Key, r.Pair.OldSource, r.Pair.OldVersion)
		return
	}
	fmt.Fprintf(w, "Direct impact on module.%s in %s (%d issue(s)):\n",
		r.Pair.Key, path, len(r.DirectImpact))
	if len(r.DirectImpact) == 0 {
		fmt.Fprintln(w, "  (none — callers at base are compatible with the new child)")
	} else {
		for _, e := range r.DirectImpact {
			fmt.Fprintf(w, "  %s\n", e)
		}
	}
	if len(r.APIChanges) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  API changes for module.%s:\n", r.Pair.Key)
	WriteChangesByKind(w, "    ", "      ", r.APIChanges)
}
