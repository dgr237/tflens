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

// extractTarGz unpacks a tar.gz stream into destDir.
//
// It is defensive against malicious archives: entries whose cleaned path
// would escape destDir (via "../" traversal or absolute paths) are
// rejected. Symlinks are rejected for the same reason — a symlink to
// "../" followed by a regular-file entry that writes through it would
// otherwise let an archive modify paths outside destDir. Registry
// modules have no legitimate need for symlinks.
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

func writeFile(target string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode|0o600)
	if err != nil {
		return fmt.Errorf("creating %q: %w", target, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("writing %q: %w", target, err)
	}
	return nil
}
