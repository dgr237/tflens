package resolver_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dgr237/tflens/pkg/resolver"
)

func TestStaticCredentialsTokenLookup(t *testing.T) {
	c := resolver.StaticCredentials{
		"registry.example.com": "tok1",
		"app.terraform.io":     "tok2",
	}
	if got := c.Token("registry.example.com"); got != "tok1" {
		t.Errorf("got %q, want tok1", got)
	}
	if got := c.Token("app.terraform.io"); got != "tok2" {
		t.Errorf("got %q, want tok2", got)
	}
	if got := c.Token("unknown.com"); got != "" {
		t.Errorf("unknown host: got %q, want empty", got)
	}
}

func TestLoadTerraformrcFromHappyPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terraform.rc")
	body := `
credentials "app.terraform.io" {
  token = "abc123"
}

credentials "registry.example.com" {
  token = "xyz789"
}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := resolver.LoadTerraformrcFrom(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.Token("app.terraform.io"); got != "abc123" {
		t.Errorf("app.terraform.io token = %q, want abc123", got)
	}
	if got := c.Token("registry.example.com"); got != "xyz789" {
		t.Errorf("registry.example.com token = %q, want xyz789", got)
	}
}

func TestLoadTerraformrcFromIgnoresOtherBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terraform.rc")
	body := `
plugin_cache_dir = "/var/cache/terraform"

provider_installation {
  filesystem_mirror {
    path = "/usr/share/terraform/providers"
  }
}

credentials "app.terraform.io" {
  token = "abc123"
}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := resolver.LoadTerraformrcFrom(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.Token("app.terraform.io"); got != "abc123" {
		t.Errorf("token = %q, want abc123", got)
	}
}

func TestLoadTerraformrcFromMissingFile(t *testing.T) {
	c, err := resolver.LoadTerraformrcFrom(filepath.Join(t.TempDir(), "does-not-exist.rc"))
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if got := c.Token("anything"); got != "" {
		t.Errorf("empty source should yield empty token, got %q", got)
	}
}

func TestLoadTerraformrcFromMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terraform.rc")
	if err := os.WriteFile(path, []byte(`credentials "host" { this is not valid }`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.LoadTerraformrcFrom(path); err == nil {
		t.Error("expected error for malformed file")
	}
}

func TestLoadTerraformrcFromIgnoresUnlabeledCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terraform.rc")
	body := `
credentials {
  token = "orphan"
}

credentials "good.example.com" {
  token = "keep"
}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := resolver.LoadTerraformrcFrom(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.Token("good.example.com"); got != "keep" {
		t.Errorf("good host token = %q, want keep", got)
	}
}

func TestLoadTerraformrcFromIgnoresEmptyToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terraform.rc")
	body := `
credentials "a.example.com" {
  token = ""
}
credentials "b.example.com" {
  token = "real"
}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := resolver.LoadTerraformrcFrom(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.Token("a.example.com"); got != "" {
		t.Errorf("empty-token host should return no token, got %q", got)
	}
	if got := c.Token("b.example.com"); got != "real" {
		t.Errorf("b.example.com token = %q, want real", got)
	}
}

func TestLoadTerraformrcHonoursEnvVar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.rc")
	body := `credentials "env.example.com" { token = "env-token" }`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TF_CLI_CONFIG_FILE", path)

	c, err := resolver.LoadTerraformrc()
	if err != nil {
		t.Fatalf("LoadTerraformrc: %v", err)
	}
	if got := c.Token("env.example.com"); got != "env-token" {
		t.Errorf("token = %q, want env-token", got)
	}
}
