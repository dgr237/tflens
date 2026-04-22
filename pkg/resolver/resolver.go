// Package resolver materialises Terraform module references to directories
// on disk. It exists so the rest of the tool (loader, diff, whatif) can
// operate on directories without caring how the source — local path, init'd
// manifest entry, registry tarball, git archive — was obtained.
//
// PR 1 ships only the local-path and manifest-backed resolvers, which are a
// behaviour-preserving refactor of what lived in pkg/loader. The registry,
// cache, and git resolvers land in PR 2.
package resolver

import (
	"context"
	"errors"
)

// Ref identifies a module version, as addressed from one specific caller.
// The verbatim Source and Version attributes are kept so resolvers can
// decide for themselves whether a ref is addressed to them (e.g. a registry
// resolver inspects Source; a local resolver inspects Source; a manifest
// resolver inspects Key).
type Ref struct {
	// Source is the verbatim `source = "..."` attribute of the module call.
	Source string
	// Version is the verbatim `version = "..."` attribute; may be empty, a
	// pinned version, or a constraint expression.
	Version string
	// ParentDir is the absolute directory of the module containing this
	// call. Used by local-path resolution.
	ParentDir string
	// Key is the dotted key path of this call in the workspace (e.g. "vpc",
	// "vpc.sg"). Used by the manifest resolver, ignored by others.
	Key string
}

// Kind labels how a Ref was resolved. Downstream code can use this to
// decide, e.g., whether to warn about an unversioned local source.
type Kind int

const (
	KindUnknown Kind = iota
	KindLocal
	KindManifest
	KindRegistry
	KindGit
)

// Resolved describes a materialised module: a directory on disk containing
// the module's .tf files, ready to hand to loader.LoadDir.
type Resolved struct {
	Dir     string
	Version string
	Kind    Kind
}

// ErrNotApplicable signals that a resolver does not handle this particular
// Ref (e.g. LocalResolver asked about a registry source). Chain resolvers
// treat it as "try the next resolver". A real resolution failure should be
// returned as a concrete error instead.
var ErrNotApplicable = errors.New("resolver: ref not applicable")

// Resolver turns a Ref into a local directory.
type Resolver interface {
	Resolve(ctx context.Context, ref Ref) (*Resolved, error)
}
