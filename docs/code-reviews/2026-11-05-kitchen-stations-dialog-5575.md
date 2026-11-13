# Code review — Kitchen stations onto the list header + record dialog (ut-docs#5575)

**Date:** 2026-11-05 · **Lane:** lane:cloud-54 · **Author:** Sonnet Dev subagent · **Reviewer:** Opus 5.5 review subagent (different model), fresh worktree

## What shipped
- `web/ui/pages/kitchen_stations.html`: the per-row inline edit forms are gone. The page uses `list_header` (search + `+`), a `.list-card` table with tap-to-edit rows (Name, Destination + "View display", Address in `<bdi dir="ltr">`, Status in words) and the shared record dialog (`.show()` over `#ut-scrim`, never `showModal`). Find printers lives inside the dialog in create and edit mode; its results clear on every open, and "Use this address" fills the field without focusing it (no on-screen keyboard over the results), firing `input` for the discard guard and announcing `kitchenstations.discover.used`. Deactivate is a trash icon with `hx-confirm`; inactive stations get Activate.
- `internal/pages/kitchen_stations_page.go`: `redirectKitchenStations` / `renderKitchenStationsDialogError` (copied from promotions). Create, update (every branch, including the joined-till address-only exception), `/active` and `requirePrimary` answer htmx with `HX-Redirect` or a 400 message in the dialog; non-htmx callers (seed scripts, the routing forms marked `hx-boost="false"` by `base.html`) still get today's 303 / `?err=`.
- i18n: 5 new keys with metadata (`search_placeholder`, `edit`, `empty`, `deactivate_confirm`, `discover.used`); `kitchenstations.new.submit` and `kitchenstations.none` removed from `en.json` and the shrink-only baseline.
- CSS: only the #3653 `input[type="date"]` rule removed. The two wrap rules stay because `/tables` and `/users` still render inline row forms (epic ut-docs#2012); their comment now names those pages. This is the bounded reading of the card's "once neither page needs it" AC.
- Help: `web/help/en/kitchen-stations.md` rewritten for the dialog flow; `make docs-shots` regenerated en/ar/fa/tr PNGs + manifest. A dialog-open shot waits for ut-docs#5581.
- Tests: 4 new Go handler/render tests + updated display test (all failed first: 303 vs 200/400, missing list-header markup); new e2e `kitchen-stations-record-dialog-5575.spec.ts` (11 tests); updated 1556 (open the dialog first), 902 (`.card.list-card`) and 3297 comments.

## Findings
| # | Sev | Finding | Status |
|---|-----|---------|--------|
| 1 | Low | A printer scan (up to 25 s) still in flight when the dialog is closed and reopened for another station filled the new dialog with the old scan's results; "Use this address" could then fill a stale address. | **Fixed** — open-generation counter (`scanGen`) drops late answers. Surface hash refreshed (no pixel change). |
| 2 | Info | `requirePrimary` is now htmx-aware; only this page's routes call it and the routing forms are non-htmx, still 303 (test pins it). | OK |
| 3 | Info | CSRF: endpoints unchanged; same SameSite=Lax + `requireManager` protection as promotions. | OK |
| 4 | Info | XSS: `data-field-*`, `hx-confirm`, `var T` all contextually escaped; prefill via `.value`; discovery via `esc()`/textContent. | OK |
| 5 | Info | Deactivate/activate: prefill only touches the fields form, never the hidden `active` inputs. | OK |
| 6 | Info | Joined till: address-only edits still the only allowed change; behaviour identical. | OK |
| 7 | Low (pre-existing) | On a joined till Name/Destination stay editable and are refused on Save (same as the old inline form). | Deferred → Backlog card (filed at close-out) |
| 8 | Info | Removed keys remain in fa/tr/ar.json; Weblate prunes them (ADR-0160). | Accepted |

## Verification beyond unit tests
- Reviewer re-verified a TDD claim: restoring the old 303 in the joined-till refusal makes `TestKitchenStationsPage_ReplicaRefusalRendersInDialogMessageForHtmxRequest` fail (`code=303`); restored, passes.
- Tester (separate agent) mutation-checked three handler changes in a throwaway copy — each failed its target test.
- Tester driven run (Playwright, 4 seeded stations incl. a long German name, a display and an inactive one): 19 screenshots in en and fa at 1024×600, 1920×1080 and 360×740, list + dialog in create, edit-with-results and after "Use this address". Looked at: no sideways scroll anywhere; fa mirrors with the address kept LTR; dialog not modal, scrim shown; status/lock/exit visible and not inert at 1024 and 360; focus lands in Name on open and returns to the opener on Escape (with the discard prompt when edited); deactivate prompt text matches en.json; inactive row reads "inactive" and offers Activate; a 500 from printer search shows the error and re-enables the button.
- e2e: new 5575 spec + 1556 + 3297 → 99 passed; Dev's wider run (902, 3351, 2873, 2284, 3653, 3350) 151 passed.
- Gates: `gofmt -l .` empty; `go vet`, `go build ./...` OK; `go test ./internal/pages/...` OK; full `go test ./...` 108 packages OK, `internal/plugins` hit the 10-min timeout under CPU contention with the Playwright runs (untouched package — CI is the arbiter). Guards: i18n, i18n-meta, no-showmodal, rtl-logical-css, help-topics, data-access, fixed-dates, help-drift, help-breadcrumbs, docs-shots, compliance, competitor, mail — all pass.
- Not verified: real touch hardware / on-screen keyboard, a real LAN printer, dark theme, ar, de with the real pack.

Pre-flight: Till user-visible behaviour — `web/help/en/kitchen-stations.md` updated + `make docs-shots` (en/ar/fa/tr + manifest); `reference/list-and-dialog-pattern.md` n/a (already lists /kitchen-stations, checklist satisfied); README n/a (no claim changed); en.json keys — 5 new, all in `en.meta.json`, 2 removed from the baseline; review record — this file; follow-ups — joined-till read-only fields → Backlog card; /tables and /users ports already tracked on epic ut-docs#2012; dialog-open screenshot → ut-docs#5581.

**Verdict:** APPROVE-WITH-FIXES (fix #1 applied; gates green after it).
