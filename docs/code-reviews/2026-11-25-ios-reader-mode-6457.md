# Review: iOS reader mode (ut-docs#6457, part of ut-docs#6538)

Branch `feat/6457-ios-reader-mode`. `ios/UniversalTill/Reader/` (Core is pure Swift, host-less tests), `DelegatingTokenProvider`,
`TapToPayCoordinator.collectIntent` (reader origin), wiring, flag `UT_READER_MODE` (default NO, Release pinned NO) and its CI guard.

Pre-flight: Platform/feature claim: README `ios/README.md` section updated, public claims n/a (flag off in every shipped
build); en.lproj strings English only (ADR-0160); help topic n/a (Debug dev-signed build only, no shop-facing surface yet);
review record: this file.

Reviewers: Fable (independent, read-only) + combined Tester. Author model: Sonnet.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| I1 | high (functional, fails closed) | `ReaderNet.run` used `dataTask(with:completionHandler:)` and discarded `data`; with a completion handler URLSession never calls the delegate's `didReceive data`, so the body was always empty and pairing + pin re-proof always failed on a real socket (unit tests used a fake `PairHttp`) | fixed: body taken from the completion handler (capped at 8192). Not yet driven against a local Go main over TLS: owner/device check |
| I2 | medium (money wording) | `.pending` outcome showed "Time is up" (the card may be charged) | fixed: new `ReaderResultKind.pending` shows `reader_result_pending` ("Ask the cashier"); test updated |
| I3 | low | a collect refused before the coordinator lock (provider, bad request, unsupported) sent a result with no ack; the main would time out to pending | fixed: reported as `started(false)` (error `busy`, nothing started); test `testARefusalBeforeTheLockIsNotStarted...` |
| I4 | low | idle timer re-enabled during `.pay` (auto-lock drops the link mid-sale) | fixed: screen stays awake while a payment is requested |
| I5 | nit | hard-coded `Button("OK")` | fixed: `reader_sheet_done` string |
| I6 | medium | setup / foreground prewarm take the connection token from the cloud, so a reader-only phone with no till credential never reaches ready (fine for the owner's setup: iPhone is a till of the same store and the reader) | deferred, Backlog card; departure from D3 recorded |
| I7 | low-med | `pendingLocationOverride` can linger when already connected; a joined in-flight prewarm ignores the frame's location | deferred, card (harmless if locations equal) |
| I8 | low | `ReaderLink.stop()` leaves `isLinked` stale; link/Terminal stay open after a collect that outlives backgrounding; pairing cancel races; `SecItemAdd` result ignored; `mainName` not persisted; unused `enabled`/`ended`; guard regex accepts cross-assignment (Release pins hold) | deferred, one card |

Deviations in dev-ios.md: 1-4 accepted (Fable agrees); 5 (real-socket) was not sufficient alone, which is how I1 was found.
Checked and clean: bearer only to a pinned peer (decided in the trust challenge, empty or non-P-256 fails closed); HMAC
re-proof; commitment, code, 10 min expiry, plain-http refused before any request; Keychain `WhenUnlockedThisDeviceOnly`; flag
default off; no card data and no log call in `Reader/`; one `current` lock for both origins, second sale gets `busy`, never
queued or mixed (S5); amount mismatch and link loss are pending; a same-key retry never taps twice; no "declined" wording.

## Verification (re-run by the Reviewer)
`xcodegen` + `xcodebuild test -scheme UniversalTillTapToPayTests` (iPhone 17 simulator): 170 tests, 0 failures. Guard tests pass.
Full-app flag-on build and UI at SE / long locale / RTL not re-run (owner device check).

## Verdict
Approve for a flag-off merge. Do not enable `UT_READER_MODE` until I1 is proven on a device against the Mac main.
