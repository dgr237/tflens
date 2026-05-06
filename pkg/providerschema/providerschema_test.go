package providerschema_test

import (
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/providerschema"
)

// miniSchema is a hand-crafted slice of provider-schema JSON covering
// the minimum surface the resolver tests need: one resource with a
// scalar attribute, a map attribute, and a single-nesting block; one
// data source with a scalar attribute. Avoids dependency on a real
// `terraform providers schema -json` output (which would be ~30MB
// and tied to a specific provider version).
const miniSchema = `{
  "format_version": "0.2",
  "provider_schemas": {
    "registry.terraform.io/hashicorp/aws": {
      "resource_schemas": {
        "aws_subnet": {
          "block": {
            "attributes": {
              "cidr_block": {"type": "string", "optional": true},
              "tags":       {"type": ["map", "string"], "optional": true},
              "id":         {"type": "string", "computed": true}
            },
            "block_types": {
              "timeouts": {
                "nesting_mode": "single",
                "block": {
                  "attributes": {
                    "create": {"type": "string", "optional": true},
                    "delete": {"type": "string", "optional": true}
                  }
                }
              }
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

func TestParseAndLookup(t *testing.T) {
	s, err := providerschema.Parse([]byte(miniSchema))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	cases := []struct {
		name          string
		typeName      string
		isData        bool
		path          []string
		wantOK        bool
		wantType      string
		wantSensitive bool
	}{
		{name: "resource scalar attr", typeName: "aws_subnet", path: []string{"cidr_block"}, wantOK: true, wantType: "string"},
		{name: "resource map attr", typeName: "aws_subnet", path: []string{"tags"}, wantOK: true, wantType: "map of string"},
		{name: "resource computed attr", typeName: "aws_subnet", path: []string{"id"}, wantOK: true, wantType: "string"},
		{name: "resource nested block leaf", typeName: "aws_subnet", path: []string{"timeouts", "create"}, wantOK: true, wantType: "string"},
		{name: "resource unknown attr", typeName: "aws_subnet", path: []string{"nonexistent"}, wantOK: false},
		{name: "resource unknown nested attr", typeName: "aws_subnet", path: []string{"timeouts", "bogus"}, wantOK: false},
		{name: "unknown resource type", typeName: "aws_unknown", path: []string{"any"}, wantOK: false},
		{name: "data scalar attr", typeName: "aws_ami", isData: true, path: []string{"most_recent"}, wantOK: true, wantType: "bool"},
		{name: "data unknown attr", typeName: "aws_ami", isData: true, path: []string{"bogus"}, wantOK: false},
		{name: "empty path", typeName: "aws_subnet", path: nil, wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var attr *providerschema.Attribute
			var ok bool
			if tc.isData {
				attr, ok = s.ResolveDataAttr(tc.typeName, tc.path)
			} else {
				attr, ok = s.ResolveAttr(tc.typeName, tc.path)
			}
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if got := attr.Type.FriendlyName(); got != tc.wantType {
				t.Errorf("type = %q, want %q", got, tc.wantType)
			}
		})
	}
}

func TestHasResourceAndDataSource(t *testing.T) {
	s, err := providerschema.Parse([]byte(miniSchema))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !s.HasResource("aws_subnet") {
		t.Error("HasResource(aws_subnet) = false")
	}
	if s.HasResource("aws_unknown") {
		t.Error("HasResource(aws_unknown) = true")
	}
	if !s.HasDataSource("aws_ami") {
		t.Error("HasDataSource(aws_ami) = false")
	}
	if s.HasDataSource("aws_unknown") {
		t.Error("HasDataSource(aws_unknown) = true")
	}
}

func TestNilSchema(t *testing.T) {
	// Nil receiver shouldn't panic — every public method is nil-safe
	// because pkg/loader passes a Project's nil ProviderSchema field
	// through unchanged when no flag was supplied.
	var s *providerschema.Schema
	if s.HasResource("aws_subnet") {
		t.Error("nil HasResource = true")
	}
	if s.HasDataSource("aws_ami") {
		t.Error("nil HasDataSource = true")
	}
	if _, ok := s.ResolveAttr("aws_subnet", []string{"x"}); ok {
		t.Error("nil ResolveAttr = ok")
	}
}

func TestParseMalformedJSON(t *testing.T) {
	_, err := providerschema.Parse([]byte("not json"))
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "parse provider schema") {
		t.Errorf("error message doesn't mention parse failure: %v", err)
	}
}

func TestParseInvalidCtyType(t *testing.T) {
	bad := `{"provider_schemas":{"x":{"resource_schemas":{"r":{"block":{"attributes":{"a":{"type":"not-a-real-type"}}}}}}}}`
	_, err := providerschema.Parse([]byte(bad))
	if err == nil {
		t.Fatal("expected error from invalid cty type")
	}
}
