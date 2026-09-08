# Android Bluetooth backend — Kotlin implementation, permissions, and the states BlueZ never had (ut-docs#1751)

- **Date**: 2026-09-08
- **Branch / PR**: `fix/1751-android-bluetooth`
- **Card**: ut-docs#1751 (`complexity:hard`, `source:user` — reported by the product owner)
- **Supersedes**: ut-docs#1731 (same scope, minus the turn-it-on ask)
- **Related**: ADR-0080 (+ the amendment this change adds), ADR-0078, ut-docs#1721 (the seam), ut-docs#1643

## What shipped

ut-docs#1721 closed `status:done` having shipped **only the Go-side seam** — an `AndroidBridge` interface nothing implemented, and a `SetAndroidBridge` that CI had to baseline as a deadcode false positive. No Kotlin existed, and the app declared no Bluetooth permissions at all, so the page listed nothing on a tablet whose Bluetooth was demonstrably switched on. This is the other half:

- `android/.../BluetoothBridgeImpl.kt` — bonded list, simultaneous classic + BLE discovery merged by address, bond, unbond. Application-Context only, so `TillService` can construct it; registered before `Mobile.start(...)`.
- Manifest: both permission regimes (`minSdk = 24`, `targetSdk = 36`) plus `<uses-feature required="false">`.
- Three new sentinels/tokens — `ErrAdapterOff`/`ADAPTER_OFF`, `ErrPermissionRequired`/`PERMISSION_REQUIRED`, `ErrForgetUnsupported`/`FORGET_UNSUPPORTED` — through `classifyBridgeErr`, `apiFail`, and the page's state switch.
- Two new page notices that carry the action that clears them, rendered `hidden` and revealed only when the native bridge answers; four no-argument `@JavascriptInterface` methods on the existing `KioskBridge`.
- i18n in all four locales, help topic rewritten in all four, README, ADR-0080 amendment.

**Why three new sentinels rather than reusing `ErrUnavailable`:** the card is a report from someone who had *already* switched Bluetooth on and still saw nothing. `ErrUnavailable`'s notice says this till has no Bluetooth — it sends the operator hunting a hardware fault that does not exist. A sentinel here is defined by *what the person at the till does next*, not by which layer failed.

## Independent review

Different model, fresh context, isolated worktree, briefed to attack nine specific areas and to run the gates itself. Verdict on the first pass: **NOT SAFE TO MERGE**, two blockers. Both were real, and both were then **reproduced and re-verified on the physical tablet** rather than argued about.

### BLOCKER 1 — the enable button could crash the till (fixed)

`ACTION_REQUEST_ENABLE` is itself permission-protected on API 31+: launching it without `BLUETOOTH_CONNECT` throws `SecurityException`, on the main thread, killing the app. And the bridge's ordering guaranteed the button appeared in exactly that state — `adapter()` threw `ADAPTER_OFF` *before* the permission was ever checked, which is precisely where a fresh install lands (radio off, nothing granted).

Fixed by inverting the order: `PERMISSION_REQUIRED` now outranks `ADAPTER_OFF`, so the page offers the permission — which works — instead of an enable button that cannot be honoured. Belt and braces: `requestBluetoothEnable()` re-checks the grant, and catches `SecurityException` as well as `ActivityNotFoundException`.

**Verified on device.** Permissions revoked + Bluetooth off → the page shows "This till has not been allowed to use Bluetooth yet" with **Allow Bluetooth access** (not the enable button) → granting reloads to "Bluetooth is switched off on this till" with **Turn on Bluetooth** → that produces the system prompt → radio on. Zero `FATAL EXCEPTION` in logcat across the whole sequence.

### BLOCKER 2 — classic discovery never fired (fixed, and proved both ways)

Both `BroadcastReceiver`s were registered `RECEIVER_NOT_EXPORTED`. `ACTION_FOUND` and `ACTION_BOND_STATE_CHANGED` are sent by the Bluetooth stack process, **not** system_server, so a non-exported dynamic receiver never receives them. Consequences: no classic/BR-EDR device could ever appear (many HID barcode scanners are classic), and `pair()` could never observe `BOND_BONDED`, so every successful pairing would report "refused" after its timeout.

This is the second time this repo has learned this exact lesson — `MainActivity.kt` already carries the note for DownloadManager: *"a RECEIVER_NOT_EXPORTED registration silently never fired … verified the hard way on a real tablet."*

**This one nearly shipped because the on-device run appeared to pass.** The first device run listed several devices, and that was taken as "classic and BLE work". It wasn't: every result had arrived through the BLE `ScanCallback`, which is a direct callback and never touches a broadcast. A list with devices in it looked exactly like a working feature.

The decisive experiment: make a Mac classic-discoverable and scan for it by address.

| | scan result |
|---|---|
| `RECEIVER_NOT_EXPORTED` (before) | `5C:E9:1E:70:CD:F0` **absent**; only BLE addresses listed |
| `RECEIVER_EXPORTED` (after) | **`MacBook Pro (6)  5C:E9:1E:70:CD:F0`** listed |

`RECEIVER_EXPORTED` is safe here in a way it would not be for an app-defined action: both are *protected broadcasts*, which only the platform may send, so no other app can forge one in. `pair()` additionally now reads `device.bondState` as ground truth rather than trusting the broadcast alone.

### Also fixed from the review

| # | Finding | Fix |
|---|---|---|
| 3 | Under screen pinning Android blocks starting another package's activity **and throws nothing**, so "Open Bluetooth settings" was a silent no-op — the exact dead end this card exists to remove | `bluetoothActionBlockedByLockdown()` gates it and shows a translated Toast (new string in all four Android locales). Permission dialogs are *not* blocked by lock task, so that path is deliberately not gated |
| 4 | The manifest comment reasoned backwards: omitting `<uses-feature>` makes the store derive `required="true"`, the opposite of the intent | Declared `android.hardware.bluetooth` and `bluetooth_le` as `required="false"`, comment corrected. The same pre-existing bug on `CAMERA` is filed as ut-docs#1770, not fixed here |
| 5 | `scanning` was set 24 lines before the `try` that resets it; any throw between them would wedge every later scan until app restart, reported as "no Bluetooth adapter" | `try` moved directly after the `compareAndSet`; the collision now reports its own token, which deliberately falls through to the page's generic "try again" — correct advice, no new locale key |
| 6 | BLE `onScanFailed` unhandled (Android throttles to ~5 scan starts / 30s — an operator hunting a scanner hits this) and `startDiscovery()`'s false return ignored | Both logged; "we could not look" no longer renders as "we looked and found nothing" |
| 7 | `data/.unitill.lock` and `test-results/.last-run.json` committed | Removed and gitignored |
| 8 | `removeBond()` only *accepts* the request — the page would say "Forgotten." one line above the still-paired device; README/help/ADR asserted a certainty the code did not have | `forget()` waits for `BOND_NONE` and reports `FORGET_UNSUPPORTED` if the bond survives; prose in README + four help locales + the ADR now says it may or may not work, and never claims a device is forgotten while it is still paired |
| NIT | Nothing pinned the `": "` separator the Kotlin error contract depends on — changing the match to a bare token prefix passed every existing test | Added `TestClassifyBridgeErr_RequiresTheColonSeparator`; mutation-checked (fails against the mutation, passes restored) |

**Accepted, not fixed:** `connectedAddresses()` consults only the GATT profile, so a live classic HID device reads "not connected" — the classic profiles answer through async proxies, which would turn a synchronous bridge call into a multi-second dance for a column the operator does not act on. Documented in code. The stale-APK case the review raised is unreachable on Android: the Go server ships *inside* the APK, so server and Kotlin always update together.

## Verified beyond the automated tests

On the real TECLAST P50T (Android 16, API 36, not device-owner — `Device Owner Type: -1`), driven over adb, build installed **over** the existing app so the German catalog survived:

- Permission-missing state → prompt → granted → page recovers, Scan enabled.
- Scan lists real devices; **classic confirmed by the named Mac appearing only after the receiver fix**.
- Bluetooth off → notice → **Turn on Bluetooth** → *"Universal Till wants to turn on Bluetooth"* → radio on, page recovers.
- No `FATAL EXCEPTION` anywhere in logcat across all of it.
- Gates: `gofmt`, `go build`, `go vet`, `go test ./internal/bluetooth ./internal/pages`, `guard-i18n`, `guard-android-i18n`, `guard-help-topics`, `guard-data-access`, `guard-docs-shots`, `guard-compliance-claims`, `guard-gobind-skip`, plus `./gradlew assembleDebug` and `assembleRelease`.

**Not verified, stated rather than implied:** pairing an actual device (the only units in range belong to neighbours); the `FORGET_UNSUPPORTED` path, which needs a paired device; and the two new notices in RTL or at the 1024×600 kiosk floor — the diff introduces no physical CSS properties, but that is a grep, not a look.

Unrelated and pre-existing: `internal/alerts TestStart_RunsDigestLoopBody` fails depending on the wall-clock hour (reproduced on clean `origin/main`, invisible to CI because CI runs in UTC) — filed as ut-docs#1769.

## Verdict

**Safe to merge** after the two blockers and six other findings above, each re-verified on the physical device where the device could show it.

The lesson worth carrying: the first on-device run *looked* like a pass. Half the feature was dead and the screen gave no sign of it. What separated the two runs was not looking harder at the screenshot — it was picking a device whose absence would be meaningful, and scanning for it by address.
