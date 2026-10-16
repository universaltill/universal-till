//go:build wasip1

// Test guest for file-rows import entries (ADR-0164 §2, ut-docs#4526): on
// "import.requested.ask" it reads the staged file through import_file_*,
// parses a fixture CSV (header fixture_ref,fixture_title,fixture_price_minor)
// and hands each line back as an item row through import_rows_submit, then
// answers ok. It is a stand-in for a vendor export parser: the file, not
// plugin settings, drives it, because a file-rows ask can read no settings.
//
// Directive lines (in the title column, ref ignored):
//
//	PROBE      try the host functions a file-rows ask must refuse; the
//	           answer's message lists each one's code
//	GEN:<n>    add n generated rows
//	BATCH:<n>  rows per import_rows_submit call (default 500)
package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/sdk/plugin"
)

func main() {
	plugin.Run(plugin.Handlers{"import.requested.ask": handle})
}

func code(err error) int32 {
	if err == nil {
		return 0
	}
	var e *plugin.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return -3
}

type row struct {
	ExternalID string `json:"external_id"`
	Name       string `json:"name"`
	PriceMinor int64  `json:"price_minor"`
}

func probe() string {
	_, settingsErr := plugin.SettingsGet("anything")
	_, storageErr := plugin.StorageGet("anything")
	_, httpErr := plugin.HTTP(plugin.HTTPRequest{Method: "GET", URL: "https://probe.invalid/"})
	publishErr := plugin.EventPublish("com.test.filerows.done", map[string]int{})
	_, tzErr := plugin.DeviceTimezone()
	_, blobErr := plugin.BlobList()
	return fmt.Sprintf("probe settings_get=%d storage_get=%d http_request=%d event_publish=%d device_timezone_get=%d blob_list=%d",
		code(settingsErr), code(storageErr), code(httpErr), code(publishErr), code(tzErr), code(blobErr))
}

func handle(ev plugin.Event) (any, error) {
	var req plugin.ImportRequest
	if err := ev.Decode(&req); err != nil {
		return map[string]any{"ok": false, "error": "bad payload"}, nil
	}
	f := req.File()
	defer f.Close()
	cr := csv.NewReader(f)
	cr.FieldsPerRecord = -1
	head, err := cr.Read()
	if err != nil || len(head) < 3 || head[0] != "fixture_ref" {
		return map[string]any{"ok": false, "error": "not a fixture export"}, nil
	}
	var rows []row
	batch := 500
	message := ""
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) < 2 {
			return map[string]any{"ok": false, "error": "bad line"}, nil
		}
		title := rec[1]
		switch {
		case title == "PROBE":
			message = probe()
			continue
		case strings.HasPrefix(title, "GEN:"):
			n, _ := strconv.Atoi(strings.TrimPrefix(title, "GEN:"))
			for i := 0; i < n; i++ {
				rows = append(rows, row{ExternalID: fmt.Sprintf("g%d", i), Name: fmt.Sprintf("G%d", i), PriceMinor: 1})
			}
			continue
		case strings.HasPrefix(title, "BATCH:"):
			batch, _ = strconv.Atoi(strings.TrimPrefix(title, "BATCH:"))
			continue
		}
		price, _ := strconv.ParseInt(rec[2], 10, 64)
		rows = append(rows, row{ExternalID: rec[0], Name: title, PriceMinor: price})
	}
	sent := 0
	for start := 0; start < len(rows); start += batch {
		n, err := plugin.ImportRowsSubmit("items", rows[start:min(start+batch, len(rows))])
		if err != nil {
			return map[string]any{"ok": false, "error": fmt.Sprintf("import_rows_submit: %d", code(err))}, nil
		}
		sent += n
	}
	if message == "" {
		message = fmt.Sprintf("parsed %d rows (%s)", sent, req.Mode)
	}
	return map[string]any{"ok": true, "message": message, "counts": map[string]int{"items": sent}}, nil
}
