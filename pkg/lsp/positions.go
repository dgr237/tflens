package lsp

import (
	"net/url"
	"path/filepath"
	"strings"

	"github.com/dgr237/tflens/pkg/token"
)

// toLSPPos converts our 1-based (line, column) into LSP's 0-based (line,
// character). This is a byte-based approximation — correct for ASCII and
// mostly correct for Terraform configs, which rarely contain wide Unicode.
func toLSPPos(p token.Position) Position {
	return Position{Line: p.Line - 1, Character: p.Column - 1}
}

// toLSPRange returns a range that starts at p and extends width bytes. If
// width is 0 the range is empty (valid "caret" position).
func toLSPRange(p token.Position, width int) Range {
	start := toLSPPos(p)
	end := start
	end.Character += width
	return Range{Start: start, End: end}
}

// fromLSPPos converts 0-based LSP (line, character) into our 1-based
// (Line, Column). File is not populated by this function.
func fromLSPPos(p Position) token.Position {
	return token.Position{Line: p.Line + 1, Column: p.Character + 1}
}

// uriToPath converts a file:// URI into a filesystem path. Handles the
// Windows file:///C:/... convention.
func uriToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	p := u.Path
	// Windows: /C:/path -> C:/path
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

// pathToURI converts a filesystem path into a file:// URI.
func pathToURI(p string) string {
	p = filepath.ToSlash(p)
	if strings.HasPrefix(p, "/") {
		return "file://" + p
	}
	// Windows drive letter: C:/foo -> file:///C:/foo
	return "file:///" + p
}
