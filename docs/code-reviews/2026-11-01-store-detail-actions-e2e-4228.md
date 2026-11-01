# 2026-11-01 — e2e: plugin store detail page actions (ut-docs#4228)

## What shipped
- New Playwright spec `e2e/tests/catalog-store-detail-actions-4228.spec.ts`, in the
  `diagnostics` project (`DIAGNOSTICS_ONLY_SPECS` in `e2e/playwright.config.ts`).
  An in-process fake ut-cloud on :8096 lists one verified and one unverified
  plugin. The spec drives the inline script of `web/ui/pages/plugins_store_detail.html`:
  - the cloud-terms box (unregistered till, ut-docs#4791) stops Download while unticked;
  - Download posts `listing_id` (+ `accept_terms=on`); a verified publisher gets no prompt;
  - the unverified-publisher `confirm()` (ADR-0006) names the vendor; Cancel sends
    nothing, OK sends exactly one POST;
  - an API error key (`plugins.install.error.not_entitled`) shows its English text
    and the buttons come back enabled;
  - a listing with a long description and a long `net:validation:<host>` permission
    has no sideways page scroll at 360 / 440 px and in Persian (RTL) at 360 px,
    and every permission host stays inside the card. This replaces the card's
    "point phone-layout-sweep-3297 at a real listing" item: that sweep's till
    has no catalogue.
- The download POST is intercepted in the browser (`page.route`), so the shared
  diagnostics till never registers from this spec.
- Test-only. No product code, no locale keys, no help change.

## Review
Built by a Sonnet subagent; independent review by an Opus subagent in a
detached worktree (ran the whole diagnostics project, re-verified a mutation).

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The terms-box assertions were conditional, and in the full project run the till was already registered by `cloud-catalog-bootstrap-4494` / `diagnostic-mode-indicator-2169` (files run in name order, `workers: 1`), so CI never covered the terms gate. | **Fixed**: renamed the spec `catalog-store-detail-actions-4228` so it sorts right after `catalog-language-download-4477` and before the registering specs; the terms box is now asserted unconditionally. Header + config comment say why. |
| 2 | nit | `listInside` measured the `<ul>`, which can't overflow its container. | **Fixed**: measures each `.perm-desc-item code`. |
| 3 | nit | The `?lang=en` restore line was unnecessary (fresh context per test). | **Fixed**: removed. |
| 4 | nit | `waitForTimeout(500)` for the "nothing sent" checks. | Accepted: the decision is synchronous in the click handler; a negative check needs a window. |
| 5 | nit | `Failed to load resource:.*403` console exemption is page-wide. | Accepted: only two pages, low risk. |
| 6 | info | Catalogue cache: no earlier diagnostics spec opens `/plugins*`, so the fetch is cold; later specs don't read it. | Noted in the header. |

## Verified
- `npx playwright test --project=diagnostics`: 14 passed (after the fixes, in CI file order).
- Mutations, each failing the named test and then restored (`git diff web/ui` empty):
  - `if (!window.confirm(q)) return;` → `window.confirm(q);`: Cancel test fails (1 POST received).
  - `listing_id: listing` → `"x" + listing`: verified test fails on `listing_id`.
  - client-side terms check disabled: verified test fails in the **full project run**
    (`Expected substring: "Accept the Universal Till cloud service terms"`).
  - `.perm-desc code { white-space:nowrap }` (reviewer): 360 px test fails (scrollWidth 1195).
- Screenshots looked at (360 en, 440 en, 360 fa): description, permission badge and
  host line wrap inside the card; Persian mirrors correctly. Light theme only;
  dark theme not looked at (no styling changed).

## Verdict
Safe to merge.
