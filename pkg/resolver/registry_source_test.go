package resolver

import "testing"

func TestParseRegistrySourceThreePart(t *testing.T) {
	got, ok := parseRegistrySource("terraform-aws-modules/vpc/aws")
	if !ok {
		t.Fatal("expected ok for 3-part source")
	}
	if got.host != "" {
		t.Errorf("host = %q, want empty (default)", got.host)
	}
	if got.ns != "terraform-aws-modules" || got.name != "vpc" || got.provider != "aws" {
		t.Errorf("parsed = %+v", got)
	}
	if got.subdir != "" {
		t.Errorf("subdir = %q, want empty", got.subdir)
	}
}

func TestParseRegistrySourceFourPart(t *testing.T) {
	got, ok := parseRegistrySource("registry.example.com/ns/name/aws")
	if !ok {
		t.Fatal("expected ok for 4-part source")
	}
	if got.host != "registry.example.com" {
		t.Errorf("host = %q", got.host)
	}
	if got.ns != "ns" || got.name != "name" || got.provider != "aws" {
		t.Errorf("parsed = %+v", got)
	}
}

func TestParseRegistrySourceWithSubdir(t *testing.T) {
	got, ok := parseRegistrySource("ns/name/aws//modules/child")
	if !ok {
		t.Fatal("expected ok")
	}
	if got.subdir != "modules/child" {
		t.Errorf("subdir = %q, want modules/child", got.subdir)
	}
}

func TestParseRegistrySourceRejectsLocal(t *testing.T) {
	for _, s := range []string{"./foo", "../bar", "./ns/name/aws"} {
		if _, ok := parseRegistrySource(s); ok {
			t.Errorf("parseRegistrySource(%q) should fail (local path)", s)
		}
	}
}

func TestParseRegistrySourceRejectsForcedScheme(t *testing.T) {
	bad := []string{
		"git::https://github.com/foo/bar.git",
		"hg::https://example.com/repo",
		"s3::https://bucket.s3.amazonaws.com/key",
	}
	for _, s := range bad {
		if _, ok := parseRegistrySource(s); ok {
			t.Errorf("parseRegistrySource(%q) should fail (forced scheme)", s)
		}
	}
}

func TestParseRegistrySourceRejectsURL(t *testing.T) {
	bad := []string{
		"https://example.com/mod.tar.gz",
		"http://example.com/mod",
		"git@github.com:foo/bar.git",
	}
	for _, s := range bad {
		if _, ok := parseRegistrySource(s); ok {
			t.Errorf("parseRegistrySource(%q) should fail", s)
		}
	}
}

func TestParseRegistrySourceRejectsVCSShorthand(t *testing.T) {
	// 3-part sources whose first segment is a known VCS host are git
	// shorthand, not registry sources.
	bad := []string{
		"github.com/foo/bar",
		"bitbucket.org/foo/bar",
		"gitlab.com/foo/bar",
	}
	for _, s := range bad {
		if _, ok := parseRegistrySource(s); ok {
			t.Errorf("parseRegistrySource(%q) should fail (VCS shorthand)", s)
		}
	}
}

func TestParseRegistrySourceRejectsBadSegmentCounts(t *testing.T) {
	bad := []string{
		"",
		"ns",
		"ns/name",
		"a/b/c/d/e",
	}
	for _, s := range bad {
		if _, ok := parseRegistrySource(s); ok {
			t.Errorf("parseRegistrySource(%q) should fail", s)
		}
	}
}

func TestParseRegistrySourceRejectsFourPartWithoutHostlikeFirst(t *testing.T) {
	// A 4-part source whose first segment doesn't contain a dot can't be
	// a host — it's probably malformed.
	if _, ok := parseRegistrySource("hashicorp/consul/aws/extra"); ok {
		t.Error("4-part with non-hostlike first segment should fail")
	}
}

func TestParseRegistrySourceRejectsEmptySegments(t *testing.T) {
	bad := []string{"ns//name/aws", "/name/aws", "ns/name/"}
	for _, s := range bad {
		if _, ok := parseRegistrySource(s); ok {
			t.Errorf("parseRegistrySource(%q) should fail (empty segment)", s)
		}
	}
}
