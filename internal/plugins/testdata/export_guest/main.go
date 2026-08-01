//go:build wasip1

// Test guest for the export/report dispatcher (ut-docs#189). Reads the
// incoming "export.requested.ask" event from stdin and writes a fixed JSON
// answer to stdout — stdout is the answer channel for ".ask"-suffixed
// events (see internal/plugins/wasm_runtime.go's HandleEvent), the same
// convention ut-plugin-tax-de's handleTaxRateAsk uses.
package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
)

func main() {
	// The event JSON (id/type/timestamp/payload with from/to/entry_key) is
	// read but not inspected — this guest exists to prove the dispatch
	// reaches a real compiled wasm module and its answer round-trips, not
	// to exercise payload parsing (that's exportRequestPayload's job on the
	// host side).
	_, _ = io.ReadAll(os.Stdin)

	content := []byte("id,total\n1,9.99\n")
	fmt.Printf(`{"ok":true,"filename":"export.csv","content_b64":"%s"}`+"\n",
		base64.StdEncoding.EncodeToString(content))
}
