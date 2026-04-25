package render_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/render"
)

// exportFixtureProject loads pkg/render/testdata/export/<name> as a
// loader.Project using the offline-only loader (no network). Mirrors
// the inventoryFixtureModule pattern but for full project trees so
// child-module nesting fixtures are supported.
func exportFixtureProject(t *testing.T, name string) *loader.Project {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "testdata", "export", name)
	p, _, err := loader.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject(%s): %v", name, err)
	}
	return p
}

// exportCase pairs a fixture name with focused assertions on the
// resulting Export envelope. Per-case fixtures keep failures
// actionable — when one assertion blows up you know exactly which
// concept regressed.
type exportCase struct {
	Name   string
	Custom func(t *testing.T, exp render.Export)
}

func TestRendererExportCases(t *testing.T) {
	for _, tc := range exportCases {
		t.Run(tc.Name, func(t *testing.T) {
			p := exportFixtureProject(t, tc.Name)
			exp := render.BuildExport(p, "test-version")
			tc.Custom(t, exp)
		})
	}
}

var exportCases = []exportCase{
	{
		// Variables, locals, outputs, data — and the cty stdlib
		// evaluation surface in particular: the format()-driven local
		// MUST surface an evaluated_value (proves stdlib is wired
		// through the export); the data-source-driven local MUST NOT
		// (proves the conservative-fallback principle holds — text
		// only when evaluation can't resolve). Tracked-attribute
		// surfacing is also exercised here because the marker lives
		// on the same local.
		Name:   "scalar_entities",
		Custom: assertScalarEntities,
	},
	{
		// Resource + data source meta-args: for_each text capture,
		// lifecycle prevent_destroy / ignore_changes capture, count
		// text capture, separate buckets for resources vs data sources,
		// stable sort by (type, name).
		Name:   "resources_and_data",
		Custom: assertResourcesAndData,
	},
	{
		// Terraform block: required_version + required_providers
		// (with source + version constraint) + backend type. Catches
		// regressions in pkg/analysis getter naming.
		Name:   "terraform_block",
		Custom: assertTerraformBlock,
	},
	{
		// Project tree recursion: child module appears under
		// root.children.<call-name> with the original source string
		// preserved, child's own variables/outputs are exported.
		Name:   "nested_modules",
		Custom: assertNestedModules,
	},
}

func assertScalarEntities(t *testing.T, exp render.Export) {
	if !exp.Experimental {
		t.Error("expected _experimental: true")
	}
	if exp.SchemaVersion != render.ExportSchemaVersion {
		t.Errorf("schema_version = %q, want %q", exp.SchemaVersion, render.ExportSchemaVersion)
	}
	if exp.TflensVersion != "test-version" {
		t.Errorf("tflens_version = %q", exp.TflensVersion)
	}

	rm := exp.Root.Module
	region := findVariable(t, rm.Variables, "region")
	if region.Type != "string" {
		t.Errorf("region.type = %q, want string", region.Type)
	}
	if region.DefaultValue == nil {
		t.Error("region.default_value should be populated for a literal default")
	}

	count := findVariable(t, rm.Variables, "instance_count")
	if !count.Sensitive {
		t.Error("instance_count.sensitive should be true")
	}

	image := findLocal(t, rm.Locals, "image")
	if image.ValueText == "" {
		t.Error("image.value_text should be populated")
	}
	if image.EvaluatedValue == nil {
		t.Error("image.evaluated_value should be populated (format() is in the curated stdlib)")
	}

	unevaled := findLocal(t, rm.Locals, "unevaled")
	if unevaled.ValueText == "" {
		t.Error("unevaled.value_text should be populated")
	}
	if unevaled.EvaluatedValue != nil {
		t.Errorf("unevaled.evaluated_value should be nil (data-source ref is unevaluable); got %s",
			string(unevaled.EvaluatedValue.Value))
	}

	// Tracked attribute on local.image surfaces with subject =
	// local.image.value and the canonical expression text.
	if len(rm.Tracked) == 0 {
		t.Fatal("expected at least one tracked_attribute")
	}
	if rm.Tracked[0].Subject != "local.image.value" {
		t.Errorf("tracked[0].subject = %q, want local.image.value", rm.Tracked[0].Subject)
	}

	// Outputs come through with both text and (when applicable)
	// sensitivity flag.
	if len(rm.Outputs) != 2 {
		t.Fatalf("outputs len = %d, want 2", len(rm.Outputs))
	}
	for _, o := range rm.Outputs {
		if o.ValueText == "" {
			t.Errorf("output %q value_text should be populated", o.Name)
		}
	}
}

func assertResourcesAndData(t *testing.T, exp render.Export) {
	rm := exp.Root.Module
	if len(rm.Resources) != 2 {
		t.Fatalf("resources len = %d, want 2", len(rm.Resources))
	}
	// Stable sort by (type, name): aws_instance.web before aws_security_group.sg.
	if rm.Resources[0].Type != "aws_instance" || rm.Resources[1].Type != "aws_security_group" {
		t.Errorf("resources not sorted by (type,name): %+v",
			[]string{rm.Resources[0].Type, rm.Resources[1].Type})
	}

	web := rm.Resources[0]
	if web.ForEachText == "" {
		t.Error("aws_instance.web.for_each_text should be populated")
	}
	if !web.PreventDestroy {
		t.Error("aws_instance.web.prevent_destroy should be true")
	}
	if web.IgnoreChangesText == "" {
		t.Error("aws_instance.web.ignore_changes_text should be populated")
	}

	sg := rm.Resources[1]
	if sg.CountText == "" {
		t.Error("aws_security_group.sg.count_text should be populated")
	}

	// Data sources go in their own array, sorted by (type, name).
	if len(rm.DataSources) != 2 {
		t.Fatalf("data_sources len = %d, want 2", len(rm.DataSources))
	}
	if rm.DataSources[0].Type != "aws_ami" || rm.DataSources[1].Type != "aws_caller_identity" {
		t.Errorf("data_sources not sorted by type: %+v",
			[]string{rm.DataSources[0].Type, rm.DataSources[1].Type})
	}
}

func assertTerraformBlock(t *testing.T, exp render.Export) {
	tf := exp.Root.Module.Terraform
	if tf.RequiredVersion != ">= 1.5.0" {
		t.Errorf("required_version = %q", tf.RequiredVersion)
	}
	aws, ok := tf.RequiredProviders["aws"]
	if !ok {
		t.Fatalf("required_providers missing 'aws'; got %v", tf.RequiredProviders)
	}
	if aws.Source != "hashicorp/aws" {
		t.Errorf("aws.source = %q", aws.Source)
	}
	if aws.VersionConstraint != "~> 5.0" {
		t.Errorf("aws.version_constraint = %q", aws.VersionConstraint)
	}
	if _, ok := tf.RequiredProviders["random"]; !ok {
		t.Error("required_providers missing 'random'")
	}
	if tf.Backend == nil || tf.Backend.Type != "s3" {
		t.Errorf("backend = %+v, want type=s3", tf.Backend)
	}
}

func assertNestedModules(t *testing.T, exp render.Export) {
	if len(exp.Root.Module.ModuleCalls) != 1 {
		t.Fatalf("module_calls len = %d, want 1", len(exp.Root.Module.ModuleCalls))
	}
	mc := exp.Root.Module.ModuleCalls[0]
	if mc.Name != "child" || mc.Source != "./child" {
		t.Errorf("module_call = %+v, want name=child source=./child", mc)
	}
	if mc.Arguments["region"] != "var.region" {
		t.Errorf("module.region argument = %q", mc.Arguments["region"])
	}

	child, ok := exp.Root.Children["child"]
	if !ok {
		t.Fatalf("expected 'child' in root.children; got %v", exp.Root.Children)
	}
	if child.Source != "./child" {
		t.Errorf("child.source = %q, want ./child", child.Source)
	}
	if findVariable(t, child.Module.Variables, "region").Type != "string" {
		t.Error("child's region variable should have type=string")
	}
	if len(child.Module.Outputs) != 1 || child.Module.Outputs[0].Name != "echo" {
		t.Errorf("child outputs = %+v, want one named 'echo'", child.Module.Outputs)
	}
}

// TestWriteExportRoundtripsViaJSON confirms WriteExport emits valid
// JSON that re-parses into the Export struct without loss for the
// fields the table tests exercise. Catches regressions where struct
// tags drift away from the field names. Reuses scalar_entities so
// the round-trip touches every field group covered by the table.
func TestWriteExportRoundtripsViaJSON(t *testing.T) {
	p := exportFixtureProject(t, "scalar_entities")
	exp := render.BuildExport(p, "v0")
	var buf bytes.Buffer
	if err := render.WriteExport(exp, &buf); err != nil {
		t.Fatalf("WriteExport: %v", err)
	}
	var round render.Export
	if err := json.Unmarshal(buf.Bytes(), &round); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if round.SchemaVersion != render.ExportSchemaVersion {
		t.Errorf("round-trip schema_version = %q", round.SchemaVersion)
	}
	if !round.Experimental {
		t.Error("round-trip _experimental should still be true")
	}
	// Spot-check that the evaluated_value RawMessage round-trips —
	// the json.RawMessage shape is the most likely thing to break.
	if len(round.Root.Module.Locals) == 0 {
		t.Fatal("round-trip lost locals")
	}
	for _, l := range round.Root.Module.Locals {
		if l.Name == "image" && l.EvaluatedValue == nil {
			t.Error("round-trip lost image.evaluated_value")
		}
	}
}

// TestBuildExportNilProjectIsEmpty pins the nil-safety contract: a
// nil/empty project produces a valid envelope (with experimental flag
// + schema version) rather than panicking. Inline because no fixture
// is needed — the input is literally nil.
func TestBuildExportNilProjectIsEmpty(t *testing.T) {
	exp := render.BuildExport(nil, "")
	if !exp.Experimental {
		t.Error("nil project should still emit envelope with _experimental")
	}
	if exp.SchemaVersion == "" {
		t.Error("nil project should still emit schema_version")
	}
}

// ---- helpers ----

func findVariable(t *testing.T, vars []render.ExportVariable, name string) render.ExportVariable {
	t.Helper()
	for _, v := range vars {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("variable %q not found in %d variables", name, len(vars))
	return render.ExportVariable{}
}

func findLocal(t *testing.T, locals []render.ExportLocal, name string) render.ExportLocal {
	t.Helper()
	for _, l := range locals {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("local %q not found in %d locals", name, len(locals))
	return render.ExportLocal{}
}
