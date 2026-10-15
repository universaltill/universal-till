//go:build wasip1

// Test guest for API-source imports (ADR-0161, ut-docs#4429): on
// "import.requested.ask" it submits catalogue rows back to the till through
// import_rows_submit, in batches, then answers ok. What it submits comes
// from its own plugin settings, so one compiled module serves every test:
//
//	rows   JSON array of item rows to submit as is
//	gen    generate this many rows {"external_id":"g<i>","name":"G<i>","price_minor":1}
//	batch  rows per import_rows_submit call (default 500)
//	entity the entity to submit for (default "items")
//	answer "decline" answers ok:false after submitting
//	ignore_errors "1" keeps going (and answers ok) after a failed submit
//
// A failing submit answers ok:false with the host code, so a test can tell
// which gate refused it.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/universaltill/universal-till/sdk/plugin"
)

func main() {
	plugin.Run(plugin.Handlers{"*": handle})
}

func setting(key string) string {
	v, err := plugin.SettingsGet(key)
	if err != nil {
		return ""
	}
	return v
}

func code(err error) int32 {
	var e *plugin.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return -3
}

func handle(ev plugin.Event) (any, error) {
	var rows []json.RawMessage
	if raw := setting("rows"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &rows); err != nil {
			return map[string]any{"ok": false, "error": "bad rows setting"}, nil
		}
	}
	if n, _ := strconv.Atoi(setting("gen")); n > 0 {
		for i := 0; i < n; i++ {
			rows = append(rows, json.RawMessage(fmt.Sprintf(`{"external_id":"g%d","name":"G%d","price_minor":1}`, i, i)))
		}
	}
	batch, _ := strconv.Atoi(setting("batch"))
	if batch <= 0 {
		batch = 500
	}
	entity := setting("entity")
	if entity == "" {
		entity = "items"
	}
	sent := 0
	for start := 0; start < len(rows) || (start == 0 && len(rows) == 0); start += batch {
		end := min(start+batch, len(rows))
		n, err := plugin.ImportRowsSubmit(entity, rows[start:end])
		if err != nil && setting("ignore_errors") != "1" {
			return map[string]any{"ok": false, "error": fmt.Sprintf("import_rows_submit: %d", code(err))}, nil
		}
		sent += n
		if len(rows) == 0 {
			break
		}
	}
	if setting("answer") == "decline" {
		return map[string]any{"ok": false, "error": "declined by the test guest"}, nil
	}
	return map[string]any{"ok": true, "message": fmt.Sprintf("submitted %d", sent), "counts": map[string]int{entity: sent}}, nil
}
