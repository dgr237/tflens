package resolver_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dgr237/tflens/pkg/resolver"
)

func TestLoadTfeTokensFromHappyPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	body := `
tokens:
  - address: tfe.example.com
    token: tok1
  - address: https://other.tfe.example.com
    token: tok2
  - address: bare.example.com:8443
    token: tok3
  - address: https://defaultport.example.com:443
    token: tok4
  - address: http://insecure.example.com:80
    token: tok5
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := resolver.LoadTfeTokensFrom(path)
	if err != nil {
		t.Fatalf("LoadTfeTokensFrom: %v", err)
	}
	cases := map[string]string{
		"tfe.example.com":            "tok1",
		"other.tfe.example.com":      "tok2",
		"bare.example.com:8443":      "tok3",
		"defaultport.example.com":    "tok4", // :443 stripped because scheme is https
		"insecure.example.com":       "tok5", // :80 stripped because scheme is http
		"unknown.example.com":        "",
	}
	for host, want := range cases {
		if got := c.Token(host); got != want {
			t.Errorf("Token(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestLoadTfeTokensFromMissingFile(t *testing.T) {
	c, err := resolver.LoadTfeTokensFrom(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing file should not error, got: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil empty CredentialsSource for missing file")
	}
	if got := c.Token("anything"); got != "" {
		t.Errorf("missing file should yield empty creds, got %q", got)
	}
}

func TestLoadTfeTokensFromMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	if err := os.WriteFile(path, []byte("tokens: [unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.LoadTfeTokensFrom(path); err == nil {
		t.Fatal("expected error on malformed YAML")
	}
}

func TestLoadTfeTokensFromSkipsEmptyEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	body := `
tokens:
  - address: ""
    token: orphan-token
  - address: present.example.com
    token: ""
  - address: usable.example.com
    token: keep
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := resolver.LoadTfeTokensFrom(path)
	if err != nil {
		t.Fatalf("LoadTfeTokensFrom: %v", err)
	}
	if got := c.Token("present.example.com"); got != "" {
		t.Errorf("entry with empty token should be skipped, got %q", got)
	}
	if got := c.Token("usable.example.com"); got != "keep" {
		t.Errorf("usable entry should be kept, got %q", got)
	}
}

func TestLoadTfeTokensReadsFromEnvVar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alt.yaml")
	body := `
tokens:
  - address: env.example.com
    token: env-token
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(resolver.TfeTokensFileEnv, path)
	c, err := resolver.LoadTfeTokens()
	if err != nil {
		t.Fatalf("LoadTfeTokens: %v", err)
	}
	if got := c.Token("env.example.com"); got != "env-token" {
		t.Errorf("env-pointed file: got %q, want env-token", got)
	}
}

// TestLoadTfeTokensWithEnvUnsetReturnsEmpty confirms loading is strictly
// opt-in: with $TFLENS_TFE_TOKENS_FILE unset we never touch the filesystem and
// always yield an empty, non-nil source. This avoids accidentally
// picking up an unrelated file at any conventional path.
func TestLoadTfeTokensWithEnvUnsetReturnsEmpty(t *testing.T) {
	t.Setenv(resolver.TfeTokensFileEnv, "")
	c, err := resolver.LoadTfeTokens()
	if err != nil {
		t.Fatalf("LoadTfeTokens: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil empty CredentialsSource")
	}
	if got := c.Token("anything"); got != "" {
		t.Errorf("expected empty creds with env unset, got %q", got)
	}
}

func TestMergedCredentialsFirstMatchWins(t *testing.T) {
	first := resolver.StaticCredentials{
		"shared.example.com": "first-tok",
		"only-first.example": "f",
	}
	second := resolver.StaticCredentials{
		"shared.example.com":  "second-tok",
		"only-second.example": "s",
	}
	m := resolver.MergedCredentials{first, second}
	if got := m.Token("shared.example.com"); got != "first-tok" {
		t.Errorf("shared host: got %q, want first-tok", got)
	}
	if got := m.Token("only-first.example"); got != "f" {
		t.Errorf("only-first: got %q, want f", got)
	}
	if got := m.Token("only-second.example"); got != "s" {
		t.Errorf("only-second: got %q, want s", got)
	}
	if got := m.Token("unknown"); got != "" {
		t.Errorf("unknown host: got %q, want empty", got)
	}
}

func TestMergedCredentialsSkipsNil(t *testing.T) {
	m := resolver.MergedCredentials{
		nil,
		resolver.StaticCredentials{"x": "y"},
		nil,
	}
	if got := m.Token("x"); got != "y" {
		t.Errorf("nil entries should be skipped, got %q", got)
	}
}
