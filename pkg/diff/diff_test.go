package diff_test

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
)

func analyse(t *testing.T, filename, src string) *analysis.Module {
	t.Helper()
	p := hclparse.NewParser()
	hclFile, diags := p.ParseHCL([]byte(src), filename)
	for _, d := range diags {
		t.Errorf("parse error: %s", d.Error())
	}
	if t.Failed() {
		t.FailNow()
	}
	body, ok := hclFile.Body.(*hclsyntax.Body)
	if !ok {
		t.Fatalf("unexpected body type %T", hclFile.Body)
	}
	return analysis.Analyse(&analysis.File{Filename: filename, Source: []byte(src), Body: body})
}

func diffFixture(t *testing.T, oldSrc, newSrc string) []diff.Change {
	t.Helper()
	return diff.Diff(
		analyse(t, "old.tf", oldSrc),
		analyse(t, "new.tf", newSrc),
	)
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

// ---- identical ----

func TestNoChangesForIdentical(t *testing.T) {
	src := `
variable "env" { type = string, default = "dev" }
resource "aws_vpc" "main" { cidr_block = "10.0.0.0/16" }
output "id" { value = aws_vpc.main.id }
`
	// Trailing commas aren't valid HCL, use newlines.
	src = `
variable "env" {
  type    = string
  default = "dev"
}
resource "aws_vpc" "main" { cidr_block = "10.0.0.0/16" }
output "id" { value = aws_vpc.main.id }
`
	if cs := diffFixture(t, src, src); len(cs) != 0 {
		t.Errorf("identical modules should produce no changes, got: %v", cs)
	}
}

// ---- variables ----

func TestVariableRemovedIsBreaking(t *testing.T) {
	oldSrc := `variable "env" { default = "dev" }`
	newSrc := ``
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.env")
	if c == nil {
		t.Fatal("expected change for variable.env, got none")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "removed") {
		t.Errorf("detail should say 'removed': %q", c.Detail)
	}
}

func TestOptionalVariableAddedIsNonBreaking(t *testing.T) {
	oldSrc := ``
	newSrc := `variable "tags" { default = {} }`
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.tags")
	if c == nil {
		t.Fatal("expected change for variable.tags")
	}
	if c.Kind != diff.NonBreaking {
		t.Errorf("kind = %v, want NonBreaking", c.Kind)
	}
}

func TestRequiredVariableAddedIsBreaking(t *testing.T) {
	oldSrc := ``
	newSrc := `variable "region" { type = string }`
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.region")
	if c == nil {
		t.Fatal("expected change for variable.region")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "required") {
		t.Errorf("detail should mention required: %q", c.Detail)
	}
}

func TestVariableDefaultRemovedIsBreaking(t *testing.T) {
	oldSrc := `variable "env" { type = string, default = "dev" }`
	oldSrc = "variable \"env\" {\n  type    = string\n  default = \"dev\"\n}\n"
	newSrc := "variable \"env\" {\n  type = string\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.env")
	if c == nil {
		t.Fatal("expected change for variable.env")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking; detail=%q", c.Kind, c.Detail)
	}
	if !strings.Contains(c.Detail, "default removed") {
		t.Errorf("detail should mention default removed: %q", c.Detail)
	}
}

func TestVariableDefaultAddedIsNonBreaking(t *testing.T) {
	oldSrc := "variable \"env\" {\n  type = string\n}\n"
	newSrc := "variable \"env\" {\n  type    = string\n  default = \"dev\"\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.env")
	if c == nil {
		t.Fatal("expected change for variable.env")
	}
	if c.Kind != diff.NonBreaking {
		t.Errorf("kind = %v, want NonBreaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "default added") {
		t.Errorf("detail should mention default added: %q", c.Detail)
	}
}

func TestVariableTypeChangedIsBreaking(t *testing.T) {
	oldSrc := "variable \"port\" {\n  type = string\n}\n"
	newSrc := "variable \"port\" {\n  type = number\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.port")
	if c == nil {
		t.Fatal("expected change for variable.port")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "string") || !strings.Contains(c.Detail, "number") {
		t.Errorf("detail should mention both types: %q", c.Detail)
	}
}

func TestVariableWidenedToAnyIsNonBreaking(t *testing.T) {
	oldSrc := "variable \"port\" {\n  type = string\n}\n"
	newSrc := "variable \"port\" {\n  type = any\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.port")
	if c == nil {
		t.Fatal("expected change for variable.port")
	}
	if c.Kind != diff.NonBreaking {
		t.Errorf("kind = %v, want NonBreaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "widened") {
		t.Errorf("detail should say widened: %q", c.Detail)
	}
}

func TestVariableListTypeElementChangeIsBreaking(t *testing.T) {
	oldSrc := "variable \"ports\" {\n  type = list(string)\n}\n"
	newSrc := "variable \"ports\" {\n  type = list(number)\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.ports")
	if c == nil {
		t.Fatal("expected change for variable.ports")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
}

func TestVariableListElementWidenedToAnyIsNonBreaking(t *testing.T) {
	// list(string) → list(any): old values still satisfy the new type.
	oldSrc := "variable \"ports\" {\n  type = list(string)\n}\n"
	newSrc := "variable \"ports\" {\n  type = list(any)\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.ports")
	if c == nil {
		t.Fatal("expected change for variable.ports")
	}
	if c.Kind != diff.NonBreaking {
		t.Errorf("kind = %v, want NonBreaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "widened") {
		t.Errorf("detail should say widened: %q", c.Detail)
	}
}

func TestVariableMapElementWidenedToAnyIsNonBreaking(t *testing.T) {
	oldSrc := "variable \"tags\" {\n  type = map(string)\n}\n"
	newSrc := "variable \"tags\" {\n  type = map(any)\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.tags")
	if c == nil {
		t.Fatal("expected change for variable.tags")
	}
	if c.Kind != diff.NonBreaking {
		t.Errorf("kind = %v, want NonBreaking", c.Kind)
	}
}

func TestVariableAnyNarrowedToConcreteIsBreaking(t *testing.T) {
	// any → string: callers who passed a non-string value now break.
	oldSrc := "variable \"x\" {\n  type = any\n}\n"
	newSrc := "variable \"x\" {\n  type = string\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.x")
	if c == nil {
		t.Fatal("expected change for variable.x")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "narrowed") {
		t.Errorf("detail should say narrowed: %q", c.Detail)
	}
}

func TestObjectFieldInnerWidenedToAnyIsNonBreaking(t *testing.T) {
	// object({a=string}) → object({a=any}): a's type widened, callers fine.
	oldSrc := "variable \"o\" {\n  type = object({ a = string })\n}\n"
	newSrc := "variable \"o\" {\n  type = object({ a = any })\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	if len(changes) == 0 {
		t.Fatal("expected at least one change for variable.o")
	}
	// Find the per-field message.
	var fieldChange *diff.Change
	for i := range changes {
		if strings.Contains(changes[i].Detail, "field \"a\"") {
			fieldChange = &changes[i]
			break
		}
	}
	if fieldChange == nil {
		t.Fatalf("expected per-field change for object field a; got: %v", changes)
	}
	if fieldChange.Kind != diff.NonBreaking {
		t.Errorf("kind = %v, want NonBreaking; detail=%q", fieldChange.Kind, fieldChange.Detail)
	}
	if !strings.Contains(fieldChange.Detail, "widened") {
		t.Errorf("detail should say widened: %q", fieldChange.Detail)
	}
}

func TestVariableTypeChangeWithDefaultStillValidEmitsInfo(t *testing.T) {
	// Type widens from string → any; the literal "dev" default still works.
	oldSrc := "variable \"env\" {\n  type    = string\n  default = \"dev\"\n}\n"
	newSrc := "variable \"env\" {\n  type    = any\n  default = \"dev\"\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)

	var info *diff.Change
	for i := range changes {
		if changes[i].Kind == diff.Informational && strings.Contains(changes[i].Detail, "default value remains valid") {
			info = &changes[i]
			break
		}
	}
	if info == nil {
		t.Fatalf("expected an Informational change about the default still being valid; got: %v", changes)
	}
}

func TestVariableTypeNarrowedRejectsExistingDefault(t *testing.T) {
	// any → number AND default is the literal "dev" (a string). The default
	// no longer converts to the new type, so the "default still valid" info
	// must NOT be emitted; only the breaking type-narrow is.
	oldSrc := "variable \"x\" {\n  type    = any\n  default = \"dev\"\n}\n"
	newSrc := "variable \"x\" {\n  type    = number\n  default = \"dev\"\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	for _, c := range changes {
		if c.Kind == diff.Informational && strings.Contains(c.Detail, "default value remains valid") {
			t.Errorf("did not expect default-still-valid info; got: %v", c)
		}
	}
}

// ---- outputs ----

func TestOutputRemovedIsBreaking(t *testing.T) {
	oldSrc := `output "id" { value = "x" }`
	newSrc := ``
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "output.id")
	if c == nil {
		t.Fatal("expected change for output.id")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
}

func TestOutputAddedIsNonBreaking(t *testing.T) {
	oldSrc := ``
	newSrc := `output "id" { value = "x" }`
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "output.id")
	if c == nil {
		t.Fatal("expected change for output.id")
	}
	if c.Kind != diff.NonBreaking {
		t.Errorf("kind = %v, want NonBreaking", c.Kind)
	}
}

// ---- resources ----

func TestResourceRemovedIsBreaking(t *testing.T) {
	oldSrc := `resource "aws_vpc" "main" {}`
	newSrc := ``
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_vpc.main")
	if c == nil {
		t.Fatal("expected change for resource.aws_vpc.main")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "removed") {
		t.Errorf("detail should say 'removed': %q", c.Detail)
	}
}

func TestResourceAddedIsInformational(t *testing.T) {
	oldSrc := ``
	newSrc := `resource "aws_vpc" "main" {}`
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_vpc.main")
	if c == nil {
		t.Fatal("expected change for resource.aws_vpc.main")
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
}

func TestResourceRenameDetected(t *testing.T) {
	oldSrc := `resource "aws_vpc" "old_name" {}`
	newSrc := `resource "aws_vpc" "new_name" {}`
	changes := diffFixture(t, oldSrc, newSrc)
	renameSubject := "resource.aws_vpc.old_name → resource.aws_vpc.new_name"
	c := findChange(changes, renameSubject)
	if c == nil {
		t.Fatalf("expected rename change, got: %v", changes)
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "moved") {
		t.Errorf("detail should mention moved block: %q", c.Detail)
	}
}

func TestResourceTypeChangeIsNotTreatedAsRename(t *testing.T) {
	// Different types — not a rename, reported as remove + add.
	oldSrc := `resource "aws_vpc" "main" {}`
	newSrc := `resource "aws_subnet" "main" {}`
	changes := diffFixture(t, oldSrc, newSrc)
	if findChange(changes, "resource.aws_vpc.main") == nil {
		t.Error("expected removal of aws_vpc.main")
	}
	if findChange(changes, "resource.aws_subnet.main") == nil {
		t.Error("expected addition of aws_subnet.main")
	}
	// No rename subject with arrow.
	for _, c := range changes {
		if strings.Contains(c.Subject, "→") {
			t.Errorf("should not pair across different types: %v", c)
		}
	}
}

func TestMultipleSameTypeNotPairedAsRename(t *testing.T) {
	// 2 removed + 2 added of same type can't be unambiguously paired.
	oldSrc := "resource \"aws_vpc\" \"a\" {}\nresource \"aws_vpc\" \"b\" {}\n"
	newSrc := "resource \"aws_vpc\" \"c\" {}\nresource \"aws_vpc\" \"d\" {}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	for _, c := range changes {
		if strings.Contains(c.Subject, "→") {
			t.Errorf("should not pair ambiguous same-type changes: %v", c)
		}
	}
}

// ---- depends_on ----

func TestDependsOnAddedOnResource(t *testing.T) {
	oldSrc := `resource "aws_vpc" "main" {}`
	newSrc := "resource \"aws_vpc\" \"main\" {\n  depends_on = [aws_account.setup]\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_vpc.main")
	if c == nil {
		t.Fatalf("expected change, got: %v", changes)
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "depends_on") {
		t.Errorf("detail should mention depends_on: %q", c.Detail)
	}
}

func TestDependsOnChangedOnOutput(t *testing.T) {
	oldSrc := "output \"x\" {\n  value      = \"v\"\n  depends_on = [aws_vpc.old]\n}\n"
	newSrc := "output \"x\" {\n  value      = \"v\"\n  depends_on = [aws_vpc.new]\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "output.x")
	if c == nil {
		t.Fatalf("expected change, got: %v", changes)
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "aws_vpc.old") || !strings.Contains(c.Detail, "aws_vpc.new") {
		t.Errorf("detail should include both sides: %q", c.Detail)
	}
}

func TestDependsOnUnchangedProducesNoChange(t *testing.T) {
	src := "resource \"aws_vpc\" \"main\" {\n  depends_on = [aws_account.setup, aws_region.default]\n}\n"
	if cs := diffFixture(t, src, src); len(cs) != 0 {
		t.Errorf("identical depends_on should produce no changes, got: %v", cs)
	}
}

// ---- lifecycle ----

func TestLifecyclePreventDestroyAddedIsInformational(t *testing.T) {
	oldSrc := `resource "aws_vpc" "main" {}`
	newSrc := "resource \"aws_vpc\" \"main\" {\n  lifecycle {\n    prevent_destroy = true\n  }\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_vpc.main")
	if c == nil {
		t.Fatalf("expected change, got: %v", changes)
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "prevent_destroy") {
		t.Errorf("detail should mention prevent_destroy: %q", c.Detail)
	}
}

func TestLifecyclePreventDestroyRemovedIsInformational(t *testing.T) {
	oldSrc := "resource \"aws_vpc\" \"main\" {\n  lifecycle {\n    prevent_destroy = true\n  }\n}\n"
	newSrc := `resource "aws_vpc" "main" {}`
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_vpc.main")
	if c == nil {
		t.Fatal("expected change")
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "can now be destroyed") {
		t.Errorf("detail should note destroy now allowed: %q", c.Detail)
	}
}

func TestLifecycleCreateBeforeDestroyAddedIsInformational(t *testing.T) {
	oldSrc := `resource "aws_vpc" "main" {}`
	newSrc := "resource \"aws_vpc\" \"main\" {\n  lifecycle {\n    create_before_destroy = true\n  }\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_vpc.main")
	if c == nil {
		t.Fatal("expected change")
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "create_before_destroy") {
		t.Errorf("detail should mention create_before_destroy: %q", c.Detail)
	}
}

func TestLifecycleIgnoreChangesChangedIsInformational(t *testing.T) {
	oldSrc := "resource \"aws_vpc\" \"main\" {\n  lifecycle {\n    ignore_changes = [tags]\n  }\n}\n"
	newSrc := "resource \"aws_vpc\" \"main\" {\n  lifecycle {\n    ignore_changes = [tags, cidr_block]\n  }\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_vpc.main")
	if c == nil {
		t.Fatalf("expected change, got: %v", changes)
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "ignore_changes") {
		t.Errorf("detail should mention ignore_changes: %q", c.Detail)
	}
	if !strings.Contains(c.Detail, "cidr_block") {
		t.Errorf("detail should show the changed list: %q", c.Detail)
	}
}

func TestLifecycleUnchangedProducesNoChange(t *testing.T) {
	src := "resource \"aws_vpc\" \"main\" {\n  lifecycle {\n    prevent_destroy       = true\n    create_before_destroy = true\n    ignore_changes        = [tags]\n  }\n}\n"
	if cs := diffFixture(t, src, src); len(cs) != 0 {
		t.Errorf("identical lifecycle should produce no changes, got: %v", cs)
	}
}

// ---- for_each / count expression content ----

func TestForEachExpressionChangedIsInformational(t *testing.T) {
	oldSrc := "resource \"aws_iam_user\" \"u\" {\n  for_each = var.a\n}\n"
	newSrc := "resource \"aws_iam_user\" \"u\" {\n  for_each = var.b\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_iam_user.u")
	if c == nil {
		t.Fatalf("expected change, got: %v", changes)
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "for_each expression changed") {
		t.Errorf("detail should mention for_each: %q", c.Detail)
	}
	if !strings.Contains(c.Detail, "var.a") || !strings.Contains(c.Detail, "var.b") {
		t.Errorf("detail should show both sides: %q", c.Detail)
	}
}

func TestCountExpressionChangedIsInformational(t *testing.T) {
	oldSrc := "resource \"aws_instance\" \"w\" {\n  count = 3\n}\n"
	newSrc := "resource \"aws_instance\" \"w\" {\n  count = var.n\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_instance.w")
	if c == nil {
		t.Fatal("expected change")
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "count expression changed") {
		t.Errorf("detail should mention count: %q", c.Detail)
	}
}

func TestForEachIdenticalExpressionProducesNoChange(t *testing.T) {
	src := "resource \"aws_iam_user\" \"u\" {\n  for_each = toset(var.names)\n}\n"
	if cs := diffFixture(t, src, src); len(cs) != 0 {
		t.Errorf("identical for_each expression should produce no changes, got: %v", cs)
	}
}

func TestForEachKeyTypeNarrowedIsBreaking(t *testing.T) {
	// for_each driven by a typed variable. Old keys are strings (set element
	// type = string); new keys are numbers — every instance would be
	// re-addressed.
	oldSrc := "variable \"k\" { type = set(string) }\n" +
		"resource \"aws_iam_user\" \"u\" { for_each = var.k }\n"
	newSrc := "variable \"k\" { type = set(number) }\n" +
		"resource \"aws_iam_user\" \"u\" { for_each = var.k }\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_iam_user.u")
	if c == nil {
		t.Fatalf("expected change, got: %v", changes)
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking; detail=%q", c.Kind, c.Detail)
	}
	if !strings.Contains(c.Detail, "for_each key type") {
		t.Errorf("detail should mention key type: %q", c.Detail)
	}
}

func TestForEachSetToMapKeepsStringKeysIsInformational(t *testing.T) {
	// set(string) → map(string): both key by string. Should remain
	// Informational (no key-type change), not upgraded to Breaking.
	oldSrc := "variable \"k\" { type = set(string) }\n" +
		"resource \"aws_iam_user\" \"u\" { for_each = var.k }\n"
	newSrc := "variable \"k\" { type = map(string) }\n" +
		"resource \"aws_iam_user\" \"u\" { for_each = var.k }\n"
	changes := diffFixture(t, oldSrc, newSrc)
	for _, c := range changes {
		if c.Subject == "resource.aws_iam_user.u" && c.Kind == diff.Breaking {
			// We accept the type-change Breaking on the variable itself but
			// the resource's for_each should NOT be flagged as a key-type
			// breaking change.
			if strings.Contains(c.Detail, "for_each key type") {
				t.Errorf("set(string) → map(string) should not be a for_each key-type breaking change: %q", c.Detail)
			}
		}
	}
}

// ---- count / for_each ----

func TestCountToForEachIsBreaking(t *testing.T) {
	oldSrc := "resource \"aws_subnet\" \"pub\" {\n  count = 3\n}\n"
	newSrc := "resource \"aws_subnet\" \"pub\" {\n  for_each = { a = 1 }\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_subnet.pub")
	if c == nil {
		t.Fatal("expected change for aws_subnet.pub")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "count") || !strings.Contains(c.Detail, "for_each") {
		t.Errorf("detail should mention both: %q", c.Detail)
	}
}

func TestForEachToCountIsBreaking(t *testing.T) {
	oldSrc := "resource \"aws_subnet\" \"pub\" {\n  for_each = { a = 1 }\n}\n"
	newSrc := "resource \"aws_subnet\" \"pub\" {\n  count = 3\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_subnet.pub")
	if c == nil {
		t.Fatal("expected change for aws_subnet.pub")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
}

func TestCountAddedToSingletonIsBreaking(t *testing.T) {
	oldSrc := `resource "aws_vpc" "main" {}`
	newSrc := "resource \"aws_vpc\" \"main\" {\n  count = 1\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_vpc.main")
	if c == nil {
		t.Fatal("expected change for aws_vpc.main")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "single instance") || !strings.Contains(c.Detail, "count") {
		t.Errorf("detail should mention transition: %q", c.Detail)
	}
}

func TestForEachRemovedIsBreaking(t *testing.T) {
	oldSrc := "resource \"aws_vpc\" \"main\" {\n  for_each = { a = 1 }\n}\n"
	newSrc := `resource "aws_vpc" "main" {}`
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_vpc.main")
	if c == nil {
		t.Fatal("expected change for aws_vpc.main")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
}

// ---- data sources + modules ----

func TestDataSourceRemovedIsBreaking(t *testing.T) {
	oldSrc := `data "aws_ami" "ubuntu" { most_recent = true }`
	newSrc := ``
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "data.aws_ami.ubuntu")
	if c == nil {
		t.Fatal("expected change for data.aws_ami.ubuntu")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
}

func TestModuleBlockRenameDetected(t *testing.T) {
	oldSrc := `module "net" { source = "./v1" }`
	newSrc := `module "network" { source = "./v1" }`
	changes := diffFixture(t, oldSrc, newSrc)
	subject := "module.net → module.network"
	c := findChange(changes, subject)
	if c == nil {
		t.Fatalf("expected module rename, got: %v", changes)
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
}

// ---- terraform block ----

func TestRequiredVersionTightenedIsBreaking(t *testing.T) {
	// ">= 1.5" accepts [1.5, ∞); ">= 1.6" is a strict subset — narrowed.
	oldSrc := "terraform {\n  required_version = \">= 1.5\"\n}\n"
	newSrc := "terraform {\n  required_version = \">= 1.6\"\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "terraform.required_version")
	if c == nil {
		t.Fatalf("expected change, got: %v", changes)
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "tightened") {
		t.Errorf("detail should say tightened: %q", c.Detail)
	}
}

func TestRequiredProviderAddedIsBreaking(t *testing.T) {
	oldSrc := "terraform {\n  required_providers {\n  }\n}\n"
	newSrc := "terraform {\n  required_providers {\n    aws = {\n      source  = \"hashicorp/aws\"\n      version = \">= 4.0\"\n    }\n  }\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "provider.aws")
	if c == nil {
		t.Fatalf("expected change for provider.aws, got: %v", changes)
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "added") {
		t.Errorf("detail should mention added: %q", c.Detail)
	}
}

func TestRequiredProviderRemovedIsNonBreaking(t *testing.T) {
	oldSrc := "terraform {\n  required_providers {\n    aws = {\n      source  = \"hashicorp/aws\"\n      version = \">= 4.0\"\n    }\n  }\n}\n"
	newSrc := "terraform {\n  required_providers {\n  }\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "provider.aws")
	if c == nil {
		t.Fatal("expected change")
	}
	if c.Kind != diff.NonBreaking {
		t.Errorf("kind = %v, want NonBreaking", c.Kind)
	}
}

func TestProviderSourceChangedIsBreaking(t *testing.T) {
	oldSrc := "terraform {\n  required_providers {\n    aws = {\n      source  = \"hashicorp/aws\"\n      version = \">= 4.0\"\n    }\n  }\n}\n"
	newSrc := "terraform {\n  required_providers {\n    aws = {\n      source  = \"myorg/aws-fork\"\n      version = \">= 4.0\"\n    }\n  }\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "provider.aws")
	if c == nil {
		t.Fatal("expected change")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "hashicorp/aws") || !strings.Contains(c.Detail, "myorg/aws-fork") {
		t.Errorf("detail should show both sources: %q", c.Detail)
	}
}

func TestProviderVersionTightenedIsBreaking(t *testing.T) {
	// ">= 4.0" → ">= 5.0" is a strict subset (4.x callers break).
	oldSrc := "terraform {\n  required_providers {\n    aws = {\n      source  = \"hashicorp/aws\"\n      version = \">= 4.0\"\n    }\n  }\n}\n"
	newSrc := "terraform {\n  required_providers {\n    aws = {\n      source  = \"hashicorp/aws\"\n      version = \">= 5.0\"\n    }\n  }\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "provider.aws")
	if c == nil {
		t.Fatal("expected change")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "4.0") || !strings.Contains(c.Detail, "5.0") {
		t.Errorf("detail should show both constraints: %q", c.Detail)
	}
}

// ---- output value expression ----

func TestOutputExpressionChangedIsInformational(t *testing.T) {
	oldSrc := `output "id" { value = aws_vpc.main.id }`
	newSrc := `output "id" { value = aws_vpc.main.arn }`
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "output.id")
	if c == nil {
		t.Fatalf("expected change for output.id, got: %v", changes)
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "value expression changed") {
		t.Errorf("detail should mention value expression: %q", c.Detail)
	}
	if !strings.Contains(c.Detail, "id") || !strings.Contains(c.Detail, "arn") {
		t.Errorf("detail should show both sides: %q", c.Detail)
	}
}

func TestOutputTypeNarrowedIsBreaking(t *testing.T) {
	// Old output's value is a string (templated); new output's value is a
	// list (a for-expression). Downstream consumers expecting a string
	// will fail.
	oldSrc := "variable \"env\" {}\n" +
		"output \"name\" { value = \"${var.env}-app\" }\n"
	newSrc := "variable \"env\" {}\n" +
		"output \"name\" { value = [for s in [\"a\",\"b\"] : s] }\n"
	changes := diff.Diff(
		mustAnalyseIgnoreVal(t, "old.tf", oldSrc),
		mustAnalyseIgnoreVal(t, "new.tf", newSrc),
	)
	c := findChange(changes, "output.name")
	if c == nil {
		t.Fatalf("expected change, got: %v", changes)
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking; detail=%q", c.Kind, c.Detail)
	}
	if !strings.Contains(c.Detail, "output type changed") {
		t.Errorf("detail should mention output type: %q", c.Detail)
	}
	if !strings.Contains(c.Detail, "string") {
		t.Errorf("detail should mention old type 'string': %q", c.Detail)
	}
}

func TestOutputTypeUnknownStaysInformational(t *testing.T) {
	// Both expressions reference resource attributes whose types we don't
	// track — no type analysis possible — so the Informational text-change
	// message should still be emitted (no false-positive Breaking).
	oldSrc := `output "id" { value = aws_vpc.main.id }`
	newSrc := `output "id" { value = aws_vpc.main.arn }`
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "output.id")
	if c == nil {
		t.Fatalf("expected change, got: %v", changes)
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational (types are unknown); detail=%q", c.Kind, c.Detail)
	}
}

func TestOutputExpressionIdenticalProducesNoChange(t *testing.T) {
	// Semantically identical expressions, differently spaced, should not diff.
	oldSrc := `output "id" { value = aws_vpc.main.id }`
	newSrc := "output \"id\" { value   =   aws_vpc.main.id }"
	if cs := diffFixture(t, oldSrc, newSrc); len(cs) != 0 {
		t.Errorf("semantically identical expressions should not produce changes, got: %v", cs)
	}
}

func TestOutputExpressionTemplateChange(t *testing.T) {
	oldSrc := `output "name" { value = "${var.env}-app" }`
	newSrc := `output "name" { value = "${var.env}-service" }`
	// var.env referenced but not declared — ignore validation errors for this test.
	changes := diff.Diff(
		mustAnalyseIgnoreVal(t, "old.tf", oldSrc),
		mustAnalyseIgnoreVal(t, "new.tf", newSrc),
	)
	c := findChange(changes, "output.name")
	if c == nil {
		t.Fatalf("expected change, got: %v", changes)
	}
	if !strings.Contains(c.Detail, "value expression changed") {
		t.Errorf("detail should mention value change: %q", c.Detail)
	}
}

// mustAnalyseIgnoreVal parses + analyses a source and ignores any parse or
// validation errors — useful when we only care about shape changes.
func mustAnalyseIgnoreVal(t *testing.T, filename, src string) *analysis.Module {
	t.Helper()
	p := hclparse.NewParser()
	hclFile, diags := p.ParseHCL([]byte(src), filename)
	for _, d := range diags {
		t.Logf("parse warning: %s", d.Error())
	}
	if hclFile == nil {
		return analysis.AnalyseFiles(nil)
	}
	body, ok := hclFile.Body.(*hclsyntax.Body)
	if !ok {
		return analysis.AnalyseFiles(nil)
	}
	return analysis.Analyse(&analysis.File{Filename: filename, Source: []byte(src), Body: body})
}

// ---- resource provider alias ----

func TestResourceProviderAliasChangeIsBreaking(t *testing.T) {
	oldSrc := "resource \"aws_vpc\" \"main\" {\n  provider = aws.east\n}\n"
	newSrc := "resource \"aws_vpc\" \"main\" {\n  provider = aws.west\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_vpc.main")
	if c == nil {
		t.Fatalf("expected change, got: %v", changes)
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "aws.east") || !strings.Contains(c.Detail, "aws.west") {
		t.Errorf("detail should show both aliases: %q", c.Detail)
	}
}

func TestResourceProviderAddedIsBreaking(t *testing.T) {
	oldSrc := `resource "aws_vpc" "main" {}`
	newSrc := "resource \"aws_vpc\" \"main\" {\n  provider = aws.east\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_vpc.main")
	if c == nil {
		t.Fatal("expected change")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "<default>") || !strings.Contains(c.Detail, "aws.east") {
		t.Errorf("detail should show default→alias: %q", c.Detail)
	}
}

func TestResourceProviderUnchanged(t *testing.T) {
	src := "resource \"aws_vpc\" \"main\" {\n  provider = aws.east\n}\n"
	if cs := diffFixture(t, src, src); len(cs) != 0 {
		t.Errorf("identical provider should produce no changes, got: %v", cs)
	}
}

// ---- indirect locals referenced by outputs ----

func TestOutputReferencingLocalWhoseExprChangedIsInformational(t *testing.T) {
	oldSrc := "locals {\n  prefix = \"old-\"\n}\noutput \"name\" { value = local.prefix }\n"
	newSrc := "locals {\n  prefix = \"new-\"\n}\noutput \"name\" { value = local.prefix }\n"
	changes := diffFixture(t, oldSrc, newSrc)
	// The output expression "local.prefix" is textually unchanged, but the
	// underlying local changed.
	c := findChange(changes, "output.name")
	if c == nil {
		t.Fatalf("expected change for output.name, got: %v", changes)
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "local.prefix") {
		t.Errorf("detail should mention local.prefix: %q", c.Detail)
	}
	if !strings.Contains(c.Detail, "old") || !strings.Contains(c.Detail, "new") {
		t.Errorf("detail should show both values: %q", c.Detail)
	}
}

func TestOutputReferencingUnchangedLocalProducesNoChange(t *testing.T) {
	src := "locals {\n  prefix = \"unchanged\"\n}\noutput \"name\" { value = local.prefix }\n"
	if cs := diffFixture(t, src, src); len(cs) != 0 {
		t.Errorf("identical local + output should produce no changes, got: %v", cs)
	}
}

func TestOutputValueExprChangeShadowsIndirectCheck(t *testing.T) {
	// If both the output expression AND the referenced local changed,
	// we only need to report the output expression change (not duplicate).
	oldSrc := "locals {\n  a = \"old\"\n}\noutput \"x\" { value = local.a }\n"
	newSrc := "locals {\n  a = \"new\"\n}\noutput \"x\" { value = local.a.id }\n"
	changes := diffFixture(t, oldSrc, newSrc)
	found := 0
	for _, c := range changes {
		if c.Subject == "output.x" {
			found++
		}
	}
	if found != 1 {
		t.Errorf("expected exactly 1 change for output.x, got %d: %v", found, changes)
	}
}

// ---- module call arguments ----

func TestModuleArgumentAddedIsInformational(t *testing.T) {
	oldSrc := "module \"net\" {\n  source = \"./net\"\n  cidr   = \"10.0.0.0/16\"\n}\n"
	newSrc := "module \"net\" {\n  source = \"./net\"\n  cidr   = \"10.0.0.0/16\"\n  region = \"us-east-1\"\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "module.net")
	if c == nil {
		t.Fatalf("expected change, got: %v", changes)
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "region") || !strings.Contains(c.Detail, "added") {
		t.Errorf("detail should mention the added argument: %q", c.Detail)
	}
}

func TestModuleArgumentRemovedIsInformational(t *testing.T) {
	oldSrc := "module \"net\" {\n  source = \"./net\"\n  cidr   = \"10.0.0.0/16\"\n  region = \"us-east-1\"\n}\n"
	newSrc := "module \"net\" {\n  source = \"./net\"\n  cidr   = \"10.0.0.0/16\"\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "module.net")
	if c == nil {
		t.Fatal("expected change")
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "region") || !strings.Contains(c.Detail, "removed") {
		t.Errorf("detail should mention removed region: %q", c.Detail)
	}
}

func TestModuleArgumentValueChangedIsInformational(t *testing.T) {
	oldSrc := "module \"net\" {\n  source = \"./net\"\n  cidr   = \"10.0.0.0/16\"\n}\n"
	newSrc := "module \"net\" {\n  source = \"./net\"\n  cidr   = \"10.1.0.0/16\"\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "module.net")
	if c == nil {
		t.Fatal("expected change")
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "cidr") || !strings.Contains(c.Detail, "value changed") {
		t.Errorf("detail should indicate value change: %q", c.Detail)
	}
}

func TestModuleMetaArgsNotTreatedAsArguments(t *testing.T) {
	// count/for_each/depends_on/source/version transitions have their own
	// classifiers and shouldn't be reported as "argument X added/removed".
	oldSrc := "module \"net\" {\n  source = \"./net\"\n}\n"
	newSrc := "module \"net\" {\n  source     = \"./net\"\n  count      = 3\n  depends_on = [aws_vpc.main]\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	for _, c := range changes {
		if strings.Contains(c.Detail, "argument \"count\"") || strings.Contains(c.Detail, "argument \"depends_on\"") {
			t.Errorf("meta-arg %s should not be treated as a user argument: %v", c.Detail, c)
		}
	}
}

// ---- module source / version ----

func TestModuleSourceChangeIsInformational(t *testing.T) {
	oldSrc := `module "net" { source = "./v1" }`
	newSrc := `module "net" { source = "./v2" }`
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "module.net")
	if c == nil {
		t.Fatalf("expected change for module.net, got: %v", changes)
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "source") {
		t.Errorf("detail should mention source: %q", c.Detail)
	}
	if !strings.Contains(c.Detail, "v1") || !strings.Contains(c.Detail, "v2") {
		t.Errorf("detail should include both paths: %q", c.Detail)
	}
}

func TestModuleExactVersionChangeIsDisjoint(t *testing.T) {
	// "1.0.0" is an exact pin; "2.0.0" is a different exact pin — no overlap.
	oldSrc := "module \"net\" {\n  source  = \"hashicorp/network/aws\"\n  version = \"1.0.0\"\n}\n"
	newSrc := "module \"net\" {\n  source  = \"hashicorp/network/aws\"\n  version = \"2.0.0\"\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "module.net")
	if c == nil {
		t.Fatal("expected change")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "incompatible") && !strings.Contains(c.Detail, "no overlap") {
		t.Errorf("detail should mention incompatibility: %q", c.Detail)
	}
}

func TestModuleVersionAddedIsBreakingNarrowing(t *testing.T) {
	// Moving from unpinned (accepts all) to a specific version is a narrow.
	oldSrc := `module "net" { source = "hashicorp/network/aws" }`
	newSrc := "module \"net\" {\n  source  = \"hashicorp/network/aws\"\n  version = \"1.0.0\"\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "module.net")
	if c == nil {
		t.Fatal("expected change")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "tightened") {
		t.Errorf("detail should mention tightening: %q", c.Detail)
	}
}

func TestModuleSourceUnchangedProducesNoChange(t *testing.T) {
	src := `module "net" { source = "./v1" }`
	if cs := diffFixture(t, src, src); len(cs) != 0 {
		t.Errorf("identical module blocks should produce no changes, got: %v", cs)
	}
}

// ---- nullable / sensitive / validation ----

func TestNullableFalseAddedIsBreaking(t *testing.T) {
	oldSrc := "variable \"x\" {\n  type = string\n}\n"
	newSrc := "variable \"x\" {\n  type     = string\n  nullable = false\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.x")
	if c == nil {
		t.Fatalf("expected change for variable.x, got: %v", changes)
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "nullable") {
		t.Errorf("detail should mention nullable: %q", c.Detail)
	}
}

func TestNullableFalseRemovedIsNonBreaking(t *testing.T) {
	oldSrc := "variable \"x\" {\n  type     = string\n  nullable = false\n}\n"
	newSrc := "variable \"x\" {\n  type = string\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.x")
	if c == nil {
		t.Fatal("expected change")
	}
	if c.Kind != diff.NonBreaking {
		t.Errorf("kind = %v, want NonBreaking", c.Kind)
	}
}

func TestSensitiveAddedOnVariableIsBreaking(t *testing.T) {
	oldSrc := "variable \"x\" {\n  type = string\n}\n"
	newSrc := "variable \"x\" {\n  type      = string\n  sensitive = true\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.x")
	if c == nil {
		t.Fatal("expected change")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "sensitive") {
		t.Errorf("detail should mention sensitive: %q", c.Detail)
	}
}

func TestSensitiveAddedOnOutputIsInformational(t *testing.T) {
	oldSrc := `output "x" { value = "v" }`
	newSrc := "output \"x\" {\n  value     = \"v\"\n  sensitive = true\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "output.x")
	if c == nil {
		t.Fatal("expected change")
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
}

func TestSensitiveRemovedOnOutputIsInformational(t *testing.T) {
	oldSrc := "output \"x\" {\n  value     = \"v\"\n  sensitive = true\n}\n"
	newSrc := `output "x" { value = "v" }`
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "output.x")
	if c == nil {
		t.Fatal("expected change")
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "visible") && !strings.Contains(c.Detail, "no longer sensitive") {
		t.Errorf("detail should note the exposure: %q", c.Detail)
	}
}

func TestValidationBlockAddedIsInformational(t *testing.T) {
	oldSrc := "variable \"x\" {\n  type = string\n}\n"
	newSrc := "variable \"x\" {\n  type = string\n  validation {\n    condition     = length(var.x) > 0\n    error_message = \"must not be empty\"\n  }\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.x")
	if c == nil {
		t.Fatalf("expected change, got: %v", changes)
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "validation") {
		t.Errorf("detail should mention validation: %q", c.Detail)
	}
}

// ---- precondition / postcondition ----

func TestVariablePreconditionAddedIsInformational(t *testing.T) {
	oldSrc := "variable \"x\" { type = string }\n"
	newSrc := "variable \"x\" {\n  type = string\n  precondition {\n    condition     = length(var.x) > 0\n    error_message = \"must be nonempty\"\n  }\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.x")
	if c == nil {
		t.Fatalf("expected change, got: %v", changes)
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "precondition") {
		t.Errorf("detail should mention precondition: %q", c.Detail)
	}
}

func TestOutputPostconditionAddedIsInformational(t *testing.T) {
	oldSrc := `output "x" { value = "v" }`
	newSrc := "output \"x\" {\n  value = \"v\"\n  postcondition {\n    condition     = self != \"\"\n    error_message = \"must be nonempty\"\n  }\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "output.x")
	if c == nil {
		t.Fatalf("expected change, got: %v", changes)
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "postcondition") {
		t.Errorf("detail should mention postcondition: %q", c.Detail)
	}
}

func TestLifecyclePreconditionAddedIsInformational(t *testing.T) {
	oldSrc := `resource "aws_vpc" "main" {}`
	newSrc := "resource \"aws_vpc\" \"main\" {\n  lifecycle {\n    precondition {\n      condition     = var.env != \"\"\n      error_message = \"env required\"\n    }\n  }\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_vpc.main")
	if c == nil {
		t.Fatalf("expected change, got: %v", changes)
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
	if !strings.Contains(c.Detail, "precondition") {
		t.Errorf("detail should mention precondition: %q", c.Detail)
	}
}

// ---- object field-level diff ----

func TestObjectFieldAddedRequiredIsBreaking(t *testing.T) {
	oldSrc := "variable \"cfg\" {\n  type = object({ a = string })\n}\n"
	newSrc := "variable \"cfg\" {\n  type = object({ a = string, b = string })\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.cfg")
	if c == nil {
		t.Fatalf("expected change for variable.cfg, got: %v", changes)
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, `"b"`) || !strings.Contains(c.Detail, "required") {
		t.Errorf("detail should mention field b and required: %q", c.Detail)
	}
}

func TestObjectFieldAddedOptionalIsNonBreaking(t *testing.T) {
	oldSrc := "variable \"cfg\" {\n  type = object({ a = string })\n}\n"
	newSrc := "variable \"cfg\" {\n  type = object({ a = string, b = optional(string) })\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.cfg")
	if c == nil {
		t.Fatalf("expected change for variable.cfg, got: %v", changes)
	}
	if c.Kind != diff.NonBreaking {
		t.Errorf("kind = %v, want NonBreaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "optional") {
		t.Errorf("detail should mention optional: %q", c.Detail)
	}
}

func TestObjectFieldRemovedIsBreaking(t *testing.T) {
	oldSrc := "variable \"cfg\" {\n  type = object({ a = string, b = number })\n}\n"
	newSrc := "variable \"cfg\" {\n  type = object({ a = string })\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.cfg")
	if c == nil {
		t.Fatal("expected change for variable.cfg")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "removed") || !strings.Contains(c.Detail, `"b"`) {
		t.Errorf("detail should say field b removed: %q", c.Detail)
	}
}

func TestObjectFieldOptionalToRequiredIsBreaking(t *testing.T) {
	oldSrc := "variable \"cfg\" {\n  type = object({ a = optional(string) })\n}\n"
	newSrc := "variable \"cfg\" {\n  type = object({ a = string })\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.cfg")
	if c == nil {
		t.Fatal("expected change for variable.cfg")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "required") {
		t.Errorf("detail should mention required: %q", c.Detail)
	}
}

func TestObjectFieldRequiredToOptionalIsNonBreaking(t *testing.T) {
	oldSrc := "variable \"cfg\" {\n  type = object({ a = string })\n}\n"
	newSrc := "variable \"cfg\" {\n  type = object({ a = optional(string) })\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.cfg")
	if c == nil {
		t.Fatal("expected change for variable.cfg")
	}
	if c.Kind != diff.NonBreaking {
		t.Errorf("kind = %v, want NonBreaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "optional") {
		t.Errorf("detail should mention optional: %q", c.Detail)
	}
}

func TestObjectFieldInnerTypeChangeIsBreaking(t *testing.T) {
	oldSrc := "variable \"cfg\" {\n  type = object({ a = string })\n}\n"
	newSrc := "variable \"cfg\" {\n  type = object({ a = number })\n}\n"
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "variable.cfg")
	if c == nil {
		t.Fatal("expected change for variable.cfg")
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
	if !strings.Contains(c.Detail, "string") || !strings.Contains(c.Detail, "number") {
		t.Errorf("detail should show type change: %q", c.Detail)
	}
}

func TestIdenticalOptionalFieldsProduceNoChange(t *testing.T) {
	src := "variable \"cfg\" {\n  type = object({ a = optional(string), b = number })\n}\n"
	if changes := diffFixture(t, src, src); len(changes) != 0 {
		t.Errorf("identical object types should produce no changes, got: %v", changes)
	}
}

// ---- moved / removed blocks ----

func TestMovedBlockSuppressesRenameBreaking(t *testing.T) {
	oldSrc := `resource "aws_vpc" "old_name" {}`
	newSrc := `
resource "aws_vpc" "new_name" {}
moved {
  from = aws_vpc.old_name
  to   = aws_vpc.new_name
}
`
	changes := diffFixture(t, oldSrc, newSrc)
	subject := "resource.aws_vpc.old_name → resource.aws_vpc.new_name"
	c := findChange(changes, subject)
	if c == nil {
		t.Fatalf("expected a change for the rename, got: %v", changes)
	}
	if c.Kind != diff.Informational {
		t.Errorf("moved-handled rename should be Informational, got: %v (detail=%q)", c.Kind, c.Detail)
	}
	if !strings.Contains(c.Detail, "moved") {
		t.Errorf("detail should reference moved: %q", c.Detail)
	}
	// No "possible rename" breaking should appear for this pair.
	for _, ch := range changes {
		if ch.Kind == diff.Breaking && strings.Contains(ch.Subject, subject) {
			t.Errorf("should not be breaking when moved handles it: %v", ch)
		}
	}
}

func TestMovedBlockForModule(t *testing.T) {
	oldSrc := `module "net" { source = "./v1" }`
	newSrc := `
module "network" { source = "./v1" }
moved {
  from = module.net
  to   = module.network
}
`
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "module.net → module.network")
	if c == nil {
		t.Fatalf("expected rename change, got: %v", changes)
	}
	if c.Kind != diff.Informational {
		t.Errorf("kind = %v, want Informational", c.Kind)
	}
}

func TestRemovedBlockDowngradesRemoval(t *testing.T) {
	oldSrc := `resource "aws_vpc" "legacy" {}`
	newSrc := `
removed {
  from = aws_vpc.legacy
  lifecycle {
    destroy = false
  }
}
`
	changes := diffFixture(t, oldSrc, newSrc)
	c := findChange(changes, "resource.aws_vpc.legacy")
	if c == nil {
		t.Fatalf("expected change for aws_vpc.legacy, got: %v", changes)
	}
	if c.Kind != diff.Informational {
		t.Errorf("removed-block removal should be Informational, got: %v", c.Kind)
	}
	if !strings.Contains(c.Detail, "removed") {
		t.Errorf("detail should mention removed block: %q", c.Detail)
	}
}

func TestMovedBlockMismatchStillBreaking(t *testing.T) {
	// moved block refers to entities that don't both exist — should not take effect.
	oldSrc := `resource "aws_vpc" "old_name" {}`
	newSrc := `
resource "aws_vpc" "new_name" {}
moved {
  from = aws_vpc.different_name
  to   = aws_vpc.new_name
}
`
	changes := diffFixture(t, oldSrc, newSrc)
	// The actual rename (old_name → new_name) was NOT declared, so it should still be Breaking.
	c := findChange(changes, "resource.aws_vpc.old_name → resource.aws_vpc.new_name")
	if c == nil {
		t.Fatalf("expected breaking rename, got: %v", changes)
	}
	if c.Kind != diff.Breaking {
		t.Errorf("kind = %v, want Breaking", c.Kind)
	}
}

// ---- sorting ----

func TestChangesSortedBreakingFirst(t *testing.T) {
	oldSrc := `
variable "old_var" { default = "x" }
output "old_out" { value = "x" }
`
	newSrc := `
variable "new_var" { default = "y" }
output "new_out" { value = "y" }
`
	changes := diffFixture(t, oldSrc, newSrc)
	if len(changes) == 0 {
		t.Fatal("expected some changes")
	}
	// All Breaking changes must come before any NonBreaking, which must come
	// before any Informational.
	for i := 1; i < len(changes); i++ {
		if changes[i-1].Kind > changes[i].Kind {
			t.Errorf("changes out of order: %v before %v", changes[i-1], changes[i])
		}
	}
}
