package cmd

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/forcenew"
)

var (
	refreshForceNewURL    string
	refreshForceNewSource string
	refreshForceNewOutput string
)

var refreshForceNewCmd = &cobra.Command{
	Use:   "refresh-force-new",
	Short: "Fetch a Crossplane runtime IR and extract a force-new override table",
	Long: `Downloads a Crossplane runtime IR JSON from --url, extracts the
minimal force-new attribute table tflens uses for breaking-change
classification, and writes it to --output. The resulting file is
suitable for use with --immutable-table-override on subsequent
commands.

The URL must point to a runtime-ir-*.json file as published by upjet
(or a compatible source). Refer to your provider distribution's
release artifacts for the canonical URL.

Example:
  tflens refresh-force-new \
    --url    https://example.org/runtime-ir-v2.5.0.json \
    --source crossplane-runtime-ir-v2.5.0 \
    --output ./force_new_override.json

  tflens diff --immutable-table-override ./force_new_override.json ...`,
	RunE: runRefreshForceNew,
}

func init() {
	refreshForceNewCmd.Flags().StringVar(&refreshForceNewURL, "url", "",
		"URL of the runtime-ir JSON file (required)")
	refreshForceNewCmd.Flags().StringVar(&refreshForceNewSource, "source", "",
		"provenance label written into the table (required, e.g. crossplane-runtime-ir-v2.5.0)")
	refreshForceNewCmd.Flags().StringVar(&refreshForceNewOutput, "output", "force_new_table.json",
		"destination path for the extracted table")
	_ = refreshForceNewCmd.MarkFlagRequired("url")
	_ = refreshForceNewCmd.MarkFlagRequired("source")
	rootCmd.AddCommand(refreshForceNewCmd)
}

func runRefreshForceNew(cmd *cobra.Command, _ []string) error {
	// 5 min handles slow links on the ~15 MB runtime IR; the default
	// http.Client timeout of 0 (no timeout) would hang indefinitely on
	// a stalled connection.
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(refreshForceNewURL)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", refreshForceNewURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: HTTP %s", refreshForceNewURL, resp.Status)
	}

	table, err := forcenew.Extract(resp.Body, refreshForceNewSource)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	f, err := os.Create(refreshForceNewOutput)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := forcenew.WriteJSON(f, table); err != nil {
		return err
	}

	totalPaths := 0
	for _, paths := range table.Resources {
		totalPaths += len(paths)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d resources, %d force-new paths) from %s\n",
		refreshForceNewOutput, len(table.Resources), totalPaths, refreshForceNewURL)
	return nil
}
