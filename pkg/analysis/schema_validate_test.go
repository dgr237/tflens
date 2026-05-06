package analysis_test

import (
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/providerschema"
)

const testSchema = `{
  "format_version": "0.2",
  "provider_schemas": {
    "registry.terraform.io/hashicorp/aws": {
      "resource_schemas": {
        "aws_subnet": {
          "block": {
            "attributes": {
              "cidr_block": {"type": "string", "optional": true},
              "id":         {"type": "string", "computed": true},
              "tags":       {"type": ["map", "string"], "optional": true}
            }
          }
        }
      },
      "data_source_schemas": {
        "aws_ami": {
          "block": {
            "attributes": {
              "id":          {"type": "string", "computed": true},
              "most_recent": {"type": "bool", "optional": true}
            }
          }
        }
      }
    }
  }
}`

// loadTestSchema parses the in-test schema, failing the test on
// parse error. Returned schema is shared across the table cases —
// pure-read so concurrency-safe.
func loadTestSchema(t *testing.T) *providerschema.Schema {
	t.Helper()
	s, err := providerschema.Parse([]byte(testSchema))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return s
}

func TestCheckResourceAttrRefs(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantMsg string // substring match; empty means no errors expected
	}{
		{
			name: "valid resource attribute reference",
			src: `
resource "aws_subnet" "x" { cidr_block = "10.0.0.0/24" }
output "ok" { value = aws_subnet.x.cidr_block }
`,
		},
		{
			name: "invalid resource attribute reference",
			src: `
resource "aws_subnet" "x" { cidr_block = "10.0.0.0/24" }
output "bad" { value = aws_subnet.x.cider_block }
`,
			wantMsg: `attribute "cider_block" does not exist on resource type aws_subnet`,
		},
		{
			name: "valid map attribute with index",
			src: `
resource "aws_subnet" "x" { cidr_block = "10.0.0.0/24" }
output "ok" { value = aws_subnet.x.tags["env"] }
`,
		},
		{
			name: "valid data source attribute reference",
			src: `
data "aws_ami" "latest" { most_recent = true }
output "ok" { value = data.aws_ami.latest.id }
`,
		},
		{
			name: "invalid data source attribute reference",
			src: `
data "aws_ami" "latest" { most_recent = true }
output "bad" { value = data.aws_ami.latest.bogus }
`,
			wantMsg: `attribute "bogus" does not exist on data source type aws_ami`,
		},
		{
			name: "unknown resource type silently skipped",
			src: `
resource "google_compute_subnetwork" "x" { name = "demo" }
output "ok" { value = google_compute_subnetwork.x.bogus_attr }
`,
			// google_compute_subnetwork isn't in the schema — must NOT
			// flag (multi-cloud config supplying only AWS schema).
		},
		{
			name: "ref to undeclared resource silently skipped",
			src: `
output "bad" { value = aws_subnet.does_not_exist.cidr_block }
`,
			// The undefined-reference pass already catches this; the
			// schema pass shouldn't double-flag.
		},
	}

	schema := loadTestSchema(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := analyseFixtureNamed(t, "main.tf", tc.src)
			analysis.CheckResourceAttrRefs(m, schema)
			errs := m.Validate()
			if tc.wantMsg == "" {
				// Filter to schema-validation errors only — the
				// "ref to undeclared resource" case has an existing
				// undefined-reference error that we don't want to
				// fail on here.
				for _, e := range errs {
					if strings.Contains(e.Msg, "does not exist on") {
						t.Errorf("unexpected schema error: %v", e)
					}
				}
				return
			}
			for _, e := range errs {
				if strings.Contains(e.Msg, tc.wantMsg) {
					return
				}
			}
			t.Errorf("expected error containing %q, got: %v", tc.wantMsg, errs)
		})
	}
}

func TestCheckResourceAttrRefs_NilSchema(t *testing.T) {
	// Passing nil schema must be a no-op, not a panic — every cmd
	// path passes through nil when the user didn't supply --provider-schema.
	m := analyseFixtureNamed(t, "main.tf", `
resource "aws_subnet" "x" { cidr_block = "10.0.0.0/24" }
output "would_be_bad" { value = aws_subnet.x.cider_block }
`)
	analysis.CheckResourceAttrRefs(m, nil)
	for _, e := range m.Validate() {
		if strings.Contains(e.Msg, "does not exist on") {
			t.Errorf("nil schema produced schema error: %v", e)
		}
	}
}
