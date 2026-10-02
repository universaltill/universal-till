# Code review: iOS Bluetooth devices page tells the truth (ut-docs#3261)

**Date:** 2026-10-02 · **Lane:** lane:cloud-24 · **Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent)

## What shipped

The owner reported that on iOS "Bluetooth find device is not working". The cause: `newDBusClientFor` special-cased only `android`, so on `ios` it dialled D-Bus, failed, and the page said *"no Bluetooth adapter was found, or the service is not running"*, with Scan disabled.

The card asked for a CoreBluetooth bridge. Research turned that down. This page pairs HID input devices, and iOS keeps HID for the system (Core Bluetooth never exposes service 0x1812 to an app). Bluetooth printing has no transport on any platform. iOS exposes only peripheral UUIDs, never MACs. Findings and sources are on the card.

So instead:
- `internal/bluetooth/dbus.go`: `ios` returns `ErrUnsupportedPlatform` before touching D-Bus, even with an Android bridge registered.
- `internal/pages/bluetooth_devices_page.go`: injectable `bluetoothPlatform` (= `runtime.GOOS`) is passed to the template as `ios`.
- `web/ui/pages/bluetooth_devices.html`: on iOS, a `.notice-block-warn` notice. It says to pair scanners/keyboards in **Settings → Bluetooth**, and that Bluetooth printers and scales can't be used (use a network printer). The intro, the paired list, the Scan card and the page script are not rendered.
- 2 new keys in en/ar/fa/tr, plus follow-up PRs in `ut-plugin-language-{de,es}`.
- Help topic `bluetooth-devices`: iPhone/iPad bullet in en/de/fa/ar/tr. The ar help-drift baseline entry is shifted by the same +1 (the pre-existing gap is unchanged).
- `ios/README.md` updated. ut-docs: ADR-0080 amendment and a hardware-matrix note.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Intro/help promise "scanner or scale"; the iOS notice was silent on scales, which aren't HID and can't be used from iOS | **Fixed**: "printers and scales" in all locales, help and packs |
| 2 | nit | ar used "نقطة البيع" for the till; the rest of ar uses "الصندوق" | **Fixed** |
| 3 | nit | Test asserted the bare substring "network" | **Fixed**: asserts "Wi-Fi or Ethernet" and "scales" |
| 4 | nit | de "tippt er" agrees only with Scanner | **Fixed**: "tippt das Gerät" |
| 5 | process | New en keys make `lang-pack-drift` red on main until the de/es pack PRs land | Pack PRs prepared, merged right after core in this cycle |

The reviewer also saw `guard-deadcode-baseline` red on `internal/logging/file.go` locally. That file is untouched by this diff; checked against main CI.

## Verified beyond unit tests

- **TDD** (author, then re-verified by the reviewer with the fix reverted): `TestNewDBusClientFor_IOSIsUnsupportedPlatform` failed with the real `dial unix /var/run/dbus/system_bus_socket` misreport. The iOS page test failed on all assertions (no notice; Scan, paired list and intro rendered).
- **Visual:** the real handler's iOS output was served through a running till in Chromium.
  - Viewports and locales: 360×800 en and fa (RTL); 1024×600 en and tr.
  - No horizontal overflow, no Scan button.
  - The first draft used the `.tag` pill and rendered as a clipped oval. Changed to `.notice-block-warn` and looked at again.
  - Light theme only. Dark uses the same tokens; not looked at.
- **Not verifiable here:** a real iPhone/iPad. The Swift shell is unchanged, so `ios-ci` only re-proves the build.
- **Gates:** `go build ./...`, `go vet`, full `go test ./...`, gofmt, golangci-lint (reviewer), guards i18n / help-drift / help-topics / docs-shots / compliance / competitor / core-neutral / data-access / kiosk-engine / no-showmodal / page-http-error / readme-links / osk / htmx / autofill.
- **docs-shots:** the full `make docs-shots` run reproduced byte-identical PNGs. After the wording-only help fixes, the manifest was rewritten with `e2e/tests-docs/write-manifest.js`; template and surface were unchanged.

## Verdict

Safe to merge. Deferred: iOS BLE receipt printing, which needs a print transport and an ADR, is filed as a Backlog card.
