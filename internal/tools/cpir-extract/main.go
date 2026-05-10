// Command cpir-extract reads a Crossplane runtime IR (the
// runtime-ir-*.json shipped by upjet) and emits the minimal force-new
// table tflens embeds for breaking-change classification. Run via:
//
//	go run ./internal/tools/cpir-extract \
//	  -in   runtime-ir-v2.5.0-9fb84fc37179.json \
//	  -source crossplane-runtime-ir-v2.5.0-9fb84fc37179
//
// All extraction logic lives in pkg/forcenew so the CLI's
// `tflens refresh-force-new` subcommand and this build-time tool share
// a single implementation.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/dgr237/tflens/pkg/forcenew"
)

func main() {
	in := flag.String("in", "", "path to runtime-ir-*.json")
	out := flag.String("out", "pkg/forcenew/data/force_new_table.json", "output path for the pretty-printed table")
	source := flag.String("source", "", "source label for provenance, e.g. crossplane-runtime-ir-v2.5.0-9fb84fc37179")
	flag.Parse()

	if *in == "" || *source == "" {
		fmt.Fprintln(os.Stderr, "usage: cpir-extract -in <runtime-ir.json> -source <label> [-out <path>]")
		os.Exit(2)
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0755); err != nil {
		log.Fatal(err)
	}

	rf, err := os.Open(*in)
	if err != nil {
		log.Fatal(err)
	}
	defer rf.Close()

	t, err := forcenew.Extract(rf, *source)
	if err != nil {
		log.Fatal(err)
	}

	wf, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer wf.Close()

	if err := forcenew.WriteJSON(wf, t); err != nil {
		log.Fatal(err)
	}

	totalPaths := 0
	for _, paths := range t.Resources {
		totalPaths += len(paths)
	}
	log.Printf("wrote %s (%d resources, %d force-new paths)", *out, len(t.Resources), totalPaths)
}
