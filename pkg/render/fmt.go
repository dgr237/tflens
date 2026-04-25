package render

import (
	"fmt"
	"io"

	"github.com/hashicorp/hcl/v2"
)

// writeFmtParseErrors prints one "parse error: <msg>" line per
// diagnostic. Used by `tflens fmt` to surface syntax failures with
// position info before exiting non-zero.
func writeFmtParseErrors(w io.Writer, diags hcl.Diagnostics) {
	for _, d := range diags {
		fmt.Fprintf(w, "parse error: %s\n", d.Error())
	}
}
