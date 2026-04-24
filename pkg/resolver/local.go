package resolver

import (
	"context"
	"path/filepath"
	"strings"
)

// LocalResolver resolves local-path module sources ("./foo", "../bar") by
// joining them against the caller's ParentDir.
//
// Consistent with the previous loader behaviour, a local source is returned
// even if the target directory does not exist — the loader itself surfaces
// that as a parse-time warning, not a resolver failure.
type LocalResolver struct{}

func NewLocalResolver() *LocalResolver { return &LocalResolver{} }

func (LocalResolver) Resolve(_ context.Context, ref Ref) (*Resolved, error) {
	if !isLocalSource(ref.Source) {
		return nil, ErrNotApplicable
	}
	dir := filepath.Clean(filepath.Join(ref.ParentDir, ref.Source))
	return &Resolved{Dir: dir, Kind: KindLocal}, nil
}

// IsLocalSource reports whether a Terraform module source is a local
// path (relative to the parent module). Terraform's spec for local
// sources is "starts with ./ or ../"; absolute paths are not allowed.
// Exported so callers (notably cmd/diff) can classify a child module's
// origin without depending on resolver internals.
func IsLocalSource(source string) bool {
	return strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../")
}

// isLocalSource is kept as a package-private alias for the existing
// internal callers in this package.
func isLocalSource(source string) bool { return IsLocalSource(source) }
