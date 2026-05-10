// Package forcenew exposes a lookup API over the embedded force-new
// attribute table — for each Terraform resource type the table covers,
// the set of attribute paths whose value change forces destroy+recreate.
//
// The table is generated from a Crossplane runtime IR by
// internal/tools/cpir-extract and embedded at build time. Refresh runs
// in CI; consumers (pkg/diff breaking-change classifier) see only this
// package's two-function API and never touch the IR.
//
// Coverage is bounded by what the source IR carries. Resources missing
// from the table are reported as known=false from IsForceNew so callers
// can decide between "treat as safe" or "ask user for an override
// table" — see Init for the override path.
package forcenew

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"sync"
)

//go:embed data/force_new_table.json
var tableJSON []byte

// Table is the in-memory force-new lookup. Resources maps a Terraform
// resource type to the set of attribute paths whose value change forces
// destroy+recreate. Source carries the IR provenance label written at
// extraction time so `tflens version` (or similar) can surface which
// Crossplane IR the binary was built against.
type Table struct {
	Source    string
	Resources map[string][][]string
}

var (
	tableOnce sync.Once
	table     *Table
	// initOverride is set by Init before the first lookup so loadTable
	// can apply the override during the OnceFunc-protected init. After
	// loadTable runs, this field is no longer consulted.
	initOverride string
)

func loadTable() {
	t := parseEmbedded()
	if initOverride != "" {
		if err := mergeOverrideFromFile(t, initOverride); err != nil {
			// Init already returned this error to the caller; here we
			// just keep the embedded table without the override applied.
			// Belt-and-braces — Init's error path should have prevented
			// this codepath from running.
			_ = err
		}
	}
	table = t
}

func parseEmbedded() *Table {
	t, err := ReadJSON(bytes.NewReader(tableJSON))
	if err != nil {
		return &Table{Resources: map[string][][]string{}}
	}
	return t
}

func mergeOverrideFromFile(into *Table, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	override, err := ReadJSON(f)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	into.Merge(override)
	return nil
}

// Init loads the embedded table and optionally merges a user-supplied
// override JSON from path. Call once at startup before any IsForceNew
// lookups; pass "" to skip override loading. Returns an error only
// when path is non-empty and unreadable / unparseable — embedded-table
// parse failures degrade silently (the table appears empty).
//
// Calling Init after a lookup has already triggered loadTable is a
// no-op for the override merge — the singleton is locked in by the
// first IsForceNew call. Wire Init through cobra's PersistentPreRunE
// (or equivalent) to ensure correct ordering.
func Init(overridePath string) error {
	if overridePath == "" {
		return nil
	}
	// Validate eagerly so a bad path/JSON fails the CLI startup with a
	// clear error rather than silently producing wrong classifications.
	f, err := os.Open(overridePath)
	if err != nil {
		return fmt.Errorf("immutable-table-override: %w", err)
	}
	if _, err := ReadJSON(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("immutable-table-override: %w", err)
	}
	_ = f.Close()
	initOverride = overridePath
	return nil
}

// IsForceNew reports whether a value change at `path` on a resource of
// type `tfType` triggers destroy+recreate. The second return reports
// whether tfType appears in the table at all — callers can distinguish
// "definitely not force-new" (forceNew=false, known=true) from "no
// data for this resource" (forceNew=false, known=false). The latter
// is the right signal to suggest a user-supplied override table.
func IsForceNew(tfType string, path []string) (forceNew, known bool) {
	tableOnce.Do(loadTable)
	paths, ok := table.Resources[tfType]
	if !ok {
		return false, false
	}
	for _, p := range paths {
		if pathsEqual(p, path) {
			return true, true
		}
	}
	return false, true
}

// Source returns the IR provenance label baked into the embedded
// table at extraction time, e.g. "crossplane-runtime-ir-v2.5.0-9fb84fc37179".
// Empty when the embedded table failed to load.
func Source() string {
	tableOnce.Do(loadTable)
	return table.Source
}

// Resources reports how many Terraform resource types the active
// table covers (embedded ∪ override). Useful for `tflens version`
// provenance output.
func Resources() int {
	tableOnce.Do(loadTable)
	return len(table.Resources)
}

func pathsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
