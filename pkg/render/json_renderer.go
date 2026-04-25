package render

import (
	"encoding/json"
	"io"

	"github.com/hashicorp/hcl/v2"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/statediff"
)

// JSONRenderer is the structured-output Renderer implementation —
// every method builds the appropriate envelope struct and emits it as
// pretty-printed JSON to W. Wire-format struct tags live alongside
// each method so the JSON shape stays in sync with the data passed
// in by cmd.
//
// JSONRenderer never calls os.Exit; cmd handles process control. The
// renderer's only job is to write the envelope.
type JSONRenderer struct {
	W io.Writer
}

// emit pretty-prints v as JSON to W, two-space indented to match the
// historical format. Encoding errors are silently dropped — they
// shouldn't be reachable with the well-typed envelopes the renderer
// constructs.
func (j *JSONRenderer) emit(v any) {
	enc := json.NewEncoder(j.W)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// ---- single-module subcommands ----

func (j *JSONRenderer) Cycles(cycles [][]string) {
	if cycles == nil {
		cycles = [][]string{}
	}
	j.emit(struct {
		Cycles [][]string `json:"cycles"`
	}{Cycles: cycles})
}

func (j *JSONRenderer) Deps(id string, deps, dependents []string) {
	j.emit(struct {
		Entity       string   `json:"entity"`
		DependsOn    []string `json:"depends_on"`
		ReferencedBy []string `json:"referenced_by"`
	}{Entity: id, DependsOn: deps, ReferencedBy: dependents})
}

func (j *JSONRenderer) Impact(id string, affected []string) {
	if affected == nil {
		affected = []string{}
	}
	j.emit(struct {
		Entity   string   `json:"entity"`
		Affected []string `json:"affected"`
	}{Entity: id, Affected: affected})
}

func (j *JSONRenderer) Inventory(m *analysis.Module) {
	entities := make([]JSONEntity, 0, len(m.Entities()))
	for _, e := range m.Entities() {
		entities = append(entities, jsonEnt(e))
	}
	j.emit(struct {
		Total    int          `json:"total"`
		Entities []JSONEntity `json:"entities"`
	}{Total: len(entities), Entities: entities})
}

func (j *JSONRenderer) Unused(unused []analysis.Entity) {
	entities := make([]JSONEntity, 0, len(unused))
	for _, e := range unused {
		entities = append(entities, jsonEnt(e))
	}
	j.emit(struct {
		Unreferenced []JSONEntity `json:"unreferenced"`
	}{Unreferenced: entities})
}

// ---- validate ----

func (j *JSONRenderer) Validate(
	refErrs, crossErrs []analysis.ValidationError,
	typeErrs []analysis.TypeCheckError,
) {
	refJSON := make([]JSONValidationError, 0, len(refErrs))
	for _, e := range refErrs {
		refJSON = append(refJSON, jsonValErr(e))
	}
	crossJSON := make([]JSONValidationError, 0, len(crossErrs))
	for _, e := range crossErrs {
		crossJSON = append(crossJSON, jsonValErr(e))
	}
	typeJSON := make([]JSONTypeError, 0, len(typeErrs))
	for _, e := range typeErrs {
		typeJSON = append(typeJSON, jsonTypeErr(e))
	}
	j.emit(struct {
		UndefinedReferences []JSONValidationError `json:"undefined_references"`
		CrossModuleIssues   []JSONValidationError `json:"cross_module_issues"`
		TypeErrors          []JSONTypeError       `json:"type_errors"`
	}{refJSON, crossJSON, typeJSON})
}

// ---- cache ----

func (j *JSONRenderer) CacheInfo(path string, entries int, bytes int64) {
	j.emit(struct {
		Path    string `json:"path"`
		Entries int    `json:"entries"`
		Bytes   int64  `json:"bytes"`
	}{path, entries, bytes})
}

// CacheAlreadyEmpty produces the same shape as CacheInfo with zero
// counts, so JSON consumers can treat both clear-paths uniformly.
func (j *JSONRenderer) CacheAlreadyEmpty(path string) {
	j.CacheInfo(path, 0, 0)
}

// CacheCleared also reuses the CacheInfo shape — what was cleared is
// expressible as the (entries, bytes) state at the moment of removal.
func (j *JSONRenderer) CacheCleared(entries int, bytes int64, path string) {
	j.CacheInfo(path, entries, bytes)
}

// ---- ref-comparing subcommands ----

func (j *JSONRenderer) Diff(
	baseRef, path string,
	results []diff.PairResult,
	rootChanges []diff.Change,
) {
	j.emit(buildJSONDiff(baseRef, path, results, rootChanges))
}

func (j *JSONRenderer) Whatif(baseRef, path string, calls []diff.WhatifResult) {
	j.emit(buildJSONWhatif(baseRef, path, calls))
}

func (j *JSONRenderer) Statediff(result *statediff.Result) {
	j.emit(result)
}

// ---- fmt ----

// FmtParseErrors emits an array of {message, file, line, column}
// objects so JSON consumers can react to syntax failures without
// regex-parsing the text format.
func (j *JSONRenderer) FmtParseErrors(diags hcl.Diagnostics) {
	out := make([]struct {
		Message string `json:"message"`
		File    string `json:"file,omitempty"`
		Line    int    `json:"line,omitempty"`
		Column  int    `json:"column,omitempty"`
	}, 0, len(diags))
	for _, d := range diags {
		entry := struct {
			Message string `json:"message"`
			File    string `json:"file,omitempty"`
			Line    int    `json:"line,omitempty"`
			Column  int    `json:"column,omitempty"`
		}{Message: d.Error()}
		if d.Subject != nil {
			entry.File = d.Subject.Filename
			entry.Line = d.Subject.Start.Line
			entry.Column = d.Subject.Start.Column
		}
		out = append(out, entry)
	}
	j.emit(struct {
		ParseErrors []struct {
			Message string `json:"message"`
			File    string `json:"file,omitempty"`
			Line    int    `json:"line,omitempty"`
			Column  int    `json:"column,omitempty"`
		} `json:"parse_errors"`
	}{ParseErrors: out})
}
