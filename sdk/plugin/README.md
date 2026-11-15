# Universal Till Go guest SDK

`github.com/universaltill/universal-till/sdk/plugin`: the supported way for a
Go WASM plugin to call the till (ADR-0121 F4, ut-docs#3951). Standard
library only.

```sh
go get github.com/universaltill/universal-till/sdk/plugin@latest
GOOS=wasip1 GOARCH=wasm go build -o bin/plugin.wasm .
```

```go
func main() {
	plugin.Run(plugin.Handlers{
		"tax.rate.ask": func(e plugin.Event) (any, error) {
			var p struct{ SKU string `json:"sku"` }
			if err := e.Decode(&p); err != nil {
				return nil, err
			}
			return map[string]int{"rate_bp": 2000}, nil
		},
	})
}
```

- `Run` reads the event, dispatches on its type (`"*"` catches the rest),
  writes the answer (`nil` = no opinion), exits non-zero on a handler error.
- One typed wrapper per host function; errors match `ErrNotFound` …
  `ErrBusy` with `errors.Is`.
- Tests run natively: `plugin.UseFakeHost(t, plugin.NewFakeHost())`.
- **File-rows import entries** (`"source": "file"`, ADR-0164 §2): decode the
  ask into `plugin.ImportRequest`, read `req.File()`, hand rows back with
  `ImportRowsSubmit`. `req.Locale` is the operator's UI language ("de"; empty
  from an older till — fall back to "en"). The ask runs in the import sandbox — only
  `FileImportHostFunctions` act, on a till and in the cloud alike — so test
  with `FakeHost.FileImport = true`.

Reference (every host function, permissions, limits): ut-docs
`reference/plugin-host-functions.md`.

- **Sales-channel adapters** (`sdk/plugin/channel`, ADR-0169): the guest SDK
  of a `channel` plugin, which runs in the ut-cloud channel host and never on
  a till. It imports only the `"channel"` host module (`channel_http`,
  `cursor_get/set`, `channel_submit`, `outbox_next/ack`, `log`), never the
  `"ut"` module above, and has its own `FakeHost`. Contract:
  ut-docs `reference/contracts/sales-channel-v1.md`.

**For host changes:** a new host function lands in the same PR as its
`//go:wasmimport` + `raw*` shim in `raw_wasip1.go`, the same shim in
`fakehost.go`, a typed wrapper, and its reference entry —
`scripts/ci/guard-sdk-hostfns.sh` fails otherwise. Releases are tagged
`sdk/plugin/vX.Y.Z` (they never match the app's `v*` release workflow).
