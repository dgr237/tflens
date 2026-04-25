package render

import (
	"fmt"
	"io"

	"github.com/dgr237/tflens/pkg/statediff"
)

// writeStatediff emits a human-readable text report of a statediff.Result.
// Sections are separated by a blank line and only emitted when they have
// content; an empty result (no findings, no orphans) produces a single
// "No … changes detected vs <ref>." line.
//
// The order is: resource identity adds/removes, renames, sensitive
// value changes (with affected resources nested underneath), then
// state orphans. State orphans are reported separately because they
// represent pre-existing drift, not something this PR introduced —
// they don't gate CI either.
//
// Nil-safe: writes nothing for a nil result.
func writeStatediff(w io.Writer, r *statediff.Result) {
	if r == nil {
		return
	}
	any := false

	if len(r.AddedResources) > 0 || len(r.RemovedResources) > 0 {
		any = true
		fmt.Fprintf(w, "Resource identity changes vs %s:\n", r.BaseRef)
		for _, a := range r.AddedResources {
			fmt.Fprintf(w, "  + %s (%s)\n", a.Address(), a.Mode)
		}
		for _, a := range r.RemovedResources {
			fmt.Fprintf(w, "  - %s (%s)\n", a.Address(), a.Mode)
		}
	}

	if len(r.RenamedResources) > 0 {
		if any {
			fmt.Fprintln(w)
		}
		any = true
		fmt.Fprintln(w, "Renames (moved block handled — no destroy/recreate):")
		for _, rn := range r.RenamedResources {
			fmt.Fprintf(w, "  %s → %s\n", rn.FromAddress(), rn.ToAddress())
		}
	}

	if len(r.SensitiveChanges) > 0 {
		if any {
			fmt.Fprintln(w)
		}
		any = true
		fmt.Fprintln(w, "Value changes that may alter count/for_each expansion:")
		for _, sc := range r.SensitiveChanges {
			prefix := sc.Kind + "." + sc.Name
			if sc.Module != "" {
				prefix = sc.Module + "." + prefix
			}
			fmt.Fprintf(w, "  - %s\n", prefix)
			fmt.Fprintf(w, "      old: %s\n", orAbsent(sc.OldValue))
			fmt.Fprintf(w, "      new: %s\n", orAbsent(sc.NewValue))
			for _, ar := range sc.AffectedResources {
				fmt.Fprintf(w, "    Affected: %s (%s)\n", ar.Address(), ar.MetaArg)
				for _, inst := range ar.StateInstances {
					fmt.Fprintf(w, "      • state instance: %s\n", inst)
				}
			}
		}
	}

	if len(r.StateOrphans) > 0 {
		if any {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, "State drift — addresses in state but not declared in the new tree:")
		for _, o := range r.StateOrphans {
			fmt.Fprintf(w, "  ? %s\n", o)
		}
	}

	if !any && len(r.StateOrphans) == 0 {
		fmt.Fprintf(w, "No resource identity or sensitive-local changes detected vs %s.\n", r.BaseRef)
	}
}

// orAbsent renders an empty value as "(absent)" so the reader can tell
// "default removed" apart from "default = empty string".
func orAbsent(s string) string {
	if s == "" {
		return "(absent)"
	}
	return s
}
