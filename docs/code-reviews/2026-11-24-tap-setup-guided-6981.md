# Review: Tap to Pay settings — guided order and specific errors (ut-docs#6981)

Pre-flight: till user-visible behaviour: `web/help/en/tap-to-pay.md` updated (step order, new messages) and `make docs-shots` re-run (en/ar/fa/tr `tap-to-pay.png` + `manifest.json`; the shot shows the container's "unavailable" state, which the later CSS fix does not touch); `reference/` n/a (no reference page describes this screen's order; ADR-0125/0198/0203 fix neither the step order nor the bridge result shape — Architect checked); `README.md` n/a (still accurate); new `en.json` keys: 15, each with `en.meta.json` metadata, English only; my./admin. UI n/a; new host function / manifest field n/a (`devicepay.SetupResult.detail` is a native-bridge field, golden `internal/devicepay/testdata/setup_golden.json`); review record: this file; follow-ups: Backlog card (L1–L3, Swift connect-path detail, Android `detail`).

## What shipped
- `internal/pages/tap_settings_page.go`: steps ordered This iPhone → Allow card taps → Apple's terms and location → Stripe account. The first unfinished step's button sits in the status block (rendered once, so ids stay unique); Set up is disabled with the visible reason "Allow card taps first." until the plugin holds `device:tap_to_pay` + `net:payment_broker`; Allow has its own button and reason (plugin missing / not a manager).
- One code → i18n key table (`tapReasonKey`, `tapNativeReason`): the iPhone's `terms_not_accepted` / `location_denied` now hit the terms/location lines (on `main` they fell through to "Something went wrong" — the old test faked `terms`, which the iPhone never sends); `not_enrolled`, `insecure_endpoint`, `device_role_forbidden`, `entitlement_missing`, `busy`, `canceled_by_user`, broker 5xx/`auth_unavailable`, `not_enabled`, `account_not_ready`, `not_connected` each get their own line. The Stripe account check keeps the broker's code instead of collapsing it to "Can't reach the payment service". The raw code is always under "Details for support" (`<details>`, `dir=ltr`, 44px summary).
- `internal/devicepay`: optional, shape-checked `detail` on `SetupResult` (`[A-Za-z0-9_. -]{0,64}`, else dropped); reason must be `[a-z0-9_]{0,64}` (else `bad_native_result`); golden file.
- iOS Swift: the bridge passes the Apple/Stripe/cloud code through as `detail`, adds `canceled_by_user`, and `fromCloud` keeps more broker codes.
- `web/public/app.css`, `web/ui/partials/tap_settings_body.html`, help topic, 15 English keys.

## Review (independent Opus 5.5 reviewer; code by a Sonnet developer)
- **M1 (Medium) — fixed.** `tapReasonKey("other", <detail>)` showed "Apple or Stripe reported a problem" for any detail, so a cloud `http_503`/`failed`/`unauthorized` during Set up blamed Apple and disagreed with the account line. Now a detail with a space (a native code such as `stripe 2900`) is Apple's/Stripe's failure; any other detail maps through the same table. Test rows added to `TestTapReasonKey`; seen failing (3 rows) before the fix, passing after.
- **L1 (Low) — accepted, follow-up.** The wider Swift `fromCloud` also changes cashier decline `Reason` values (e.g. `unauthorized` instead of `other`). No consumer in this repo branches on `Reason` (they key on `DeclineCode`; Android already sends raw codes). The Stripe plugin's reason→message mapping could not be checked from this workspace; confirm it has a default branch. Wording only, nothing charged.
- **L2 (Low) — accepted, follow-up.** The action button is inside the `role=status aria-live` block, so screen readers re-read it on each swap.
- **L3 (Low) — accepted, follow-up (also on `main`).** Focus falls to `<body>` after Allow card taps swaps the panel.
- Swift connect path (`TapToPayCoordinator.startConnect`) still passes the mapped reason, not the raw code, into `.other(…)`; rare (location resolve fails first). Left for the follow-up: Swift can't be compiled here.
- Checked fine: native input bounded and html/template-escaped (no XSS); no showModal/native confirm; `hx-confirm` names its verb; logical CSS only; no money, migrations, file writes or cwd-relative paths.

## Tester (independent)
- 7 mutations, each failed its matching new test (order, Allow gate, native-reason mapping, broker code kept, detail/reason shape filters, `<details>`).
- Driven run, Chromium 360×780 touch and 1024×600, en and fa (RTL), fake bridge + fake broker: plugin missing, nothing done, Allow (UT.confirm dialog, verb on OK), allow done, Setup failed (each reason + code), account row for 401/403/404/409/5xx/400, ready. No horizontal scroll, one `#tap-grant` / `#tap-setup`, no JS errors.
- **D1 — fixed:** at 360px the disabled status-block button shrank beside its reason (`.tap-actions` did not wrap). Now `flex-wrap: wrap` and the reason takes the full row.
- Not checked: real iPhone / WebKit (not installed), de (no ut-translations checkout), non-manager state (unit test only), Swift compilation (ios-ci is the check).

## Gate
`gofmt`, `go build ./...`, `go vet` (pages, devicepay), `go test` devicepay, mobile, httpx, paybroker, pages (`-timeout 40m`, no -race: it times out on this container), guards i18n, i18n-meta, rtl-logical-css, help-topics, no-showmodal, no-native-confirm, fixed-dates, compliance-claims — see the PR for CI.

## Verdict
Approve after M1 and D1 (both fixed in this branch). Owner: check the screen once on the iPhone build.
