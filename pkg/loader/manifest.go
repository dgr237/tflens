package loader

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// moduleManifest mirrors .terraform/modules/modules.json. Each entry's Dir is
// relative to the workspace root where `terraform init` was run.
//
// Example:
//
//	{
//	  "Modules": [
//	    {"Key": "",       "Source": "",                                "Dir": "."},
//	    {"Key": "vpc",    "Source": "terraform-aws-modules/vpc/aws",   "Dir": ".terraform/modules/vpc"},
//	    {"Key": "vpc.sg", "Source": "./submodules/security-group",     "Dir": ".terraform/modules/vpc/submodules/security-group"}
//	  ]
//	}
type moduleManifest struct {
	// byKey maps the dotted key (e.g. "vpc", "vpc.sg") to an absolute directory.
	byKey map[string]string
}

// lookup returns the resolved directory for a module-call key path, or ""
// when no matching entry exists.
func (m *moduleManifest) lookup(key string) string {
	if m == nil {
		return ""
	}
	return m.byKey[key]
}

// readManifest reads .terraform/modules/modules.json relative to rootDir.
// Returns (nil, nil) when the manifest is absent — this is the normal case
// for a workspace that has not been initialised.
func readManifest(rootDir string) (*moduleManifest, error) {
	path := filepath.Join(rootDir, ".terraform", "modules", "modules.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var raw struct {
		Modules []struct {
			Key    string `json:"Key"`
			Source string `json:"Source"`
			Dir    string `json:"Dir"`
		} `json:"Modules"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	m := &moduleManifest{byKey: make(map[string]string, len(raw.Modules))}
	for _, entry := range raw.Modules {
		abs := filepath.Clean(filepath.Join(rootDir, entry.Dir))
		m.byKey[entry.Key] = abs
	}
	return m, nil
}
