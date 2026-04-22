package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/lsp"
)

var lspCmd = &cobra.Command{
	Use:   "lsp",
	Short: "Run as a Language Server Protocol server over stdio",
	Long: `Runs an LSP server on stdin/stdout, exposing tflens's analysis,
validation, and formatting to any LSP-aware editor.

Capabilities in this v1:
  - Diagnostics: parse errors, undefined references, type errors
  - Hover: variable / entity details at cursor
  - Go-to-definition: jump from a ref (var.x, local.y, etc.) to its declaration
  - Document symbols: outline view of all entities in the file
  - Formatting: format the whole document

Editor hookup (examples):
  - Neovim (nvim-lspconfig):
      require('lspconfig').terraform.setup({ cmd = { 'tflens', 'lsp' } })
  - VS Code: needs a thin extension wrapper (not yet shipped).
  - Helix / Zed / Emacs: configure as an LSP server pointing at 'tflens lsp'.

Logging goes to stderr; stdout is reserved for the protocol.`,
	Args: cobra.ExactArgs(0),
	Run: func(cmd *cobra.Command, args []string) {
		lsp.Serve()
	},
}

func init() {
	rootCmd.AddCommand(lspCmd)
}
