package cmd

import (
	"encoding/json"
	"os"
)

// emitJSON writes v to stdout as pretty-printed JSON. Exits on
// encoding failure (which should be impossible in practice with
// well-typed inputs).
//
// JSON-shape adapter functions and their wire-format struct types
// live in pkg/render — see JSONPos / JSONEnt / JSONValErr /
// JSONTypeErr / JSONChg.
func emitJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fatalf("encoding JSON: %v", err)
	}
}

// exitJSON writes v and then exits with code. Used by validate / diff /
// whatif / statediff to signal findings via exit code while still
// emitting structured output.
func exitJSON(v any, code int) {
	emitJSON(v)
	if code != 0 {
		os.Exit(code)
	}
}
