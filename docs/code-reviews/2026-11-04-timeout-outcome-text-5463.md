# Review: timeout-specific outcome text on the sale screen (ut-docs#5463)

- Date: 2026-11-04
- Branch: `fix/5463-timeout-outcome-text`
- Card: universaltill/ut-docs#5463 (complexity:easy)
- Pipeline: BA + Architect + UX notes (one agent, Sonnet), Dev (Sonnet), Tester (Sonnet, then Opus 5 after a model fallback), independent Reviewer (Opus), orchestrator fixes for the nits.

## What changed

- `web/public/app.js`: `showAlert` gains a `'timeout'` kind. It is sticky and scrolls into view like `'outcome'`, and falls back to the outcome-unknown text if the attribute is missing. Only the ternary in the `htmx:timeout` handler changed: a prompt element or a non-GET request gives `'timeout'`, and a GET stays `'network'`. `htmx:sendError` is unchanged.
- `web/ui/partials/pos_alert.html`: `data-msg-outcome-timeout` holds the new key `common.error.outcome_timeout`.
- `web/locales/en.json` + `web/i18n-meta/en.meta.json`: the new key, English only (ADR-0160 §2, ut-docs#5478). This overrides the card's "every locale + pack PRs" ask: Weblate supplies the other locales, and `lang-pack-drift` is advisory.
- `internal/pages/split_tender_i18n_test.go`: Dev relaxed `TestLocalePlaceholderCountsMatchEnglish` so a key fa/ar/tr lack no longer fails it (placeholder counts still checked for keys present). On rebase, `main` had already made the same change (b44cb55, #2321), so this branch now takes `main`'s version and no longer touches the file; the review's verdict on it (legitimate ADR-0160 §2 alignment) still stands.
- `internal/httpx/pos_alert_test.go`: asserts the new attribute renders non-empty (red check: renaming the attribute fails it).
- Docs: `docs/sale-screen-notifications.md`, `web/help/en/sell.md` + manifest `sell` en hash and surface hash (hidden element, no pixel change → `Docs-Shots-Unchanged: true`).
- e2e: `action-timeout-5404` (A1, A3–A5 assert the new text; A2 asserts a GET keeps the network text; new A10 and A11), `prompt-timeout-5335` (T1 and T2 flipped; T6 also asserts not-timeout-text), `prompt-timeout-pin-5335` (T9 flipped).

## Review findings (Opus, independent) and outcomes

1. **Blocker: merge order with open PR #2340** (ut-docs#5458, another developer's branch). Its `e2e/tests/basket-resync-5458.spec.ts` expects the connection-dropped text after an htmx timeout on `/api/pos/scan`, and a trial merge conflicts in `app.js` (the `htmx:timeout` handler), `web/help/en/sell.md` and `manifest.json`. This is not a defect in this PR. Outcome: this PR merges first; #2340 must rebase, switch those assertions to the timeout text, combine both prose edits and recompute the sell.en and surface hashes. The PR body links #2340 so the note shows on that PR.
2. **Should-fix: the elevation fetch timeout** (`utPostWithElevation`) still shows the connection-dropped text. The card scoped only the htmx handler. Deferred → ut-docs#5484.
3. **Should-fix: pack follow-ups.** Not needed under ADR-0160 §2; recorded here and on the card.
4. **Should-fix: missing review record** → this file.
5. **Should-fix: docs-shots trailer** → the commit carries `Docs-Shots-Unchanged: true`.
6. Nits fixed: T6 also asserts the timeout text is absent; removed the unused `OUTCOME` from the pin spec; added the Go render assertion; the stale doc comment on the placeholder test is `main`'s now. Not changed: key ordering in en.json (it sits next to its sibling).

## Verification

- Red → green: before the app change, A1, A3, A4, A5, A11, T1 and both T2 variants failed. The Tester stashed `app.js` alone and saw A1, A11 and T1 fail on the real assertion, then restored it.
- e2e after the nit fixes: `prompt-timeout-5335` + `action-timeout-5404` 23 passed; `prompt-timeout-pin-5335` (auth project) 3 passed; the Tester's earlier run including `prompt-network-outcome-5298` had 27 passed.
- Driven run (Tester, real till via `e2e/run-till.sh`): the banner reads the new text. At 1024×600 it is one line with no overlap with ✕ and the status bar still visible. At 360px it wraps to 3 lines without clipping. Not checked: dark theme, RTL (the key is English only).
- Go: `go test ./internal/httpx/ ./web/locales/...` and the `internal/pages` locale tests pass. gofmt and vet are clean. The CI build-job guards pass apart from `guard-deadcode-baseline.sh`, which can't run here (the local deadcode binary was built with go1.26 and the module needs go1.27). This diff changes no non-test Go code, and CI runs that guard.

Pre-flight: Till user-visible behaviour: `web/help/en/sell.md` updated + manifest hash (no pixel change); other help languages via Weblate (ADR-0160); en.json key: added with en.meta.json metadata, English only, no pack PRs (ADR-0160 §2); `reference/` n/a (rule lives in `docs/sale-screen-notifications.md`, updated); README n/a; ADR n/a (client wording on the #5298/#5404 contracts); follow-up → ut-docs#5484; merge-order note → PR #2340.

Rebase: onto `origin/main` 07b685e; conflicts in the placeholder test (took `main`'s) and `web/help/img/manifest.json` (surface hash recomputed with `update-docs-shots-surface-hash.sh`, sell.en = sha256 of `sell.md`); guard-docs-shots, guard-help-topics, guard-i18n green after.
