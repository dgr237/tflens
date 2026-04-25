package resolver

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarEntry is the bits we need to write a tar entry in tests.
type tarEntry struct {
	name     string
	body     string
	typeflag byte
	mode     int64
}

// buildTarGz produces an in-memory tar.gz stream containing entries.
func buildTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     e.mode,
			Size:     int64(len(e.body)),
			Typeflag: e.typeflag,
		}
		if hdr.Mode == 0 && e.typeflag == tar.TypeReg {
			hdr.Mode = 0o644
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader: %v", err)
		}
		if e.typeflag == tar.TypeReg && e.body != "" {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("Write: %v", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}

func TestExtractTarGzWritesFilesAndDirs(t *testing.T) {
	data := buildTarGz(t, []tarEntry{
		{name: "main.tf", body: "# top", typeflag: tar.TypeReg},
		{name: "modules/", typeflag: tar.TypeDir, mode: 0o755},
		{name: "modules/child/variables.tf", body: "variable \"x\" {}", typeflag: tar.TypeReg},
	})
	dest := t.TempDir()
	if err := extractTarGz(bytes.NewReader(data), dest); err != nil {
		t.Fatalf("extract: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dest, "main.tf"))
	if err != nil || string(body) != "# top" {
		t.Errorf("main.tf = %q (err=%v)", body, err)
	}
	body, err = os.ReadFile(filepath.Join(dest, "modules", "child", "variables.tf"))
	if err != nil || string(body) != "variable \"x\" {}" {
		t.Errorf("variables.tf = %q (err=%v)", body, err)
	}
}

func TestExtractTarGzRejectsPathTraversal(t *testing.T) {
	data := buildTarGz(t, []tarEntry{
		{name: "../evil.tf", body: "pwned", typeflag: tar.TypeReg},
	})
	err := extractTarGz(bytes.NewReader(data), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "escapes destination") {
		t.Errorf("expected traversal rejection, got: %v", err)
	}
}

func TestExtractTarGzRejectsAbsolutePaths(t *testing.T) {
	data := buildTarGz(t, []tarEntry{
		{name: "/etc/passwd", body: "x", typeflag: tar.TypeReg},
	})
	dest := t.TempDir()
	if err := extractTarGz(bytes.NewReader(data), dest); err != nil {
		// Accepting: some archives prefix "/" — we normalise it. We just
		// require nothing escapes dest.
		t.Fatalf("absolute path: expected acceptance under dest, got %v", err)
	}
	// Should have created dest/etc/passwd, not written /etc/passwd.
	if _, err := os.Stat(filepath.Join(dest, "etc", "passwd")); err != nil {
		t.Errorf("absolute path should have been rebased under dest: %v", err)
	}
}

func TestExtractTarGzRejectsSymlinks(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{
		Name:     "escape",
		Linkname: "../outside",
		Typeflag: tar.TypeSymlink,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()

	err := extractTarGz(bytes.NewReader(buf.Bytes()), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("expected symlink rejection, got: %v", err)
	}
}

func TestExtractTarGzRejectsCorruptGzip(t *testing.T) {
	err := extractTarGz(bytes.NewReader([]byte("not a gzip")), t.TempDir())
	if err == nil {
		t.Error("expected error for non-gzip input")
	}
}

// TestExtractTarGzRejectsOversizeEntry pins the per-file size cap.
// A tar entry whose body exceeds maxArchiveFileSize must surface as
// an error rather than silently writing the full contents — defends
// against a malicious or corrupt tarball declaring an absurd Size in
// its header that would otherwise fill the cache disk.
//
// Shrinks the cap to 64 bytes for the test to avoid allocating the
// production 100 MiB of fixture data.
func TestExtractTarGzRejectsOversizeEntry(t *testing.T) {
	prev := maxArchiveFileSize
	maxArchiveFileSize = 64
	t.Cleanup(func() { maxArchiveFileSize = prev })

	body := strings.Repeat("x", 65) // one byte over cap
	data := buildTarGz(t, []tarEntry{
		{name: "huge.tf", body: body, typeflag: tar.TypeReg},
	})
	err := extractTarGz(bytes.NewReader(data), t.TempDir())
	if err == nil {
		t.Fatal("expected error for oversize entry")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error = %q, want one mentioning the size cap", err.Error())
	}
}

// TestExtractTarGzAcceptsExactlyAtCap: an entry whose body equals
// the cap (not over) is fine. Pins the boundary so a future off-by-
// one in the comparison doesn't quietly break legitimate archives.
func TestExtractTarGzAcceptsExactlyAtCap(t *testing.T) {
	prev := maxArchiveFileSize
	maxArchiveFileSize = 64
	t.Cleanup(func() { maxArchiveFileSize = prev })

	body := strings.Repeat("x", 64) // exactly the cap
	data := buildTarGz(t, []tarEntry{
		{name: "ok.tf", body: body, typeflag: tar.TypeReg},
	})
	dest := t.TempDir()
	if err := extractTarGz(bytes.NewReader(data), dest); err != nil {
		t.Fatalf("extractTarGz at-cap: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "ok.tf"))
	if err != nil {
		t.Fatalf("read extracted file: %v", err)
	}
	if len(got) != 64 {
		t.Errorf("extracted size = %d, want 64", len(got))
	}
}
