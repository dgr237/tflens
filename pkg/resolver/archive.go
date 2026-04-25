package resolver

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxArchiveFileSize caps how many bytes any single tar entry may
// contribute. Registry-supplied .tf files are kilobytes at most;
// 100 MiB is generous for legitimate cases (test fixtures, vendored
// binaries) and small enough that an attacker-supplied tarball
// declaring an absurd Size in its header can't fill the cache disk.
//
// var (not const) so the size-cap test can shrink it temporarily
// without allocating 100 MiB of test fixture data.
var maxArchiveFileSize int64 = 100 << 20

// extractTarGz unpacks a tar.gz stream into destDir.
//
// It is defensive against malicious archives: entries whose cleaned path
// would escape destDir (via "../" traversal or absolute paths) are
// rejected. Symlinks are rejected for the same reason — a symlink to
// "../" followed by a regular-file entry that writes through it would
// otherwise let an archive modify paths outside destDir. Registry
// modules have no legitimate need for symlinks. Per-file size is
// bounded by maxArchiveFileSize so an attacker-controlled tar header
// can't induce an unbounded write.
func extractTarGz(r io.Reader, destDir string) error {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("opening gzip stream: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading tar header: %w", err)
		}

		target, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("mkdir %q: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir parent of %q: %w", target, err)
			}
			if err := writeFile(target, tr, os.FileMode(hdr.Mode&0o777)); err != nil {
				return err
			}
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("archive contains symlink or hard link at %q; not supported", hdr.Name)
		default:
			// Skip anything else (device nodes, FIFOs, etc.) — registry
			// tarballs never contain these.
		}
	}
}

// safeJoin joins name onto root, rejecting any entry whose path contains a
// ".." segment. Absolute paths are tolerated by stripping the leading
// slash and rebasing under root — some archive producers emit them.
func safeJoin(root, name string) (string, error) {
	slash := filepath.ToSlash(name)
	for seg := range strings.SplitSeq(slash, "/") {
		if seg == ".." {
			return "", fmt.Errorf("archive entry %q escapes destination", name)
		}
	}
	clean := strings.TrimLeft(slash, "/")
	clean = filepath.Clean(clean)
	if clean == "" || clean == "." {
		return root, nil
	}
	return filepath.Join(root, clean), nil
}

// writeFile writes the bounded body of one tar entry to target. The
// reader is wrapped in a LimitReader capped at maxArchiveFileSize +
// 1 so that an attacker-controlled tar header declaring an absurd
// Size can't cause an unbounded write — overshoot triggers an
// explicit error rather than filling the cache disk.
//
// Close errors are surfaced explicitly: a deferred Close that
// discards its error can mask a buffered-write flush failure
// (out-of-disk during cache populate, network-FS hiccup) and leave
// a truncated file in the cache that subsequent runs would read as
// valid HCL.
func writeFile(target string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode|0o600)
	if err != nil {
		return fmt.Errorf("creating %q: %w", target, err)
	}
	limited := &io.LimitedReader{R: r, N: maxArchiveFileSize + 1}
	n, copyErr := io.Copy(f, limited)
	closeErr := f.Close()
	if copyErr != nil {
		return fmt.Errorf("writing %q: %w", target, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing %q: %w", target, closeErr)
	}
	if n > maxArchiveFileSize {
		return fmt.Errorf("archive entry %q exceeds %d-byte size cap", target, maxArchiveFileSize)
	}
	return nil
}
