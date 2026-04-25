package diff_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
)

// diffCase describes one comparison case for TestDiffCases. Each case
// reads old.tf and new.tf from pkg/diff/testdata/<name>/ (a missing or
// empty file means "no Terraform on this side") and runs a diff. Most
// cases assert against a single change identified by its Subject; cases
// that need richer assertions use Custom.
type diffCase struct {
	// Name doubles as the testdata subdirectory and as the t.Run sub-test
	// name. Use snake_case.
	Name string

	// Subject is the change.Subject to look up (e.g. "variable.env",
	// "resource.aws_vpc.main", "terraform.backend"). Required unless
	// WantNoChanges or Custom is set.
	Subject string

	// WantKind is the expected kind of the change identified by Subject.
	WantKind diff.ChangeKind

	// DetailContains lists substrings that change.Detail must include.
	DetailContains []string

	// DetailExcludes lists substrings that change.Detail must NOT include.
	DetailExcludes []string

	// HintContains lists substrings that change.Hint must include. Use to
	// pin the most-important hints in place; cases that don't care leave
	// this empty (the hint just isn't asserted).
	HintContains []string

	// WantNoChanges asserts the diff produced zero changes.
	WantNoChanges bool

	// Custom is an escape hatch for cases that don't fit the
	// single-subject shape (multiple changes, "no message of a kind",
	// ordering checks, etc.). When set, Subject/WantKind/Detail are
	// ignored.
	Custom func(t *testing.T, changes []diff.Change)
}

func TestDiffCases(t *testing.T) {
	for _, tc := range diffCases {
		t.Run(tc.Name, func(t *testing.T) {
			oldSrc := loadFixture(t, tc.Name, "old.tf")
			newSrc := loadFixture(t, tc.Name, "new.tf")
			changes := diff.Diff(
				analyseFromTestdata(t, "old.tf", oldSrc),
				analyseFromTestdata(t, "new.tf", newSrc),
			)
			switch {
			case tc.Custom != nil:
				tc.Custom(t, changes)
			case tc.WantNoChanges:
				if len(changes) != 0 {
					t.Errorf("expected no changes, got: %v", changes)
				}
			default:
				assertSingleChange(t, changes, tc)
			}
		})
	}
}

// loadFixture reads testdata/<name>/<file>; missing or empty files yield
// an empty string (treated as "no Terraform on this side").
func loadFixture(t *testing.T, name, file string) string {
	t.Helper()
	path := filepath.Join("testdata", name, file)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// analyseFromTestdata parses src and runs analysis. Parse errors fail the
// test (testdata fixtures should be valid HCL); validation errors do not
// — fixtures may reference undeclared variables on purpose.
func analyseFromTestdata(t *testing.T, filename, src string) *analysis.Module {
	t.Helper()
	if src == "" {
		return analysis.AnalyseFiles(nil)
	}
	p := hclparse.NewParser()
	hclFile, diags := p.ParseHCL([]byte(src), filename)
	if diags.HasErrors() {
		t.Fatalf("parse %s: %s", filename, diags.Error())
	}
	body, ok := hclFile.Body.(*hclsyntax.Body)
	if !ok {
		t.Fatalf("%s: unexpected body type %T", filename, hclFile.Body)
	}
	return analysis.Analyse(&analysis.File{Filename: filename, Source: []byte(src), Body: body})
}

func assertSingleChange(t *testing.T, changes []diff.Change, tc diffCase) {
	t.Helper()
	c := findChange(changes, tc.Subject)
	if c == nil {
		t.Fatalf("expected change for %q; got: %v", tc.Subject, changes)
	}
	if c.Kind != tc.WantKind {
		t.Errorf("kind = %v, want %v; detail=%q", c.Kind, tc.WantKind, c.Detail)
	}
	for _, sub := range tc.DetailContains {
		if !strings.Contains(c.Detail, sub) {
			t.Errorf("detail should contain %q: %q", sub, c.Detail)
		}
	}
	for _, sub := range tc.DetailExcludes {
		if strings.Contains(c.Detail, sub) {
			t.Errorf("detail should not contain %q: %q", sub, c.Detail)
		}
	}
	for _, sub := range tc.HintContains {
		if !strings.Contains(c.Hint, sub) {
			t.Errorf("hint should contain %q: %q", sub, c.Hint)
		}
	}
}

// findChange returns the first Change whose Subject matches subject, or nil.
func findChange(changes []diff.Change, subject string) *diff.Change {
	for i := range changes {
		if changes[i].Subject == subject {
			return &changes[i]
		}
	}
	return nil
}

// ---- the cases ----
//
// Each entry below corresponds to a directory under pkg/diff/testdata/
// containing old.tf and new.tf. Keep entries grouped by topic and add new
// cases by appending here + dropping the two .tf files in place.

var diffCases = []diffCase{
	// ---- identical inputs ----
	{Name: "identical_no_changes", WantNoChanges: true},

	// ---- variables ----
	{
		Name: "variable_removed", Subject: "variable.env",
		WantKind: diff.Breaking, DetailContains: []string{"removed"},
	},
	{
		Name: "variable_optional_added", Subject: "variable.tags",
		WantKind: diff.NonBreaking,
	},
	{
		Name: "variable_required_added", Subject: "variable.region",
		WantKind: diff.Breaking, DetailContains: []string{"required"},
		HintContains: []string{"default = ..."},
	},
	{
		Name: "variable_default_removed", Subject: "variable.env",
		WantKind: diff.Breaking, DetailContains: []string{"default removed"},
		HintContains: []string{"keep the default"},
	},
	{
		Name: "variable_default_added", Subject: "variable.env",
		WantKind: diff.NonBreaking, DetailContains: []string{"default added"},
	},
	{
		Name: "variable_type_string_to_number", Subject: "variable.port",
		WantKind: diff.Breaking, DetailContains: []string{"string", "number"},
	},
	{
		Name: "variable_type_widened_to_any", Subject: "variable.port",
		WantKind: diff.NonBreaking, DetailContains: []string{"widened"},
	},
	{
		Name: "variable_list_element_changed", Subject: "variable.ports",
		WantKind: diff.Breaking,
	},
	{
		Name: "variable_list_widened_to_any", Subject: "variable.ports",
		WantKind: diff.NonBreaking, DetailContains: []string{"widened"},
	},
	{
		Name: "variable_map_widened_to_any", Subject: "variable.tags",
		WantKind: diff.NonBreaking,
	},
	{
		Name: "variable_any_narrowed_to_string", Subject: "variable.x",
		WantKind: diff.Breaking, DetailContains: []string{"narrowed"},
	},
	{
		Name: "variable_object_field_inner_widened_to_any",
		Custom: func(t *testing.T, changes []diff.Change) {
			var fc *diff.Change
			for i := range changes {
				if strings.Contains(changes[i].Detail, `field "a"`) {
					fc = &changes[i]
					break
				}
			}
			if fc == nil {
				t.Fatalf("expected per-field change for object field a; got: %v", changes)
			}
			if fc.Kind != diff.NonBreaking {
				t.Errorf("kind = %v, want NonBreaking; detail=%q", fc.Kind, fc.Detail)
			}
			if !strings.Contains(fc.Detail, "widened") {
				t.Errorf("detail should say widened: %q", fc.Detail)
			}
		},
	},
	{
		Name: "variable_type_change_default_still_valid",
		Custom: func(t *testing.T, changes []diff.Change) {
			for _, c := range changes {
				if c.Kind == diff.Informational && strings.Contains(c.Detail, "default value remains valid") {
					return
				}
			}
			t.Errorf("expected an Informational 'default value remains valid' change; got: %v", changes)
		},
	},
	{
		Name: "variable_type_narrowed_rejects_default",
		Custom: func(t *testing.T, changes []diff.Change) {
			for _, c := range changes {
				if c.Kind == diff.Informational && strings.Contains(c.Detail, "default value remains valid") {
					t.Errorf("did not expect default-still-valid info; got: %v", c)
				}
			}
		},
	},
	{
		Name: "variable_nullable_false_added", Subject: "variable.x",
		WantKind: diff.Breaking, DetailContains: []string{"nullable"},
		HintContains: []string{"callers passing null are now rejected"},
	},
	{
		Name: "variable_nullable_false_removed", Subject: "variable.x",
		WantKind: diff.NonBreaking,
	},
	{
		Name: "variable_sensitive_added", Subject: "variable.x",
		WantKind: diff.Breaking, DetailContains: []string{"sensitive"},
		HintContains: []string{"must also be marked sensitive"},
	},
	{
		Name: "variable_ephemeral_added", Subject: "variable.tok",
		WantKind: diff.Breaking, DetailContains: []string{"ephemeral"},
		HintContains: []string{"ephemeral"},
	},
	{
		Name: "variable_ephemeral_removed", Subject: "variable.tok",
		WantKind: diff.NonBreaking,
	},
	{
		Name: "variable_validation_added", Subject: "variable.x",
		WantKind: diff.Informational, DetailContains: []string{"validation"},
	},
	{
		Name: "variable_precondition_added", Subject: "variable.x",
		WantKind: diff.Informational, DetailContains: []string{"precondition"},
	},
	{
		Name: "variable_validation_condition_replaced",
		Custom: func(t *testing.T, changes []diff.Change) {
			var sawAdded, sawRemoved bool
			for _, c := range changes {
				if c.Subject != "variable.x" {
					continue
				}
				if strings.Contains(c.Detail, "new validation block") {
					sawAdded = true
				}
				if strings.Contains(c.Detail, "validation block(s) removed") {
					sawRemoved = true
				}
			}
			if !sawAdded || !sawRemoved {
				t.Errorf("expected both added and removed validation messages; sawAdded=%v sawRemoved=%v changes=%v",
					sawAdded, sawRemoved, changes)
			}
		},
	},
	{
		Name: "variable_validation_reordered_no_change",
		Custom: func(t *testing.T, changes []diff.Change) {
			for _, c := range changes {
				if strings.Contains(c.Detail, "validation block") {
					t.Errorf("reordered validations should not produce a change, got: %v", c)
				}
			}
		},
	},

	// ---- outputs ----
	{
		Name: "output_removed", Subject: "output.id",
		WantKind:     diff.Breaking,
		HintContains: []string{"module.X.id"},
	},
	{
		Name: "output_added", Subject: "output.id",
		WantKind: diff.NonBreaking,
	},
	{
		Name: "output_sensitive_added", Subject: "output.x",
		WantKind: diff.Informational,
	},
	{
		Name: "output_sensitive_removed_is_leak", Subject: "output.x",
		WantKind: diff.Breaking, DetailContains: []string{"leak"},
		HintContains: []string{"restore `sensitive = true`"},
	},
	{
		Name: "output_postcondition_added", Subject: "output.x",
		WantKind: diff.Informational, DetailContains: []string{"postcondition"},
	},
	{
		Name: "output_value_expression_changed_unknown_type", Subject: "output.id",
		WantKind:       diff.Informational,
		DetailContains: []string{"value expression changed", "id", "arn"},
	},
	{
		Name:          "output_value_expression_identical_no_change",
		WantNoChanges: true,
	},
	{
		Name: "output_template_change", Subject: "output.name",
		DetailContains: []string{"value expression changed"},
		Custom: func(t *testing.T, changes []diff.Change) {
			c := findChange(changes, "output.name")
			if c == nil {
				t.Fatalf("expected change, got: %v", changes)
			}
			if !strings.Contains(c.Detail, "value expression changed") {
				t.Errorf("detail should mention value change: %q", c.Detail)
			}
		},
	},
	{
		Name: "output_type_narrowed", Subject: "output.name",
		WantKind:       diff.Breaking,
		DetailContains: []string{"output type changed", "string"},
	},
	{
		Name: "output_referencing_local_changed", Subject: "output.name",
		WantKind:       diff.Informational,
		DetailContains: []string{"local.prefix", "old", "new"},
	},
	{
		Name: "output_referencing_unchanged_local", WantNoChanges: true,
	},
	{
		Name: "output_value_change_shadows_indirect",
		Custom: func(t *testing.T, changes []diff.Change) {
			n := 0
			for _, c := range changes {
				if c.Subject == "output.x" {
					n++
				}
			}
			if n != 1 {
				t.Errorf("expected exactly 1 change for output.x, got %d: %v", n, changes)
			}
		},
	},
	{
		Name: "depends_on_changed_on_output", Subject: "output.x",
		WantKind:       diff.Informational,
		DetailContains: []string{"aws_vpc.old", "aws_vpc.new"},
	},

	// ---- resources / data sources / modules ----
	{
		Name: "resource_removed", Subject: "resource.aws_vpc.main",
		WantKind: diff.Breaking, DetailContains: []string{"removed"},
	},
	{
		Name: "resource_added", Subject: "resource.aws_vpc.main",
		WantKind: diff.Informational,
	},
	{
		Name: "resource_renamed", Subject: "resource.aws_vpc.old_name → resource.aws_vpc.new_name",
		WantKind: diff.Breaking, DetailContains: []string{"moved"},
		HintContains: []string{"moved { from = resource.aws_vpc.old_name, to = resource.aws_vpc.new_name }"},
	},
	{
		Name: "resource_type_change_not_rename",
		Custom: func(t *testing.T, changes []diff.Change) {
			if findChange(changes, "resource.aws_vpc.main") == nil {
				t.Error("expected removal of aws_vpc.main")
			}
			if findChange(changes, "resource.aws_subnet.main") == nil {
				t.Error("expected addition of aws_subnet.main")
			}
			for _, c := range changes {
				if strings.Contains(c.Subject, "→") {
					t.Errorf("should not pair across different types: %v", c)
				}
			}
		},
	},
	{
		Name: "resource_multiple_same_type_no_rename",
		Custom: func(t *testing.T, changes []diff.Change) {
			for _, c := range changes {
				if strings.Contains(c.Subject, "→") {
					t.Errorf("should not pair ambiguous same-type changes: %v", c)
				}
			}
		},
	},
	{
		Name: "depends_on_added_on_resource", Subject: "resource.aws_vpc.main",
		WantKind: diff.Informational, DetailContains: []string{"depends_on"},
	},
	{
		Name: "depends_on_unchanged_no_change", WantNoChanges: true,
	},
	{
		Name: "data_source_removed", Subject: "data.aws_ami.ubuntu",
		WantKind: diff.Breaking,
	},
	{
		Name: "module_renamed", Subject: "module.net → module.network",
		WantKind: diff.Breaking,
	},
	{
		Name: "module_argument_added", Subject: "module.net",
		WantKind: diff.Informational, DetailContains: []string{"region", "added"},
	},
	{
		Name: "module_argument_removed", Subject: "module.net",
		WantKind: diff.Informational, DetailContains: []string{"region", "removed"},
	},
	{
		Name: "module_argument_value_changed", Subject: "module.net",
		WantKind: diff.Informational, DetailContains: []string{"cidr", "value changed"},
	},
	{
		Name: "module_meta_args_not_treated_as_arguments",
		Custom: func(t *testing.T, changes []diff.Change) {
			for _, c := range changes {
				if strings.Contains(c.Detail, `argument "count"`) || strings.Contains(c.Detail, `argument "depends_on"`) {
					t.Errorf("meta-arg should not be treated as a user argument: %v", c)
				}
			}
		},
	},
	{
		Name: "module_source_changed", Subject: "module.net",
		WantKind: diff.Informational, DetailContains: []string{"source", "v1", "v2"},
	},
	{
		Name: "module_exact_version_disjoint", Subject: "module.net",
		WantKind: diff.Breaking,
		Custom: func(t *testing.T, changes []diff.Change) {
			c := findChange(changes, "module.net")
			if c == nil {
				t.Fatal("expected change for module.net")
			}
			if c.Kind != diff.Breaking {
				t.Errorf("kind = %v, want Breaking", c.Kind)
			}
			if !strings.Contains(c.Detail, "incompatible") && !strings.Contains(c.Detail, "no overlap") {
				t.Errorf("detail should mention incompatibility: %q", c.Detail)
			}
		},
	},
	{
		Name: "module_version_added_is_narrowing", Subject: "module.net",
		WantKind: diff.Breaking, DetailContains: []string{"tightened"},
	},
	{
		Name: "module_source_unchanged_no_change", WantNoChanges: true,
	},

	// ---- count / for_each ----
	{
		Name: "count_to_for_each", Subject: "resource.aws_subnet.pub",
		WantKind: diff.Breaking, DetailContains: []string{"count", "for_each"},
		HintContains: []string{"moved {}"},
	},
	{
		Name: "for_each_to_count", Subject: "resource.aws_subnet.pub",
		WantKind: diff.Breaking,
	},
	{
		Name: "count_added_to_singleton", Subject: "resource.aws_vpc.main",
		WantKind: diff.Breaking, DetailContains: []string{"single instance", "count"},
	},
	{
		Name: "for_each_removed_from_resource", Subject: "resource.aws_vpc.main",
		WantKind: diff.Breaking,
	},
	{
		Name: "for_each_expression_changed", Subject: "resource.aws_iam_user.u",
		WantKind:       diff.Informational,
		DetailContains: []string{"for_each expression changed", "var.a", "var.b"},
	},
	{
		Name: "count_expression_changed", Subject: "resource.aws_instance.w",
		WantKind: diff.Informational, DetailContains: []string{"count expression changed"},
	},
	{
		Name: "for_each_identical_no_change", WantNoChanges: true,
	},
	{
		Name: "for_each_key_type_narrowed", Subject: "resource.aws_iam_user.u",
		WantKind: diff.Breaking, DetailContains: []string{"for_each key type"},
	},
	{
		Name: "for_each_set_to_map_string_keys",
		Custom: func(t *testing.T, changes []diff.Change) {
			for _, c := range changes {
				if c.Subject == "resource.aws_iam_user.u" && c.Kind == diff.Breaking &&
					strings.Contains(c.Detail, "for_each key type") {
					t.Errorf("set(string) → map(string) should not be a for_each key-type breaking change: %q", c.Detail)
				}
			}
		},
	},
	{
		Name: "count_type_narrowed_to_list", Subject: "resource.aws_instance.w",
		WantKind: diff.Breaking, DetailContains: []string{"count expression type"},
	},

	// ---- lifecycle ----
	{
		Name: "lifecycle_prevent_destroy_added", Subject: "resource.aws_vpc.main",
		WantKind: diff.Informational, DetailContains: []string{"prevent_destroy"},
	},
	{
		Name: "lifecycle_prevent_destroy_removed", Subject: "resource.aws_vpc.main",
		WantKind: diff.Informational, DetailContains: []string{"can now be destroyed"},
	},
	{
		Name: "lifecycle_create_before_destroy_added", Subject: "resource.aws_vpc.main",
		WantKind: diff.Informational, DetailContains: []string{"create_before_destroy"},
	},
	{
		Name: "lifecycle_ignore_changes_changed", Subject: "resource.aws_vpc.main",
		WantKind:       diff.Informational,
		DetailContains: []string{"ignore_changes", "cidr_block"},
	},
	{
		Name: "lifecycle_unchanged_no_change", WantNoChanges: true,
	},
	{
		Name: "lifecycle_ignore_changes_all_narrowed", Subject: "resource.aws_vpc.main",
		WantKind: diff.Breaking, DetailContains: []string{"narrowed"},
		HintContains: []string{"drift detection"},
	},
	{
		Name: "lifecycle_ignore_changes_widened_to_all", Subject: "resource.aws_vpc.main",
		WantKind: diff.NonBreaking, DetailContains: []string{"widened"},
	},
	{
		Name: "lifecycle_precondition_added", Subject: "resource.aws_vpc.main",
		WantKind: diff.Informational, DetailContains: []string{"precondition"},
	},

	// ---- providers (resource-level) ----
	{
		Name: "resource_provider_alias_changed", Subject: "resource.aws_vpc.main",
		WantKind: diff.Breaking, DetailContains: []string{"aws.east", "aws.west"},
	},
	{
		Name: "resource_provider_added", Subject: "resource.aws_vpc.main",
		WantKind: diff.Breaking, DetailContains: []string{"<default>", "aws.east"},
	},
	{
		Name: "resource_provider_unchanged_no_change", WantNoChanges: true,
	},

	// ---- terraform block ----
	{
		Name: "required_version_tightened", Subject: "terraform.required_version",
		WantKind: diff.Breaking, DetailContains: []string{"tightened"},
	},
	{
		Name: "required_provider_added", Subject: "provider.aws",
		WantKind: diff.Breaking, DetailContains: []string{"added"},
	},
	{
		Name: "required_provider_removed", Subject: "provider.aws",
		WantKind: diff.NonBreaking,
	},
	{
		Name: "provider_source_changed", Subject: "provider.aws",
		WantKind:       diff.Breaking,
		DetailContains: []string{"hashicorp/aws", "myorg/aws-fork"},
	},
	{
		Name: "provider_version_tightened", Subject: "provider.aws",
		WantKind: diff.Breaking, DetailContains: []string{"4.0", "5.0"},
	},
	{
		Name: "backend_added", Subject: "terraform.backend",
		WantKind: diff.Breaking, DetailContains: []string{"s3"},
		HintContains: []string{"terraform init -migrate-state"},
	},
	{
		Name: "backend_type_changed", Subject: "terraform.backend",
		WantKind: diff.Breaking, DetailContains: []string{"backend type changed"},
		HintContains: []string{"terraform init -migrate-state"},
	},
	{
		Name: "backend_config_key_changed", Subject: "terraform.backend",
		WantKind: diff.Breaking,
	},

	// ---- object field-level diff ----
	{
		Name: "object_field_added_required", Subject: "variable.cfg",
		WantKind: diff.Breaking, DetailContains: []string{`"b"`, "required"},
		HintContains: []string{"optional("},
	},
	{
		Name: "object_field_added_optional", Subject: "variable.cfg",
		WantKind: diff.NonBreaking, DetailContains: []string{"optional"},
	},
	{
		Name: "object_field_removed", Subject: "variable.cfg",
		WantKind: diff.Breaking, DetailContains: []string{"removed", `"b"`},
	},
	{
		Name: "object_field_optional_to_required", Subject: "variable.cfg",
		WantKind: diff.Breaking, DetailContains: []string{"required"},
	},
	{
		Name: "object_field_required_to_optional", Subject: "variable.cfg",
		WantKind: diff.NonBreaking, DetailContains: []string{"optional"},
	},
	{
		Name: "object_field_inner_type_changed", Subject: "variable.cfg",
		WantKind: diff.Breaking, DetailContains: []string{"string", "number"},
	},
	{
		Name: "object_identical_no_change", WantNoChanges: true,
	},

	// ---- moved / removed blocks ----
	{
		Name:     "moved_block_suppresses_rename",
		Subject:  "resource.aws_vpc.old_name → resource.aws_vpc.new_name",
		WantKind: diff.Informational, DetailContains: []string{"moved"},
	},
	{
		Name:     "moved_block_for_module",
		Subject:  "module.net → module.network",
		WantKind: diff.Informational,
	},
	{
		Name:     "removed_block_downgrades_removal",
		Subject:  "resource.aws_vpc.legacy",
		WantKind: diff.Informational, DetailContains: []string{"removed"},
	},
	{
		Name:     "moved_block_mismatch_still_breaking",
		Subject:  "resource.aws_vpc.old_name → resource.aws_vpc.new_name",
		WantKind: diff.Breaking,
	},

	// ---- sorting ----
	{
		Name: "changes_sorted_breaking_first",
		Custom: func(t *testing.T, changes []diff.Change) {
			if len(changes) == 0 {
				t.Fatal("expected some changes")
			}
			for i := 1; i < len(changes); i++ {
				if changes[i-1].Kind > changes[i].Kind {
					t.Errorf("changes out of order: %v before %v", changes[i-1], changes[i])
				}
			}
		},
	},
}
