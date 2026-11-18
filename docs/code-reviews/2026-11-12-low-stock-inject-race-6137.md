# Review: low-stock injection race in inventory-stock-dialog-2011 (ut-docs#6137)

**Change.** This is a test-only fix in one file, `e2e/tests/inventory-stock-dialog-2011.spec.ts` (+13/-2). Cases (f) and (g) put a row into `#low-stock-list` straight after `page.goto('/inventory')`. The page's own `hx-get="/api/inventory/low-stock" hx-trigger="load"` (`web/ui/pages/inventory.html:79`) could land after that and replace the container's innerHTML, wiping the injected row.
- New helper `gotoInventorySettled`. It starts `page.waitForResponse(/api/inventory/low-stock)` before navigating and waits for it after. It then waits until `#low-stock-list` has none of `htmx-request`, `htmx-swapping` or `htmx-settling`.
- (f) and (g) use the helper. Case (e) is untouched because open PR universal-till#2515 is editing it. No product code changed.

**Pipeline.** Dev was Sonnet. Tester and Reviewer was one separate agent running Opus, a different model from Dev.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| I1 | info | **Can the wait pass before the swap?** Checked against the vendored htmx 1.9.12. The default swap delay is 0 and `base.html` sets `htmx-config` `defaultSettleDelay: 0`. In `xhr.onload`, htmx calls the response handler first and `removeRequestIndicators` after it. With swap delay 0, the swap (add `htmx-swapping`, replace innerHTML, remove `htmx-swapping`, add `htmx-settling`) runs synchronously in that call. So `htmx-request`, which htmx puts on the element itself because no `hx-indicator` is set, is removed only after the innerHTML is already replaced. `htmx-settling` then clears on a 0 ms timer. Playwright's `response` event can fire before `onload`, but the element still has `htmx-request` then, so the class poll waits. A future `swap:` delay would show up as `htmx-swapping`, which the poll also covers. | No change. The signal is sound. |
| I2 | info | **Is the response wait redundant?** No. Before the `load` trigger fires, the element has no htmx class at all, so the class poll alone could pass early. The two waits are needed together. An `htmx:afterSettle` promise would be about as robust but needs an `addInitScript` listener. The text "Loading…" disappearing would be locale-dependent and would also clear on an error response. | Accepted as written. |
| I3 | info | **Second load from `stock-updated from:body`?** That event only comes from the `HX-Trigger` header on `/api/inventory/receipt` (`inventory_api.go:167`). (f) only opens the dialog and (g) only types into the search box; neither posts. No page script dispatches it on load. | Not reachable in (f)/(g). |

## Verification
- **Green (fix):** `npx playwright test tests/inventory-stock-dialog-2011.spec.ts --project=default --grep "\(f\)|\(g\)" --repeat-each=30 --trace off --workers=2` gave **60/60 passed**. That is 30/30 for each case, which meets the AC.
- **Red (spec reverted to `origin/main`, same command):** **3 failed, 57 passed**. All 3 failures were (g), repeats 1, 5 and 18, with `toBeVisible` reporting "element(s) not found", which is the injected row being wiped. Dev's own run without the fix also got 3/60. The race does not reproduce on every run, but it did on both.
- The whole spec file passes 7/7, with the fix restored.
- Guards: `guard-fixed-dates`, `guard-e2e-fixtures-import`, `guard-e2e-no-browser`, `guard-e2e-no-lan-discovery`, `guard-e2e-no-update-check`, `guard-mail-addresses` and `guard-compliance-claims` all exit 0. No Go, `.sh`, locale or template change, so the Go gate, shellcheck and the i18n guards are n/a.
- The review worktree has an untracked `e2e/node_modules` symlink to the main checkout's install. It is not part of the change and must not be committed.

Pre-flight: web/help n/a (test-only, no user-visible behaviour); docs-shots n/a (no `web/` change); reference/README n/a; en.json/meta n/a; same race in case (e) n/a because (e) never touches `#low-stock-list`; review record → this file.

**Verdict:** approve as is.

**Deferred:** none.
