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

// isLocalSource reports whether a Terraform module source is a local path.
func isLocalSource(source string) bool {
	return strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../")
}
