package analysis_test

import (
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
)

func TestHasEntityFindsResource(t *testing.T) {
	mod := analyseFixture(t, `
resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}

variable "env" {
  type = string
}
`)
	cases := map[string]bool{
		"resource.aws_vpc.main": true,
		"variable.env":          true,
		"resource.aws_vpc.gone": false,
		"":                      false,
		"variable.missing":      false,
	}
	for id, want := range cases {
		if got := mod.HasEntity(id); got != want {
			t.Errorf("HasEntity(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestHasEntityNilSafe(t *testing.T) {
	var nilMod *analysis.Module
	if nilMod.HasEntity("anything") {
		t.Error("nil module should report no entities")
	}
}

func TestEntityByIDReturnsValueAndOk(t *testing.T) {
	mod := analyseFixture(t, `
resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}
`)
	got, ok := mod.EntityByID("resource.aws_vpc.main")
	if !ok {
		t.Fatal("expected found=true")
	}
	if got.Type != "aws_vpc" || got.Name != "main" {
		t.Errorf("got %+v, want aws_vpc.main", got)
	}

	_, ok = mod.EntityByID("variable.gone")
	if ok {
		t.Error("expected found=false for missing entity")
	}
}

func TestEntityByIDNilSafe(t *testing.T) {
	var nilMod *analysis.Module
	if _, ok := nilMod.EntityByID("anything"); ok {
		t.Error("nil module should report not-found")
	}
}
