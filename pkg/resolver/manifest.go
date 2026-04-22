package resolver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

// ManifestResolver resolves a module call by looking its dotted Key up in
// the .terraform/modules/modules.json manifest that `terraform init` writes
// into a workspace. This is how registry and git-sourced modules resolve
// to a local directory in a post-init workspace.
//
// A resolver with no loaded manifest (missing file, malformed JSON) always
// returns ErrNotApplicable — callers chain it with LocalResolver so local
// sources still work without an init'd workspace.
type ManifestResolver struct {
	byKey map[string]string
}

// ManifestWarning is the path and message for a manifest file that could
// not be read or parsed. It is returned alongside a still-usable (empty)
// resolver so callers can surface it as a non-fatal warning.
type ManifestWarning struct {
	Path string
	Msg  string
}

// NewManifestResolver reads .terraform/modules/modules.json under rootDir.
// When the file is absent, a usable empty resolver is returned with no
// warning. When the file exists but is malformed, a usable empty resolver
// is returned together with a warning describing the problem.
func NewManifestResolver(rootDir string) (*ManifestResolver, *ManifestWarning) {
	path := filepath.Join(rootDir, ".terraform", "modules", "modules.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ManifestResolver{}, nil
		}
		return &ManifestResolver{}, &ManifestWarning{Path: path, Msg: err.Error()}
	}
	var raw struct {
		Modules []struct {
			Key    string `json:"Key"`
			Source string `json:"Source"`
			Dir    string `json:"Dir"`
		} `json:"Modules"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return &ManifestResolver{}, &ManifestWarning{Path: path, Msg: err.Error()}
	}
	byKey := make(map[string]string, len(raw.Modules))
	for _, entry := range raw.Modules {
		byKey[entry.Key] = filepath.Clean(filepath.Join(rootDir, entry.Dir))
	}
	return &ManifestResolver{byKey: byKey}, nil
}

func (m *ManifestResolver) Resolve(_ context.Context, ref Ref) (*Resolved, error) {
	if m == nil || len(m.byKey) == 0 {
		return nil, ErrNotApplicable
	}
	dir, ok := m.byKey[ref.Key]
	if !ok {
		return nil, ErrNotApplicable
	}
	return &Resolved{Dir: dir, Version: ref.Version, Kind: KindManifest}, nil
}
