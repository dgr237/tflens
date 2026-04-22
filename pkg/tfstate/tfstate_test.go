package tfstate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dgr237/tflens/pkg/tfstate"
)

const sampleState = `{
  "version": 4,
  "terraform_version": "1.5.0",
  "resources": [
    {
      "module": "",
      "mode": "managed",
      "type": "aws_vpc",
      "name": "main",
      "instances": [{"schema_version": 0, "attributes": {}}]
    },
    {
      "module": "module.compute",
      "mode": "managed",
      "type": "aws_instance",
      "name": "web",
      "instances": [
        {"index_key": "us-east-1", "attributes": {}},
        {"index_key": "us-west-2", "attributes": {}}
      ]
    },
    {
      "module": "",
      "mode": "managed",
      "type": "aws_subnet",
      "name": "public",
      "instances": [
        {"index_key": 0, "attributes": {}},
        {"index_key": 1, "attributes": {}}
      ]
    }
  ]
}`

func TestParseBytes(t *testing.T) {
	s, err := tfstate.ParseBytes([]byte(sampleState))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if s.Version != 4 {
		t.Errorf("version = %d, want 4", s.Version)
	}
	if len(s.Resources) != 3 {
		t.Fatalf("resource count = %d, want 3", len(s.Resources))
	}
}

func TestSingletonAddress(t *testing.T) {
	s, _ := tfstate.ParseBytes([]byte(sampleState))
	vpc := s.Resources[0]
	if got := vpc.Address(vpc.Instances[0]); got != "aws_vpc.main" {
		t.Errorf("singleton address = %q", got)
	}
	if got := vpc.FullAddress(vpc.Instances[0]); got != "aws_vpc.main" {
		t.Errorf("FullAddress (root) = %q", got)
	}
}

func TestForEachStringKeyAddress(t *testing.T) {
	s, _ := tfstate.ParseBytes([]byte(sampleState))
	web := s.Resources[1]
	got := web.Address(web.Instances[0])
	if got != `aws_instance.web["us-east-1"]` {
		t.Errorf("address = %q", got)
	}
	if got := web.FullAddress(web.Instances[0]); got != `module.compute.aws_instance.web["us-east-1"]` {
		t.Errorf("full address = %q", got)
	}
}

func TestCountIntKeyAddress(t *testing.T) {
	s, _ := tfstate.ParseBytes([]byte(sampleState))
	subnet := s.Resources[2]
	if got := subnet.Address(subnet.Instances[0]); got != "aws_subnet.public[0]" {
		t.Errorf("address = %q (count key expected bare integer)", got)
	}
}

func TestIndex(t *testing.T) {
	s, _ := tfstate.ParseBytes([]byte(sampleState))
	idx := s.Index()
	if _, ok := idx[tfstate.AddressKey{Module: "", Mode: tfstate.ModeManaged, Type: "aws_vpc", Name: "main"}]; !ok {
		t.Error("aws_vpc.main should be indexed")
	}
	if _, ok := idx[tfstate.AddressKey{Module: "module.compute", Mode: tfstate.ModeManaged, Type: "aws_instance", Name: "web"}]; !ok {
		t.Error("module.compute.aws_instance.web should be indexed")
	}
	if _, ok := idx[tfstate.AddressKey{Module: "", Mode: tfstate.ModeManaged, Type: "nonexistent", Name: "x"}]; ok {
		t.Error("nonexistent resource should not be indexed")
	}
}

func TestParseFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terraform.tfstate")
	if err := os.WriteFile(path, []byte(sampleState), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := tfstate.Parse(path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(s.Resources) != 3 {
		t.Errorf("resource count via Parse: %d", len(s.Resources))
	}
}

func TestParseRejectsBadVersion(t *testing.T) {
	body := []byte(`{"version": 3, "resources": []}`)
	if _, err := tfstate.ParseBytes(body); err == nil {
		t.Error("expected error for version 3")
	}
}

func TestParseRejectsMalformedJSON(t *testing.T) {
	if _, err := tfstate.ParseBytes([]byte(`{not valid`)); err == nil {
		t.Error("expected error for malformed JSON")
	}
}
