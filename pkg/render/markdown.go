package render

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/statediff"
	"github.com/dgr237/tflens/pkg/token"
)

// MarkdownRenderer is the GitHub-flavoured markdown Renderer
// implementation — output is a single stream (warnings stay on stdout
// rather than splitting to stderr) so the whole document can be piped
// into `gh pr comment`, GitHub Actions $GITHUB_STEP_SUMMARY, or any
// markdown-friendly sticky-comment tool.
//
// The four ref-comparing surfaces (Diff, Whatif, Statediff) and
// Validate get the rich treatment — severity badges, collapsible
// `<details>` sections per module, code-fenced fix hints, file:line
// as inline backticks. The single-module surfaces (Cycles, Deps,
// Impact, Inventory, Unused) and the operational ones (Cache*,
// FmtParseErrors) get terse markdown impls so the Renderer composite
// stays satisfied — they're rarely useful in PR comments but the
// interface needs them.
type MarkdownRenderer struct {
	W io.Writer
}

// Severity badges — emoji + text label.  Match GitHub's preferred
// circle-emoji style for accessibility (text label survives screen
// readers; emoji gives quick visual scan).
const (
	badgeBreaking      = "🔴 Breaking"
	badgeNonBreaking   = "🟡 Non-breaking"
	badgeInformational = "🔵 Informational"
)

func badgeForKind(k diff.ChangeKind) string {
	switch k {
	case diff.Breaking:
		return badgeBreaking
	case diff.NonBreaking:
		return badgeNonBreaking
	case diff.Informational:
		return badgeInformational
	}
	return string(k.String())
}

// ---- ref-comparing subcommands (the headline use case) ----

func (m *MarkdownRenderer) Diff(
	baseRef, path string,
	results []diff.PairResult,
	rootChanges []diff.Change,
) {
	totals := totalsFromResults(results, rootChanges)
	m.writeHeader("`tflens diff` results", baseRef, path, totals)

	if !totals.any() {
		fmt.Fprintf(m.W, "**No changes detected vs `%s`.** ✅\n", baseRef)
		return
	}

	if len(rootChanges) > 0 {
		fmt.Fprintln(m.W, "## Root module")
		fmt.Fprintln(m.W)
		m.writeChangeList(rootChanges)
		fmt.Fprintln(m.W)
	}

	for _, r := range results {
		if !r.Interesting() {
			continue
		}
		m.writePairResult(r)
	}
}

func (m *MarkdownRenderer) Whatif(baseRef, path string, calls []diff.WhatifResult) {
	// Whatif results are per-module-call. Build a flattened totals row
	// across all calls' API-changes + direct-impact errors so the
	// header has the same shape as Diff.
	totals := changeTotals{}
	for _, c := range calls {
		for _, ch := range c.APIChanges {
			totals.add(ch.Kind)
		}
		if len(c.DirectImpact) > 0 {
			totals.directImpact += len(c.DirectImpact)
		}
	}
	m.writeHeader("`tflens whatif` results", baseRef, path, totals)

	if len(calls) == 0 {
		fmt.Fprintf(m.W, "**No module calls to evaluate vs `%s`.**\n", baseRef)
		return
	}

	for _, c := range calls {
		m.writeWhatifCall(c)
	}
}

func (m *MarkdownRenderer) Statediff(result *statediff.Result) {
	if result == nil {
		fmt.Fprintln(m.W, "## `tflens statediff` results")
		fmt.Fprintln(m.W)
		fmt.Fprintln(m.W, "No state diff result.")
		return
	}
	flagged := result.FlaggedCount()
	fmt.Fprintln(m.W, "## `tflens statediff` results")
	fmt.Fprintln(m.W)
	if flagged == 0 {
		fmt.Fprintln(m.W, "**No flagged resources.** ✅")
		return
	}
	fmt.Fprintf(m.W, "**%d flagged item%s.**\n\n", flagged, plural(flagged))

	if len(result.AddedResources) > 0 {
		fmt.Fprintln(m.W, "### Added resources")
		fmt.Fprintln(m.W)
		for _, r := range result.AddedResources {
			fmt.Fprintf(m.W, "- `%s`\n", r.Address())
		}
		fmt.Fprintln(m.W)
	}
	if len(result.RemovedResources) > 0 {
		fmt.Fprintln(m.W, "### Removed resources")
		fmt.Fprintln(m.W)
		for _, r := range result.RemovedResources {
			fmt.Fprintf(m.W, "- `%s`\n", r.Address())
		}
		fmt.Fprintln(m.W)
	}
	if len(result.RenamedResources) > 0 {
		fmt.Fprintln(m.W, "### Renamed resources (via `moved` blocks)")
		fmt.Fprintln(m.W)
		for _, p := range result.RenamedResources {
			fmt.Fprintf(m.W, "- `%s` → `%s`\n", p.FromAddress(), p.ToAddress())
		}
		fmt.Fprintln(m.W)
	}
	if len(result.SensitiveChanges) > 0 {
		fmt.Fprintln(m.W, "### Sensitive changes reaching count/for_each")
		fmt.Fprintln(m.W)
		for _, sc := range result.SensitiveChanges {
			label := fmt.Sprintf("%s.%s", sc.Kind, sc.Name)
			if sc.Module != "" {
				label = sc.Module + "." + label
			}
			fmt.Fprintf(m.W, "<details><summary><code>%s</code> changed (`%s` → `%s`)</summary>\n\n",
				label, displayValue(sc.OldValue), displayValue(sc.NewValue))
			if len(sc.AffectedResources) > 0 {
				fmt.Fprintln(m.W, "Affected resources:")
				fmt.Fprintln(m.W)
				for _, a := range sc.AffectedResources {
					fmt.Fprintf(m.W, "- `%s` (via `%s`)\n", a.Address(), a.MetaArg)
					for _, inst := range a.StateInstances {
						fmt.Fprintf(m.W, "  - state instance: `%s`\n", inst)
					}
				}
				fmt.Fprintln(m.W)
			}
			fmt.Fprintln(m.W, "</details>")
			fmt.Fprintln(m.W)
		}
	}
	if len(result.StateOrphans) > 0 {
		fmt.Fprintln(m.W, "### State orphans (in state but not in source)")
		fmt.Fprintln(m.W)
		for _, o := range result.StateOrphans {
			fmt.Fprintf(m.W, "- `%s`\n", o)
		}
		fmt.Fprintln(m.W)
	}
}

// ---- validate ----

func (m *MarkdownRenderer) Validate(
	refErrs, crossErrs []analysis.ValidationError,
	typeErrs []analysis.TypeCheckError,
) {
	total := len(refErrs) + len(crossErrs) + len(typeErrs)
	fmt.Fprintln(m.W, "## `tflens validate` results")
	fmt.Fprintln(m.W)
	if total == 0 {
		fmt.Fprintln(m.W, "**No issues found.** ✅")
		return
	}
	fmt.Fprintf(m.W, "**%d issue%s found.** 🔴\n\n", total, plural(total))

	if len(refErrs) > 0 {
		fmt.Fprintln(m.W, "### Undefined references")
		fmt.Fprintln(m.W)
		for _, e := range refErrs {
			fmt.Fprintf(m.W, "- `%s` references undeclared `%s` &mdash; %s\n",
				e.EntityID, e.Ref, locationCode(e.Pos))
		}
		fmt.Fprintln(m.W)
	}
	if len(crossErrs) > 0 {
		fmt.Fprintln(m.W, "### Cross-module issues")
		fmt.Fprintln(m.W)
		for _, e := range crossErrs {
			msg := e.Msg
			if msg == "" {
				msg = fmt.Sprintf("%s is undefined", e.Ref)
			}
			fmt.Fprintf(m.W, "- `%s`: %s &mdash; %s\n", e.EntityID, msg, locationCode(e.Pos))
		}
		fmt.Fprintln(m.W)
	}
	if len(typeErrs) > 0 {
		fmt.Fprintln(m.W, "### Type errors")
		fmt.Fprintln(m.W)
		for _, e := range typeErrs {
			attr := ""
			if e.Attr != "" {
				attr = fmt.Sprintf(" (`%s`)", e.Attr)
			}
			fmt.Fprintf(m.W, "- `%s`%s: %s &mdash; %s\n",
				e.EntityID, attr, e.Msg, locationCode(e.Pos))
		}
		fmt.Fprintln(m.W)
	}
}

// ---- single-module surfaces (terse impls) ----

func (m *MarkdownRenderer) Cycles(cycles [][]string) {
	fmt.Fprintln(m.W, "## Dependency cycles")
	fmt.Fprintln(m.W)
	if len(cycles) == 0 {
		fmt.Fprintln(m.W, "**No cycles detected.** ✅")
		return
	}
	for _, cyc := range cycles {
		fmt.Fprintf(m.W, "- %s\n", strings.Join(quoteAll(cyc), " → "))
	}
}

func (m *MarkdownRenderer) Deps(id string, deps, dependents []string) {
	fmt.Fprintf(m.W, "## Dependencies of `%s`\n\n", id)
	fmt.Fprintln(m.W, "**Depends on:**")
	fmt.Fprintln(m.W)
	if len(deps) == 0 {
		fmt.Fprintln(m.W, "- _(none)_")
	} else {
		for _, d := range deps {
			fmt.Fprintf(m.W, "- `%s`\n", d)
		}
	}
	fmt.Fprintln(m.W)
	fmt.Fprintln(m.W, "**Referenced by:**")
	fmt.Fprintln(m.W)
	if len(dependents) == 0 {
		fmt.Fprintln(m.W, "- _(none)_")
		return
	}
	for _, d := range dependents {
		fmt.Fprintf(m.W, "- `%s`\n", d)
	}
}

func (m *MarkdownRenderer) Impact(id string, affected []string) {
	fmt.Fprintf(m.W, "## Impact of changing `%s`\n\n", id)
	if len(affected) == 0 {
		fmt.Fprintln(m.W, "_No other entities affected._")
		return
	}
	fmt.Fprintf(m.W, "**%d entit%s affected** (topological order):\n\n", len(affected), pluralY(len(affected)))
	for _, a := range affected {
		fmt.Fprintf(m.W, "1. `%s`\n", a)
	}
}

func (m *MarkdownRenderer) Inventory(mod *analysis.Module) {
	ents := mod.Entities()
	fmt.Fprintf(m.W, "## Inventory (%d entit%s)\n\n", len(ents), pluralY(len(ents)))
	if len(ents) == 0 {
		fmt.Fprintln(m.W, "_(empty)_")
		return
	}
	fmt.Fprintln(m.W, "| Kind | ID | Location |")
	fmt.Fprintln(m.W, "| --- | --- | --- |")
	for _, e := range ents {
		fmt.Fprintf(m.W, "| %s | `%s` | `%s` |\n", e.Kind, e.ID(), e.Location())
	}
}

func (m *MarkdownRenderer) Unused(unused []analysis.Entity) {
	fmt.Fprintf(m.W, "## Unused entities (%d)\n\n", len(unused))
	if len(unused) == 0 {
		fmt.Fprintln(m.W, "**No unused entities.** ✅")
		return
	}
	for _, e := range unused {
		fmt.Fprintf(m.W, "- `%s` &mdash; `%s`\n", e.ID(), e.Location())
	}
}

// ---- cache ----

func (m *MarkdownRenderer) CacheInfo(path string, entries int, bytes int64) {
	fmt.Fprintln(m.W, "## Cache info")
	fmt.Fprintln(m.W)
	fmt.Fprintf(m.W, "- **Path:** `%s`\n", path)
	fmt.Fprintf(m.W, "- **Entries:** %d\n", entries)
	fmt.Fprintf(m.W, "- **Size:** %s\n", humanBytes(bytes))
}

func (m *MarkdownRenderer) CacheAlreadyEmpty(path string) {
	fmt.Fprintf(m.W, "Cache at `%s` was already empty.\n", path)
}

func (m *MarkdownRenderer) CacheCleared(entries int, bytes int64, path string) {
	fmt.Fprintf(m.W, "Cleared %d entr%s (%s) from cache at `%s`.\n",
		entries, pluralY(entries), humanBytes(bytes), path)
}

// ---- fmt ----

func (m *MarkdownRenderer) FmtParseErrors(diags hcl.Diagnostics) {
	fmt.Fprintln(m.W, "## Parse errors")
	fmt.Fprintln(m.W)
	for _, d := range diags {
		var loc string
		if d.Subject != nil {
			loc = fmt.Sprintf(" (`%s:%d:%d`)", d.Subject.Filename,
				d.Subject.Start.Line, d.Subject.Start.Column)
		}
		fmt.Fprintf(m.W, "- **%s**: %s%s\n", d.Summary, d.Detail, loc)
	}
}

// ---- internal helpers ----

// changeTotals tallies the per-Kind counts plus a directImpact counter
// for whatif. Renders into a one-liner header summary.
type changeTotals struct {
	breaking      int
	nonBreaking   int
	informational int
	directImpact  int
}

func (t *changeTotals) add(k diff.ChangeKind) {
	switch k {
	case diff.Breaking:
		t.breaking++
	case diff.NonBreaking:
		t.nonBreaking++
	case diff.Informational:
		t.informational++
	}
}

func (t changeTotals) any() bool {
	return t.breaking+t.nonBreaking+t.informational+t.directImpact > 0
}

func totalsFromResults(results []diff.PairResult, rootChanges []diff.Change) changeTotals {
	totals := changeTotals{}
	for _, ch := range rootChanges {
		totals.add(ch.Kind)
	}
	for _, r := range results {
		for _, ch := range r.Changes {
			totals.add(ch.Kind)
		}
	}
	return totals
}

// writeHeader emits the title + base-ref + summary line shared by Diff
// and Whatif. The summary line uses the badge constants so a quick
// scan tells you what severity to expect before diving into details.
func (m *MarkdownRenderer) writeHeader(title, baseRef, path string, t changeTotals) {
	fmt.Fprintf(m.W, "## %s\n\n", title)
	fmt.Fprintf(m.W, "_Base ref: `%s` &middot; Path: `%s`_\n\n", baseRef, path)
	if t.any() {
		var parts []string
		if t.breaking > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", t.breaking, badgeBreaking))
		}
		if t.nonBreaking > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", t.nonBreaking, badgeNonBreaking))
		}
		if t.informational > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", t.informational, badgeInformational))
		}
		if t.directImpact > 0 {
			parts = append(parts, fmt.Sprintf("%d direct impact", t.directImpact))
		}
		fmt.Fprintf(m.W, "**Summary:** %s\n\n", strings.Join(parts, ", "))
	}
}

// writePairResult emits one paired-call section. Added/Removed get
// short single-line headings; Changed gets a `<details>` collapsible
// with the per-Kind change list.
func (m *MarkdownRenderer) writePairResult(r diff.PairResult) {
	switch r.Pair.Status {
	case loader.StatusAdded:
		fmt.Fprintf(m.W, "### Module `%s` &mdash; **ADDED** (source `%s`",
			r.Pair.Key, r.Pair.NewSource)
		if r.Pair.NewVersion != "" {
			fmt.Fprintf(m.W, ", version `%s`", r.Pair.NewVersion)
		}
		fmt.Fprintln(m.W, ")")
		fmt.Fprintln(m.W)
		return
	case loader.StatusRemoved:
		fmt.Fprintf(m.W, "### Module `%s` &mdash; **REMOVED** (was source `%s`",
			r.Pair.Key, r.Pair.OldSource)
		if r.Pair.OldVersion != "" {
			fmt.Fprintf(m.W, ", version `%s`", r.Pair.OldVersion)
		}
		fmt.Fprintln(m.W, ")")
		fmt.Fprintln(m.W)
		return
	}

	// changed
	openSummary := fmt.Sprintf("Module <code>%s</code>", r.Pair.Key)
	if r.Pair.OldSource != r.Pair.NewSource {
		openSummary += fmt.Sprintf(" &mdash; source <code>%s</code> → <code>%s</code>",
			r.Pair.OldSource, r.Pair.NewSource)
	}
	if r.Pair.OldVersion != r.Pair.NewVersion {
		openSummary += fmt.Sprintf(" &mdash; version <code>%s</code> → <code>%s</code>",
			r.Pair.OldVersion, r.Pair.NewVersion)
	}
	if len(r.Changes) == 0 {
		fmt.Fprintf(m.W, "### %s\n\n_(no API changes)_\n\n", openSummary)
		return
	}

	// Collapsed by default for non-Breaking-only sections; opened by
	// default when the section contains any Breaking change so the
	// PR reviewer sees the most important info immediately.
	openAttr := ""
	for _, ch := range r.Changes {
		if ch.Kind == diff.Breaking {
			openAttr = " open"
			break
		}
	}
	fmt.Fprintf(m.W, "<details%s><summary>%s</summary>\n\n", openAttr, openSummary)
	m.writeChangeList(r.Changes)
	fmt.Fprintln(m.W, "</details>")
	fmt.Fprintln(m.W)
}

// writeWhatifCall emits one whatif call section. Direct-impact gets a
// 🔴 prefix so reviewers spot the gating signal at a glance.
func (m *MarkdownRenderer) writeWhatifCall(c diff.WhatifResult) {
	directImpact := len(c.DirectImpact) > 0
	prefix := ""
	if directImpact {
		prefix = "🔴 "
	}
	heading := fmt.Sprintf("%sModule `%s`", prefix, c.Pair.Key)
	if c.Pair.OldVersion != "" || c.Pair.NewVersion != "" {
		heading += fmt.Sprintf(" &mdash; version `%s` → `%s`",
			c.Pair.OldVersion, c.Pair.NewVersion)
	}
	if len(c.APIChanges) == 0 && !directImpact {
		fmt.Fprintf(m.W, "### %s\n\n_(no consumer-affecting changes)_\n\n", heading)
		return
	}
	openAttr := ""
	if directImpact {
		openAttr = " open"
	}
	fmt.Fprintf(m.W, "<details%s><summary>%s</summary>\n\n", openAttr, heading)
	if directImpact {
		fmt.Fprintln(m.W, "**Direct impact on this caller:**")
		fmt.Fprintln(m.W)
		for _, e := range c.DirectImpact {
			msg := e.Msg
			if msg == "" {
				msg = fmt.Sprintf("%s is undefined", e.Ref)
			}
			fmt.Fprintf(m.W, "- `%s`: %s\n", e.EntityID, msg)
		}
		fmt.Fprintln(m.W)
	}
	if len(c.APIChanges) > 0 {
		if directImpact {
			fmt.Fprintln(m.W, "**Full API diff:**")
			fmt.Fprintln(m.W)
		}
		m.writeChangeList(c.APIChanges)
	}
	fmt.Fprintln(m.W, "</details>")
	fmt.Fprintln(m.W)
}

// writeChangeList formats a slice of Change as a markdown bullet list,
// grouped by Kind (Breaking → NonBreaking → Informational), with each
// entry showing severity badge, subject as inline code, detail, and
// (when present) a fix hint as a code-fenced block.
func (m *MarkdownRenderer) writeChangeList(changes []diff.Change) {
	// Sort by kind so the list reads severity-first regardless of
	// upstream ordering.
	sorted := append([]diff.Change(nil), changes...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Kind < sorted[j].Kind
	})
	for _, ch := range sorted {
		fmt.Fprintf(m.W, "- %s &mdash; `%s`: %s\n", badgeForKind(ch.Kind), ch.Subject, ch.Detail)
		if ch.Hint != "" {
			fmt.Fprintf(m.W, "  > **Fix:** %s\n", ch.Hint)
		}
	}
	fmt.Fprintln(m.W)
}

// locationCode renders a token.Position as inline code suitable for
// embedding in a list item — empty when the position is zero.
func locationCode(p token.Position) string {
	if p.File == "" && p.Line == 0 {
		return ""
	}
	if p.File == "" {
		return fmt.Sprintf("`:%d`", p.Line)
	}
	return fmt.Sprintf("`%s:%d`", p.File, p.Line)
}

// quoteAll wraps each string in backticks for inline-code rendering.
// Used by Cycles to make the cycle path readable.
func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = "`" + s + "`"
	}
	return out
}

// plural returns "s" when n != 1 (for words like "issue").
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// pluralY returns "y" when n == 1 and "ies" otherwise (for words like
// "entity"/"entries").
func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// displayValue renders a sensitive-change value for the markdown
// summary — empty strings become the literal "(absent)" so collapsed
// summaries stay readable.
func displayValue(v string) string {
	if v == "" {
		return "(absent)"
	}
	return v
}
