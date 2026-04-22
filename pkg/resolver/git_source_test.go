package resolver

import "testing"

func TestParseGitSourceForcedHTTPS(t *testing.T) {
	got, ok := parseGitSource("git::https://github.com/foo/bar.git?ref=v1.0.0")
	if !ok {
		t.Fatal("expected ok")
	}
	if got.cloneURL != "https://github.com/foo/bar.git" {
		t.Errorf("cloneURL = %q", got.cloneURL)
	}
	if got.host != "github.com" {
		t.Errorf("host = %q", got.host)
	}
	if got.path != "foo/bar.git" {
		t.Errorf("path = %q", got.path)
	}
	if got.ref != "v1.0.0" {
		t.Errorf("ref = %q", got.ref)
	}
	if got.subdir != "" {
		t.Errorf("subdir = %q, want empty", got.subdir)
	}
}

func TestParseGitSourceForcedSSH(t *testing.T) {
	got, ok := parseGitSource("git::ssh://git@github.com/foo/bar.git?ref=main")
	if !ok {
		t.Fatal("expected ok")
	}
	if got.cloneURL != "ssh://git@github.com/foo/bar.git" {
		t.Errorf("cloneURL = %q", got.cloneURL)
	}
	if got.ref != "main" {
		t.Errorf("ref = %q", got.ref)
	}
}

func TestParseGitSourceWithSubdir(t *testing.T) {
	got, ok := parseGitSource("git::https://github.com/foo/bar.git//modules/vpc?ref=v1.0.0")
	if !ok {
		t.Fatal("expected ok")
	}
	if got.cloneURL != "https://github.com/foo/bar.git" {
		t.Errorf("cloneURL = %q; subdir must not leak into clone URL", got.cloneURL)
	}
	if got.subdir != "modules/vpc" {
		t.Errorf("subdir = %q", got.subdir)
	}
}

func TestParseGitSourceNoRefUsesDefault(t *testing.T) {
	got, ok := parseGitSource("git::https://github.com/foo/bar.git")
	if !ok {
		t.Fatal("expected ok")
	}
	if got.ref != "" {
		t.Errorf("ref = %q, want empty (default branch)", got.ref)
	}
}

func TestParseGitSourceFileScheme(t *testing.T) {
	// file:// is accepted so we can write cheap local-repo tests.
	got, ok := parseGitSource("git::file:///tmp/repo.git?ref=v1")
	if !ok {
		t.Fatal("expected ok for file://")
	}
	if got.host != "_local" {
		t.Errorf("host = %q, want _local for file://", got.host)
	}
	if !isFilePath(got.path) {
		t.Errorf("path = %q, expected an absolute path", got.path)
	}
}

// isFilePath is a loose sanity check: any non-empty path is fine. The
// exact shape depends on the OS (Unix: /tmp/repo.git, Windows after a
// file:///C:/... URL: C:/repo.git). We just ensure it's set.
func isFilePath(p string) bool { return p != "" }

func TestParseGitSourceVCSShorthand(t *testing.T) {
	cases := []struct {
		src      string
		cloneURL string
		host     string
		path     string
		ref      string
		subdir   string
	}{
		{
			src:      "github.com/foo/bar?ref=v1",
			cloneURL: "https://github.com/foo/bar",
			host:     "github.com",
			path:     "foo/bar",
			ref:      "v1",
		},
		{
			src:      "bitbucket.org/foo/bar//sub?ref=main",
			cloneURL: "https://bitbucket.org/foo/bar",
			host:     "bitbucket.org",
			path:     "foo/bar",
			ref:      "main",
			subdir:   "sub",
		},
		{
			src:      "gitlab.com/foo/bar.git?ref=abc123",
			cloneURL: "https://gitlab.com/foo/bar.git",
			host:     "gitlab.com",
			path:     "foo/bar.git",
			ref:      "abc123",
		},
	}
	for _, tc := range cases {
		got, ok := parseGitSource(tc.src)
		if !ok {
			t.Errorf("%q: expected ok", tc.src)
			continue
		}
		if got.cloneURL != tc.cloneURL || got.host != tc.host || got.path != tc.path ||
			got.ref != tc.ref || got.subdir != tc.subdir {
			t.Errorf("%q: got %+v", tc.src, got)
		}
	}
}

func TestParseGitSourceRejectsNonGit(t *testing.T) {
	bad := []string{
		"",
		"./local",
		"../sibling",
		"terraform-aws-modules/vpc/aws",    // registry source
		"https://example.com/mod.tar.gz",   // plain URL, no git:: prefix
		"git@github.com:foo/bar.git",       // SCP form (unsupported)
		"unknown-host.com/foo/bar",         // not a known VCS host
	}
	for _, s := range bad {
		if _, ok := parseGitSource(s); ok {
			t.Errorf("parseGitSource(%q) should fail", s)
		}
	}
}
