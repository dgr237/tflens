package analysis_test

import (
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
)

// validateCase describes one TestValidateCases case. The fixture is read
// from pkg/analysis/testdata/validate/<Name>/main.tf and analysed; the
// resulting ValidationErrors are checked.
type validateCase struct {
	Name string

	WantNoErrors bool

	// WantRefs lists Refs that must each appear in at least one error.
	WantRefs []string

	// WantPair asserts at least one error has both EntityID == EntityID and
	// Ref == Ref. Empty fields are wildcards.
	WantPair *struct{ EntityID, Ref string }

	// MustNotMatchRef is checked against every error: no error may have
	// this Ref. Useful for "X should NOT be flagged".
	MustNotMatchRef string

	Custom func(t *testing.T, errs []analysis.ValidationError)
}

func TestValidateCases(t *testing.T) {
	for _, tc := range validateCases {
		t.Run(tc.Name, func(t *testing.T) {
			src := loadAnalysisFixture(t, "validate", tc.Name)
			errs := analyseFixtureNamed(t, "main.tf", src).Validate()

			if tc.Custom != nil {
				tc.Custom(t, errs)
				return
			}
			if tc.WantNoErrors {
				if len(errs) != 0 {
					t.Errorf("expected no validation errors, got: %v", errs)
				}
				return
			}
			for _, ref := range tc.WantRefs {
				if !hasRef(errs, ref) {
					t.Errorf("expected error with Ref=%q; got: %v", ref, errs)
				}
			}
			if tc.WantPair != nil {
				if !hasPair(errs, tc.WantPair.EntityID, tc.WantPair.Ref) {
					t.Errorf("expected error with EntityID=%q Ref=%q; got: %v",
						tc.WantPair.EntityID, tc.WantPair.Ref, errs)
				}
			}
			if tc.MustNotMatchRef != "" {
				for _, e := range errs {
					if e.Ref == tc.MustNotMatchRef {
						t.Errorf("did not expect error with Ref=%q, got: %v", tc.MustNotMatchRef, e)
					}
				}
			}
		})
	}
}

func hasRef(errs []analysis.ValidationError, ref string) bool {
	for _, e := range errs {
		if e.Ref == ref {
			return true
		}
	}
	return false
}

func hasPair(errs []analysis.ValidationError, entityID, ref string) bool {
	for _, e := range errs {
		if (entityID == "" || e.EntityID == entityID) && (ref == "" || e.Ref == ref) {
			return true
		}
	}
	return false
}

var validateCases = []validateCase{
	{Name: "clean_module", WantNoErrors: true},
	{Name: "undefined_variable", WantRefs: []string{"variable.missing"}},
	{Name: "undefined_local", WantRefs: []string{"local.ghost"}},
	{Name: "undefined_module", WantRefs: []string{"module.network"}},
	{Name: "undefined_data_source", WantRefs: []string{"data.aws_ami.ghost"}},
	{Name: "defined_reference_no_error", WantNoErrors: true},
	{Name: "builtins_not_flagged", WantNoErrors: true},
	{
		Name: "error_has_position",
		Custom: func(t *testing.T, errs []analysis.ValidationError) {
			if len(errs) == 0 {
				t.Fatal("expected a validation error, got none")
			}
			e := errs[0]
			if e.Pos.File != "main.tf" {
				t.Errorf("Pos.File = %q, want %q", e.Pos.File, "main.tf")
			}
			if e.Pos.Line != 1 {
				t.Errorf("Pos.Line = %d, want 1", e.Pos.Line)
			}
		},
	},
	{
		Name: "errors_sorted_by_position",
		Custom: func(t *testing.T, errs []analysis.ValidationError) {
			if len(errs) < 2 {
				t.Fatalf("expected at least 2 validation errors, got %d", len(errs))
			}
			for i := 1; i < len(errs); i++ {
				if errs[i-1].Pos.Line > errs[i].Pos.Line {
					t.Errorf("errors not sorted by line: %d after %d",
						errs[i].Pos.Line, errs[i-1].Pos.Line)
				}
			}
		},
	},
	{
		Name:     "sensitive_var_referenced_by_nonsensitive_output",
		WantPair: &struct{ EntityID, Ref string }{"output.pw", "variable.password"},
	},
	{
		Name: "sensitive_output_references_sensitive_var_ok",
		Custom: func(t *testing.T, errs []analysis.ValidationError) {
			for _, e := range errs {
				if e.EntityID == "output.pw" {
					t.Errorf("sensitive output referencing sensitive var should not be flagged, got: %v", e)
				}
			}
		},
	},
	{
		Name:            "nonsensitive_output_references_nonsensitive_var_ok",
		MustNotMatchRef: "variable.env",
	},
	{
		Name: "sensitive_propagation_deduplicated",
		Custom: func(t *testing.T, errs []analysis.ValidationError) {
			n := 0
			for _, e := range errs {
				if e.EntityID == "output.pw" && e.Ref == "variable.password" {
					n++
				}
			}
			if n != 1 {
				t.Errorf("expected exactly 1 sensitive-propagation error, got %d", n)
			}
		},
	},
	{
		Name: "multiple_files_aggregated",
		Custom: func(t *testing.T, _ []analysis.ValidationError) {
			// This case parses two separate files and merges them via
			// AnalyseFiles — outside the single-file fixture model. Run
			// directly here.
			f1 := parseToFile(t, "a.tf", "locals { a = var.missing }\n")
			f2 := parseToFile(t, "b.tf", "output \"x\" { value = local.ghost }\n")
			errs := analysis.AnalyseFiles([]*analysis.File{f1, f2}).Validate()
			refs := make(map[string]bool)
			for _, e := range errs {
				refs[e.Ref] = true
			}
			if !refs["variable.missing"] {
				t.Error("variable.missing should be flagged")
			}
			if !refs["local.ghost"] {
				t.Error("local.ghost should be flagged")
			}
		},
	},
}
