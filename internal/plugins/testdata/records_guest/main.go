//go:build wasip1

// Test guest for the plugin-records host functions (ut-docs#3697): through
// the Go SDK it reads its role, forwards one record, reads its outbox status,
// sets and reads a snapshot, then records every result in plugin storage so
// the host-side test can assert on the SDK <-> host contract end to end.
package main

import (
	"encoding/json"

	"github.com/universaltill/universal-till/sdk/plugin"
)

func main() {
	plugin.Run(plugin.Handlers{"*": handle})
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func handle(ev plugin.Event) (any, error) {
	res := map[string]any{}
	role, err := plugin.RecordsRole()
	res["role"], res["role_err"] = role, errText(err)

	queued, err := plugin.RecordsForward("entries", "rec-1", map[string]any{"n": 1})
	res["queued"], res["forward_err"] = queued, errText(err)
	_, err = plugin.RecordsForward("undeclared", "rec-1", map[string]any{"n": 1})
	res["undeclared_err"] = errText(err)

	st, err := plugin.RecordsStatus("entries")
	res["status"], res["status_err"] = st, errText(err)

	stored, err := plugin.SnapshotSet("staff", []map[string]string{{"id": "1"}})
	res["stored"], res["set_err"] = stored, errText(err)
	var got []map[string]string
	found, err := plugin.SnapshotGet("staff", &got)
	res["found"], res["got"], res["get_err"] = found, got, errText(err)

	out, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	return nil, plugin.StorageSet("results", out)
}
