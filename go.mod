module github.com/universaltill/universal-till

go 1.25.0

require (
	github.com/google/uuid v1.6.0
	// Pinned deliberately, not "whatever is latest" (ut-docs#1718). v1.29.10
	// — the version this line used to carry, with a "// or latest" comment
	// that had clearly outlived whoever wrote it — has a TOCTOU race in
	// interruptOnDone: it checks its "done" flag and calls sqlite3_interrupt
	// as two separate steps, so a cancelled query's watcher goroutine can
	// fire the interrupt after its own statement finished and the connection
	// went back to database/sql's pool. Whoever picks that connection up next
	// dies with SQLITE_INTERRUPT, and on a till that meant every query failing
	// until the Android app was force-stopped — the operator seeing only a
	// blank "auth unavailable" page. Upstream put both steps under one mutex
	// ("donemu prevents a TOCTOU logical race between checking the done flag
	// and calling interrupt"). internal/db/interrupt_race_test.go drives the
	// race directly, so a downgrade past the fix fails the build rather than
	// silently re-breaking tills.
	modernc.org/sqlite v1.58.0
)

require (
	github.com/anthropics/anthropic-sdk-go v1.57.0
	github.com/godbus/dbus/v5 v5.2.2
	github.com/hashicorp/mdns v1.0.5
	github.com/joho/godotenv v1.5.1
	github.com/pact-foundation/pact-go/v2 v2.4.2
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/tetratelabs/wazero v1.12.0
	github.com/webview/webview_go v0.0.0-20240831120633-6173450d4dd6
	github.com/xuri/excelize/v2 v2.11.0
	github.com/yuin/goldmark v1.8.5
	golang.org/x/image v0.44.0
	golang.org/x/net v0.57.0
	golang.org/x/sync v0.22.0
	golang.org/x/sys v0.47.0
	golang.org/x/text v0.40.0
)

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/hashicorp/go-version v1.7.0 // indirect
	github.com/hashicorp/logutils v1.0.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/miekg/dns v1.1.41 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/richardlehane/mscfb v1.0.7 // indirect
	github.com/richardlehane/msoleps v1.0.6 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/spf13/cobra v1.10.1 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/standard-webhooks/standard-webhooks/libraries v0.0.1 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/tiendc/go-deepcopy v1.7.2 // indirect
	github.com/xuri/efp v0.0.1 // indirect
	github.com/xuri/nfp v0.0.2-0.20250530014748-2ddeb826f9a9 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.2 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/mobile v0.0.0-20260709172247-6129f5bee9d5 // indirect
	golang.org/x/mod v0.38.0 // indirect
	golang.org/x/tools v0.48.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250324211829-b45e905df463 // indirect
	google.golang.org/grpc v1.73.0 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	modernc.org/libc v1.75.6 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

tool golang.org/x/mobile/cmd/gobind

// ADR-0028: patched fork targeting webkit2gtk-4.1 (upstream hardcodes the
// abandoned webkit2gtk-4.0, which Debian 13 trixie / current Raspberry Pi OS
// dropped entirely).
replace github.com/webview/webview_go => ./internal/thirdparty/webview_go
