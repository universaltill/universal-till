//go:build wasip1

// Test guest for the export/report dispatcher (ut-docs#189, extended for
// ut-docs#221). Reads the incoming "export.requested.ask" event from stdin
// and writes a JSON answer to stdout — stdout is the answer channel for
// ".ask"-suffixed events (see internal/plugins/wasm_runtime.go's
// HandleEvent), the same convention ut-plugin-tax-de's handleTaxRateAsk
// uses.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// exportSaleRow mirrors data.ExportSaleRow's wire shape (internal/data/
// export_repo.go) — this guest is a real WASM module proving the sales data
// the host gathers actually reaches a plugin, not just that the Go structs
// compile, so it decodes the real payload shape rather than a stand-in.
type exportSaleRow struct {
	ReceiptNo string `json:"receipt_no"`
	CreatedAt string `json:"created_at"`
	Total     int64  `json:"total"`
	TaxLines  []struct {
		RateBP int64 `json:"rate_bp"`
		Net    int64 `json:"net"`
		Tax    int64 `json:"tax"`
	} `json:"tax_lines"`
	Payments []struct {
		Method string `json:"method"`
		Amount int64  `json:"amount"`
	} `json:"payments"`
}

type exportRequestPayload struct {
	From     string          `json:"from"`
	To       string          `json:"to"`
	EntryKey string          `json:"entry_key"`
	Sales    []exportSaleRow `json:"sales"`
}

type incomingEvent struct {
	Payload exportRequestPayload `json:"payload"`
}

func main() {
	raw, _ := io.ReadAll(os.Stdin)
	var ev incomingEvent
	// A malformed/missing payload just yields an empty Sales slice —
	// count/sum report 0/0 rather than the guest crashing, since this
	// fixture's job is proving real data arrives, not validating it.
	_ = json.Unmarshal(raw, &ev)

	var sum int64
	for _, s := range ev.Payload.Sales {
		sum += s.Total
	}

	content := []byte("id,total\n1,9.99\n")
	fmt.Printf(`{"ok":true,"filename":"export.csv","content_b64":"%s","message":"count=%d sum=%d"}`+"\n",
		base64.StdEncoding.EncodeToString(content), len(ev.Payload.Sales), sum)
}
