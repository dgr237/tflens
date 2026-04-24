package loader_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dgr237/tflens/pkg/loader"
)

// TestLoadFileHappyPath: a syntactically-valid single .tf file
// produces a non-nil module with no FileError entries.
func TestLoadFileHappyPath(t *testing.T) {
	path := writeTmpFile(t, "main.tf", `
resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}
`)
	mod, fileErrs, err := loader.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(fileErrs) != 0 {
		t.Errorf("expected no file errors, got %v", fileErrs)
	}
	if mod == nil {
		t.Fatal("expected non-nil module")
	}
	if !mod.HasEntity("resource.aws_vpc.main") {
		t.Error("expected resource.aws_vpc.main to be present")
	}
}

// TestLoadFileMissingPathReturnsError: an absent file is a fatal
// I/O error (not a soft FileError).
func TestLoadFileMissingPathReturnsError(t *testing.T) {
	_, _, err := loader.LoadFile(filepath.Join(t.TempDir(), "nope.tf"))
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// TestLoadFileParseErrorReturnsFileError: a syntactically-broken
// .tf file is a soft FileError (returned alongside a nil module),
// not a top-level error — so callers can decide policy.
func TestLoadFileParseErrorReturnsFileError(t *testing.T) {
	path := writeTmpFile(t, "broken.tf", `resource "missing-second-label" {`)
	mod, fileErrs, err := loader.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile should not return top-level error for parse failure, got: %v", err)
	}
	if mod != nil {
		t.Error("expected nil module when parse failed")
	}
	if len(fileErrs) != 1 {
		t.Fatalf("expected 1 FileError, got %d", len(fileErrs))
	}
	if fileErrs[0].Path != path {
		t.Errorf("FileError.Path = %q, want %q", fileErrs[0].Path, path)
	}
	if len(fileErrs[0].Errors) == 0 {
		t.Error("FileError.Errors should be populated with at least one ParseError")
	}
}

// TestLoadAnyDispatchesByPathType: directory paths route to LoadDir,
// file paths route to LoadFile. Confirmed by loading a fixture with
// each shape and checking the loaded entity set.
func TestLoadAnyDispatchesByPathType(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"),
		[]byte(`variable "from_dir" { type = string }`), 0o644); err != nil {
		t.Fatal(err)
	}
	dirMod, _, err := loader.LoadAny(dir)
	if err != nil {
		t.Fatalf("LoadAny(dir): %v", err)
	}
	if !dirMod.HasEntity("variable.from_dir") {
		t.Error("dir load missed variable.from_dir")
	}

	filePath := writeTmpFile(t, "single.tf", `variable "from_file" { type = string }`)
	fileMod, _, err := loader.LoadAny(filePath)
	if err != nil {
		t.Fatalf("LoadAny(file): %v", err)
	}
	if !fileMod.HasEntity("variable.from_file") {
		t.Error("file load missed variable.from_file")
	}
}

func TestLoadAnyMissingPathReturnsError(t *testing.T) {
	_, _, err := loader.LoadAny(filepath.Join(t.TempDir(), "nowhere"))
	if err == nil {
		t.Error("expected error for missing path")
	}
}

// TestLoadFileParseErrorMessageHasPosition pins down that ParseError
// entries carry a position (file/line/column). Required for the cmd
// layer's "warning: parse errors in <file>" rendering to be useful.
func TestLoadFileParseErrorMessageHasPosition(t *testing.T) {
	// Three blank lines before a broken declaration — the ParseError
	// position should reflect the actual line number, not 1.
	path := writeTmpFile(t, "broken.tf", "\n\n\nresource \"missing-second-label\" {")
	_, fileErrs, _ := loader.LoadFile(path)
	if len(fileErrs) != 1 || len(fileErrs[0].Errors) == 0 {
		t.Fatal("expected at least one ParseError")
	}
	pe := fileErrs[0].Errors[0]
	if pe.Pos.Line < 4 {
		t.Errorf("ParseError.Pos.Line = %d, want >= 4", pe.Pos.Line)
	}
	if pe.Msg == "" {
		t.Error("ParseError.Msg should be non-empty")
	}
}

func writeTmpFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
