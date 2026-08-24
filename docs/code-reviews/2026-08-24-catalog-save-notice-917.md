# Code review — ut-docs#917: catalog item-form "Saved" notice never visible on new-item save

**Date:** 2026-08-24
**Card:** [ut-docs#917](https://github.com/universaltill/ut-docs/issues/917)
**Complexity:** easy
**Build model:** Sonnet (inline) — **Review model:** Sonnet, independent fresh-context subagent

## What shipped

`web/ui/pages/catalog.html`'s item-form submit handler rendered its
"Saved" success notice and then, on the new-item path (`!editing`), called
`clearForm()` synchronously — and `clearForm()` unconditionally blanks
`#item-form-msg`, wiping the just-shown notice before the browser ever
painted it. A new item saved with no visible confirmation at all; editing
an existing item was unaffected (`clearForm()` is skipped when `editing`).

Fix: reorder the `.then()` callback so `clearForm()` runs before
`renderNotice(msg, 'success', …)` on the new-item path.

## What the independent review found

The reviewer (a fresh-context Sonnet subagent, no access to my reasoning
while building the fix) confirmed the reorder solves the happy path, but
found a real regression on the first draft: **CHANGES-NEEDED**, one
blocking finding.

`htmx.ajax()`'s returned promise resolves on **any completed HTTP
response** — the vendored htmx 1.9.12's `xhr.onload` always calls the
resolver; only a network-level failure (`onerror`/`onabort`/`ontimeout`)
rejects it. `/api/catalog/item` returns real, reachable 400s (duplicate
SKU via `skuAwareError`, invalid category/brand/tax via
`validateLookups`, a bad autofill barcode) on realistic operator mistakes.
The first draft's unconditional `.then()` body would therefore run on a
**failed** new-item save too: `clearForm()` would wipe the real error the
existing `htmx:responseError` listener had just rendered and discard
everything the operator typed, then paint a false "Saved" over an item
that was never created — data loss with a false confirmation, strictly
worse than the original silent-failure bug.

I verified the claim myself before accepting it: read the relevant
minified htmx source directly (`onload` calls `M(n,I)` — which fires
`htmx:responseError` for a >=400 status — then always `ie(o)`, the
promise resolver; only `onerror`/`onabort`/`ontimeout` call `ie(s)`, the
rejecter) and confirmed `internal/pages/catalog/handlers.go` genuinely
returns `http.StatusBadRequest` from `/api/catalog/item` on duplicate SKU,
invalid lookups, and bad autofill barcodes.

## Fix for the finding

Track failure via a one-shot `htmx:responseError` listener added
immediately before the `htmx.ajax()` call and checked inside `.then()`:
the listener fires synchronously during the same `onload` invocation,
before the promise settles, so the flag is guaranteed to be set by the
time `.then()`'s microtask runs. When set, `.then()` returns immediately
— skipping `clearForm()`, the success notice, and `filterRows()` — leaving
the real error (already rendered by the pre-existing top-level
`htmx:responseError` listener) on screen and the form exactly as the
operator left it.

## Verification

- **TDD, both directions, both regressions:**
  - New `e2e/tests/catalog-save-notice-917.spec.ts`, run against
    `origin/main`'s unmodified `catalog.html` (RED): the new-item-success
    test fails at the visibility assertion (bug reproduced) — the
    failed-save test also fails there, since the original code has no
    failure-gating at all.
  - Run against the fixed `catalog.html` (GREEN): all 3 tests pass —
    new-item success (notice visible and still visible ~1s later, well
    inside the 2.5s auto-expire window — not merely that the handler
    ran), edit success (unaffected path unchanged), and the **failed**
    new-item save (duplicate SKU): real error shown, no
    `.pos-notice.success` anywhere in the slot, form values (`name`,
    `sku`) untouched, and the duplicate item genuinely not in the table.
  - Driven live via Playwright + the pre-installed Chromium
    (`/opt/pw-browsers/chromium`, pinned build 1194 — older than the
    `@playwright/test` pin's expected 149.x; noted, not blocking, matches
    the existing `guard-webkit-version.sh`/`resolve-chromium.sh`
    accepted-drift pattern) against a real running till server, not
    mocked — no console errors beyond the deliberately-triggered 400's
    two expected lines (browser resource-load + htmx's own
    `htmx:responseError` log), both exempted with a comment explaining
    why each is expected noise.
- `gofmt -l .` clean, `go build ./...` clean, `go test ./...` clean.
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job run
  locally: all pass, including `guard-docs-shots.sh` after regenerating
  `web/help/img/manifest.json`'s surface hash (screen visuals unchanged —
  pure JS notice-timing/error-handling — so no screenshot re-capture
  needed, just the hash bump the guard requires for any `web/ui/**`
  touch).
- i18n: no new strings; the fix only reorders/wraps existing
  `renderNotice`/`clearForm` calls. `guard-i18n.sh` passes.
- Repository pattern / money / offline-first: not applicable — pure
  client-side JS, no SQL, no money handling, no network-dependency change.

## Files

- `web/ui/pages/catalog.html` — the fix.
- `e2e/tests/catalog-save-notice-917.spec.ts` — new e2e coverage (3
  tests).
- `web/help/img/manifest.json` — surface-hash bump only.
