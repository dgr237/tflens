// Package tfstate parses Terraform state v4 JSON files and exposes the
// subset of information tflens needs for cross-reference against code.
//
// We only look at resource identity: which (module, type, name, instance
// key) tuples exist in state. Attribute values are carried through as
// opaque JSON but tflens does not interpret them — attribute-level
// diffing requires provider schemas and expression evaluation, which is
// `terraform plan`'s job.
package tfstate

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// Mode is "managed" (resource) or "data" (data source) — matches the
// state file's own mode field.
type Mode string

const (
	ModeManaged Mode = "managed"
	ModeData    Mode = "data"
)

// Resource groups the instances of one resource block. Module is the
// dotted path from the root workspace ("" for a root-level resource,
// "module.vpc" for a nested one).
type Resource struct {
	Module    string
	Mode      Mode
	Type      string
	Name      string
	Instances []Instance
}

// Instance is one concrete expansion of a resource (singleton, count
// index, or for_each key).
type Instance struct {
	// IndexKey is json.Number-style for count (e.g. "0") or a string
	// for for_each ("us-east-1"). Empty when the resource is a
	// singleton.
	IndexKey string
	// IndexKeyIsNumber distinguishes "0" (count index) from "\"0\""
	// (for_each string key that happens to be the digit zero) when
	// rendering addresses.
	IndexKeyIsNumber bool
}

// Address returns the canonical Terraform address of the instance
// (without any module.* prefix). Examples: aws_instance.web,
// aws_instance.web[0], aws_instance.web["us-east-1"].
func (r *Resource) Address(inst Instance) string {
	base := r.Type + "." + r.Name
	if inst.IndexKey == "" {
		return base
	}
	if inst.IndexKeyIsNumber {
		return base + "[" + inst.IndexKey + "]"
	}
	return base + "[\"" + inst.IndexKey + "\"]"
}

// FullAddress prepends the module path, producing addresses that match
// those Terraform prints in plan output.
func (r *Resource) FullAddress(inst Instance) string {
	addr := r.Address(inst)
	if r.Module == "" {
		return addr
	}
	return r.Module + "." + addr
}

// State is the parsed result.
type State struct {
	Version          int
	TerraformVersion string
	Resources        []Resource
}

// Parse reads Terraform state v4 JSON from path.
func Parse(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseBytes(data)
}

// ParseBytes parses an in-memory state JSON payload.
func ParseBytes(data []byte) (*State, error) {
	var raw struct {
		Version          int    `json:"version"`
		TerraformVersion string `json:"terraform_version"`
		Resources        []struct {
			Module    string          `json:"module"`
			Mode      string          `json:"mode"`
			Type      string          `json:"type"`
			Name      string          `json:"name"`
			Instances []rawInstance   `json:"instances"`
			_         json.RawMessage `json:"-"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing state JSON: %w", err)
	}
	if raw.Version != 0 && raw.Version != 4 {
		return nil, fmt.Errorf("unsupported state version %d (expected 4)", raw.Version)
	}

	out := &State{
		Version:          raw.Version,
		TerraformVersion: raw.TerraformVersion,
		Resources:        make([]Resource, 0, len(raw.Resources)),
	}
	for _, r := range raw.Resources {
		mode := Mode(r.Mode)
		if mode == "" {
			mode = ModeManaged
		}
		res := Resource{Module: r.Module, Mode: mode, Type: r.Type, Name: r.Name}
		for _, inst := range r.Instances {
			res.Instances = append(res.Instances, inst.toInstance())
		}
		out.Resources = append(out.Resources, res)
	}
	return out, nil
}

// rawInstance holds the bits of a state instance we care about.
// The index_key field is either an integer (count) or a string
// (for_each). We distinguish at unmarshal time.
type rawInstance struct {
	IndexKey json.RawMessage `json:"index_key"`
}

func (r rawInstance) toInstance() Instance {
	if len(r.IndexKey) == 0 || string(r.IndexKey) == "null" {
		return Instance{}
	}
	// Try integer first.
	var n int64
	if err := json.Unmarshal(r.IndexKey, &n); err == nil {
		return Instance{IndexKey: strconv.FormatInt(n, 10), IndexKeyIsNumber: true}
	}
	var s string
	if err := json.Unmarshal(r.IndexKey, &s); err == nil {
		return Instance{IndexKey: s, IndexKeyIsNumber: false}
	}
	// Fall back to raw form — unusual but survives pathological state.
	return Instance{IndexKey: string(r.IndexKey), IndexKeyIsNumber: false}
}

// ByAddress indexes resources by (module, type, name). Used to look up
// "does state have this declared resource?" in O(1).
type ByAddress map[AddressKey]*Resource

// AddressKey is the composite key used by ByAddress.
type AddressKey struct {
	Module string // "" or "module.vpc"
	Mode   Mode
	Type   string
	Name   string
}

// Index builds a ByAddress lookup over s. The returned pointers alias
// into s; do not mutate the underlying Resources slice while the index
// is in use.
func (s *State) Index() ByAddress {
	out := make(ByAddress, len(s.Resources))
	for i := range s.Resources {
		r := &s.Resources[i]
		out[AddressKey{Module: r.Module, Mode: r.Mode, Type: r.Type, Name: r.Name}] = r
	}
	return out
}
