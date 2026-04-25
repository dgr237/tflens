package render_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/render"
)

// TestBuildExportShape exercises the prototype export shape end-to-end:
// loads a project from testdata, runs BuildExport, and asserts the
// envelope + per-module fields have the expected values. Brittle on
// purpose — the schema is experimental and consumers depend on the
// shape, so any change should be deliberate.
func TestBuildExportShape(t *testing.T) {
	dir := writeExportFixture(t)
	p, _, err := loader.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	exp := render.BuildExport(p, "test-version")

	if !exp.Experimental {
		t.Error("expected _experimental: true")
	}
	if exp.SchemaVersion != render.ExportSchemaVersion {
		t.Errorf("schema_version = %q, want %q", exp.SchemaVersion, render.ExportSchemaVersion)
	}
	if exp.TflensVersion != "test-version" {
		t.Errorf("tflens_version = %q, want %q", exp.TflensVersion, "test-version")
	}

	// Variables should include `region` with type=string and default
	// value evaluable to "us-east-1".
	rm := exp.Root.Module
	var region *render.ExportVariable
	for i := range rm.Variables {
		if rm.Variables[i].Name == "region" {
			region = &rm.Variables[i]
		}
	}
	if region == nil {
		t.Fatalf("expected variable 'region' in export")
	}
	if region.Type != "string" {
		t.Errorf("region.type = %q, want %q", region.Type, "string")
	}
	if region.DefaultValue == nil {
		t.Fatal("region.default_value should be populated for a literal default")
	}

	// Local 'image' uses format() — should both have value_text AND
	// an evaluated_value (proves stdlib evaluation is wired through
	// the export).
	var image *render.ExportLocal
	for i := range rm.Locals {
		if rm.Locals[i].Name == "image" {
			image = &rm.Locals[i]
		}
	}
	if image == nil {
		t.Fatalf("expected local 'image'")
	}
	if image.ValueText == "" {
		t.Error("image.value_text should be populated")
	}
	if image.EvaluatedValue == nil {
		t.Error("image.evaluated_value should be populated (format() is in the curated stdlib)")
	}

	// Tracked attribute should surface with subject = local.image.value
	// and the canonical expression text.
	if len(rm.Tracked) == 0 {
		t.Fatal("expected at least one tracked_attribute")
	}
	if rm.Tracked[0].Subject != "local.image.value" {
		t.Errorf("tracked[0].subject = %q, want %q", rm.Tracked[0].Subject, "local.image.value")
	}

	// Child module nesting: 'child' should appear under root.children
	// with the source string preserved.
	if _, ok := exp.Root.Children["child"]; !ok {
		t.Fatalf("expected 'child' in root.children; got %v", exp.Root.Children)
	}
	if exp.Root.Children["child"].Source != "./child" {
		t.Errorf("child.source = %q, want %q", exp.Root.Children["child"].Source, "./child")
	}

	// terraform block: required_version + provider source + backend
	// type all surface in the same nested object.
	if rm.Terraform.RequiredVersion != ">= 1.5.0" {
		t.Errorf("required_version = %q", rm.Terraform.RequiredVersion)
	}
	if got := rm.Terraform.RequiredProviders["aws"].Source; got != "hashicorp/aws" {
		t.Errorf("required_providers.aws.source = %q", got)
	}
	if rm.Terraform.Backend == nil || rm.Terraform.Backend.Type != "s3" {
		t.Errorf("backend = %+v, want type=s3", rm.Terraform.Backend)
	}

	// Resources: one aws_instance with for_each + lifecycle block
	// (prevent_destroy + ignore_changes). Pins the meta-arg surface.
	if len(rm.Resources) != 1 {
		t.Fatalf("resources len = %d, want 1", len(rm.Resources))
	}
	r := rm.Resources[0]
	if r.Type != "aws_instance" || r.Name != "web" {
		t.Errorf("resource[0] = %s.%s, want aws_instance.web", r.Type, r.Name)
	}
	if r.ForEachText == "" {
		t.Error("resource.for_each_text should be populated")
	}
	if !r.PreventDestroy {
		t.Error("resource.prevent_destroy should be true")
	}
	if r.IgnoreChangesText == "" {
		t.Error("resource.ignore_changes_text should be populated")
	}

	// Data sources go in their own array.
	if len(rm.DataSources) != 1 || rm.DataSources[0].Type != "aws_ami" {
		t.Errorf("data_sources = %+v", rm.DataSources)
	}

	// Output value evaluation will fall through (length() applied to a
	// resource address can't be evaluated statically — no provider
	// schema), so EvaluatedValue is nil but ValueText is captured.
	if len(rm.Outputs) != 1 {
		t.Fatalf("outputs len = %d, want 1", len(rm.Outputs))
	}
	if rm.Outputs[0].ValueText == "" {
		t.Error("output.value_text should be populated even when evaluation fails")
	}

	// Module-call arguments map captured as text.
	if len(rm.ModuleCalls) != 1 {
		t.Fatalf("module_calls len = %d, want 1", len(rm.ModuleCalls))
	}
	if rm.ModuleCalls[0].Arguments["region"] != "var.region" {
		t.Errorf("module.region argument = %q", rm.ModuleCalls[0].Arguments["region"])
	}

	// Dependency graph: aws_instance.web references var.region via
	// for_each, and module.child also references it via the region
	// argument. Both edges should appear.
	if deps := rm.Dependencies["resource.aws_instance.web"]; len(deps) == 0 {
		t.Error("expected dependencies for resource.aws_instance.web")
	}
}

// TestWriteExportRoundtripsViaJSON confirms WriteExport emits valid
// JSON that re-parses into the Export struct without loss for the
// fields we care about. Catches regressions where struct tags
// drift away from the field names.
func TestWriteExportRoundtripsViaJSON(t *testing.T) {
	dir := writeExportFixture(t)
	p, _, err := loader.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
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
}

// TestBuildExportNilProjectIsEmpty pins the nil-safety contract: a
// nil/empty project produces a valid envelope (with experimental flag
// + schema version) rather than panicking. cmd code can rely on this
// for the "loaded zero modules" edge case.
func TestBuildExportNilProjectIsEmpty(t *testing.T) {
	exp := render.BuildExport(nil, "")
	if !exp.Experimental {
		t.Error("nil project should still emit envelope with _experimental")
	}
	if exp.SchemaVersion == "" {
		t.Error("nil project should still emit schema_version")
	}
}

// writeExportFixture builds a small project tree with exactly the
// shape the export tests need: one variable, one local using format(),
// one tracked attribute, one local-source child module. Inline so the
// test is self-contained and the fixture can evolve alongside the
// assertions.
func writeExportFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	rootTF := `terraform {
  required_version = ">= 1.5.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
  backend "s3" {
    bucket = "my-state"
  }
}

variable "region" {
  type    = string
  default = "us-east-1"
}

locals {
  image = format("ec2-%s-v%d", "small", 3) # tflens:track: AMI image identifier
}

resource "aws_instance" "web" {
  for_each = toset([var.region])
  lifecycle {
    prevent_destroy = true
    ignore_changes  = [tags]
  }
}

data "aws_ami" "latest" {
  most_recent = true
}

output "instance_count" {
  value     = length(aws_instance.web)
  sensitive = false
}

module "child" {
  source = "./child"
  region = var.region
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(rootTF), 0o644); err != nil {
		t.Fatal(err)
	}
	childDir := filepath.Join(dir, "child")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	childTF := `variable "region" {
  type = string
}
`
	if err := os.WriteFile(filepath.Join(childDir, "main.tf"), []byte(childTF), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
