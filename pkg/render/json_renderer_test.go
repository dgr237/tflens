package render_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/render"
	"github.com/dgr237/tflens/pkg/statediff"
)

// jsonRendererCase pairs an Invoke closure (which drives one
// JSONRenderer method against a buffer) with an Assert closure that
// validates the captured bytes. The single entry point gives every
// JSONRenderer method a coverage-touching home without spawning a
// per-method test function — the missing-coverage methods (Deps,
// Unused, Statediff, CacheCleared, FmtParseErrors) all live here.
//
// Tests that need richer JSON envelope assertions (Diff/Whatif/
// Validate/Inventory) live in their own files alongside the text
// renderer cases.
type jsonRendererCase struct {
	Name   string
	Invoke func(r render.Renderer)
	Assert func(t *testing.T, raw []byte)
}

func TestJSONRendererCases(t *testing.T) {
	for _, tc := range jsonRendererCases {
		t.Run(tc.Name, func(t *testing.T) {
			var b bytes.Buffer
			tc.Invoke(jsonRenderer(&b))
			tc.Assert(t, b.Bytes())
		})
	}
}

var jsonRendererCases = []jsonRendererCase{
	{
		// Deps emits {entity, depends_on, referenced_by}. Confirm
		// the field names and that empty slices marshal as [], not null.
		Name: "deps_populates_envelope",
		Invoke: func(r render.Renderer) {
			r.Deps("resource.aws_vpc.main",
				[]string{"variable.cidr"},
				[]string{"resource.aws_subnet.public"})
		},
		Assert: func(t *testing.T, raw []byte) {
			var got struct {
				Entity       string   `json:"entity"`
				DependsOn    []string `json:"depends_on"`
				ReferencedBy []string `json:"referenced_by"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v\nraw=%s", err, raw)
			}
			if got.Entity != "resource.aws_vpc.main" {
				t.Errorf("Entity = %q", got.Entity)
			}
			if len(got.DependsOn) != 1 || got.DependsOn[0] != "variable.cidr" {
				t.Errorf("DependsOn = %v", got.DependsOn)
			}
			if len(got.ReferencedBy) != 1 || got.ReferencedBy[0] != "resource.aws_subnet.public" {
				t.Errorf("ReferencedBy = %v", got.ReferencedBy)
			}
		},
	},
	{
		// Unused emits {unreferenced: [JSONEntity, ...]}. Each entity
		// goes through the jsonEnt adapter so the canonical ID format
		// is observable here.
		Name: "unused_populates_envelope",
		Invoke: func(r render.Renderer) {
			r.Unused([]analysis.Entity{
				{Kind: analysis.KindVariable, Name: "orphan"},
				{Kind: analysis.KindLocal, Name: "stale"},
			})
		},
		Assert: func(t *testing.T, raw []byte) {
			var got struct {
				Unreferenced []render.JSONEntity `json:"unreferenced"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(got.Unreferenced) != 2 {
				t.Fatalf("got %d entries, want 2", len(got.Unreferenced))
			}
			ids := []string{got.Unreferenced[0].ID, got.Unreferenced[1].ID}
			for _, want := range []string{"variable.orphan", "local.stale"} {
				found := false
				for _, id := range ids {
					if id == want {
						found = true
					}
				}
				if !found {
					t.Errorf("missing %q in %v", want, ids)
				}
			}
		},
	},
	{
		// Statediff emits the statediff.Result struct as JSON. Pin
		// that the BaseRef + flagged-resource counts round-trip.
		Name: "statediff_populates_envelope",
		Invoke: func(r render.Renderer) {
			r.Statediff(&statediff.Result{
				BaseRef: "main",
				AddedResources: []statediff.ResourceRef{
					{Type: "aws_vpc", Name: "main", Mode: "managed"},
				},
				StateOrphans: []string{"aws_eip.unused"},
			})
		},
		Assert: func(t *testing.T, raw []byte) {
			var got statediff.Result
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v\nraw=%s", err, raw)
			}
			if got.BaseRef != "main" {
				t.Errorf("BaseRef = %q", got.BaseRef)
			}
			if len(got.AddedResources) != 1 || got.AddedResources[0].Name != "main" {
				t.Errorf("AddedResources = %+v", got.AddedResources)
			}
			if len(got.StateOrphans) != 1 || got.StateOrphans[0] != "aws_eip.unused" {
				t.Errorf("StateOrphans = %+v", got.StateOrphans)
			}
		},
	},
	{
		// CacheCleared reuses the {path, entries, bytes} shape so
		// JSON consumers can treat info / cleared / already-empty
		// uniformly. Pin the count fields round-trip.
		Name: "cache_cleared_reuses_cache_info_shape",
		Invoke: func(r render.Renderer) {
			r.CacheCleared(7, 1536, "/tmp/cache")
		},
		Assert: func(t *testing.T, raw []byte) {
			var got struct {
				Path    string `json:"path"`
				Entries int    `json:"entries"`
				Bytes   int64  `json:"bytes"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.Path != "/tmp/cache" || got.Entries != 7 || got.Bytes != 1536 {
				t.Errorf("got %+v", got)
			}
		},
	},
	{
		// FmtParseErrors emits {parse_errors: [{message, file, line, column}]}
		// with one entry per diagnostic. file/line/column are populated
		// from d.Subject when present.
		Name: "fmt_parse_errors_populates_positions",
		Invoke: func(r render.Renderer) {
			p := hclparse.NewParser()
			_, diags := p.ParseHCL([]byte(`resource "missing-second-label" {`), "broken.tf")
			r.FmtParseErrors(diags)
		},
		Assert: func(t *testing.T, raw []byte) {
			var got struct {
				ParseErrors []struct {
					Message string `json:"message"`
					File    string `json:"file"`
					Line    int    `json:"line"`
					Column  int    `json:"column"`
				} `json:"parse_errors"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v\nraw=%s", err, raw)
			}
			if len(got.ParseErrors) == 0 {
				t.Fatal("expected at least one parse error")
			}
			pe := got.ParseErrors[0]
			if pe.Message == "" {
				t.Error("Message should be populated")
			}
			if pe.File != "broken.tf" {
				t.Errorf("File = %q, want broken.tf", pe.File)
			}
			if pe.Line < 1 {
				t.Errorf("Line = %d, want >= 1", pe.Line)
			}
		},
	},
	{
		// FmtParseErrors with no diags emits an empty array, not null.
		Name: "fmt_parse_errors_empty_emits_empty_array",
		Invoke: func(r render.Renderer) {
			r.FmtParseErrors(nil)
		},
		Assert: func(t *testing.T, raw []byte) {
			s := string(raw)
			if !strings.Contains(s, `"parse_errors":`) {
				t.Errorf("missing parse_errors key in:\n%s", s)
			}
			if strings.Contains(s, "null") {
				t.Errorf("nil diags should produce [], not null; got:\n%s", s)
			}
		},
	},
	{
		// Validate with all three error kinds populated — exercises
		// every for-loop in JSONRenderer.Validate. Earlier tests
		// drove it with one kind at a time, leaving 90% coverage.
		Name: "validate_all_three_kinds",
		Invoke: func(r render.Renderer) {
			r.Validate(
				[]analysis.ValidationError{{EntityID: "x", Ref: "var.a", Msg: "ref err"}},
				[]analysis.ValidationError{{EntityID: "y", Ref: "var.b", Msg: "cross err"}},
				[]analysis.TypeCheckError{{EntityID: "z", Msg: "type err"}},
			)
		},
		Assert: func(t *testing.T, raw []byte) {
			var got struct {
				UndefinedReferences []render.JSONValidationError `json:"undefined_references"`
				CrossModuleIssues   []render.JSONValidationError `json:"cross_module_issues"`
				TypeErrors          []render.JSONTypeError       `json:"type_errors"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(got.UndefinedReferences) != 1 || len(got.CrossModuleIssues) != 1 || len(got.TypeErrors) != 1 {
				t.Errorf("counts: ref=%d cross=%d type=%d",
					len(got.UndefinedReferences), len(got.CrossModuleIssues), len(got.TypeErrors))
			}
		},
	},
	{
		// Whatif's per-call status with a removed pair: APIChanges
		// is empty, so `api_changes` is omitted via omitempty. Pin
		// that the `direct_impact` field is still present (no omit).
		Name: "whatif_removed_pair_omits_api_changes",
		Invoke: func(r render.Renderer) {
			r.Whatif("main", ".", []diff.WhatifResult{{
				Pair: loader.ModuleCallPair{
					Key: "vpc", Status: loader.StatusRemoved,
					OldSource: "ns/vpc/aws", OldVersion: "1.0.0",
				},
			}})
		},
		Assert: func(t *testing.T, raw []byte) {
			s := string(raw)
			if strings.Contains(s, `"api_changes"`) {
				t.Errorf("expected api_changes omitted; got:\n%s", s)
			}
			if !strings.Contains(s, `"direct_impact"`) {
				t.Errorf("direct_impact should appear; got:\n%s", s)
			}
		},
	},
}
