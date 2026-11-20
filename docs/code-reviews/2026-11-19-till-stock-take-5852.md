# Till /inventory: stock-take for a location (ut-docs#5852)

Branch `feat/5852-till-stock-take`. Independent Fable 5.2 batch review (with #5770 #5776 #5831 #5908).

## What shipped
Count mode on `/inventory`: pick a location, count every line (variants included), review the differences,
apply all-or-nothing; works offline; main-till gate on satellites; help topic; on-screen-keyboard clearing of
the sticky bar (`osk.js`). Handlers `internal/pages/inventory_stocktake.go`, repo `stock_count_repo.go`.

## Findings
| # | Finding | Severity | Result |
|---|---|---|---|
| 3 | Every line is stamped with the count start time and movements since then are added to the target, so a shop trading while counting item X (3 sold after Start) stores 7-3=4 | major (design) | accepted, documented: the help topic states "count while the till is closed"; follow-up card to stamp each line when typed and to say so on the count screen |
| 4 | `parseCount` turned "1,234" into 1.234 and refused Persian/Arabic digits | minor | fixed: fa/ar digits normalised; a comma followed by exactly 3 digits is refused where the separator is "." ("1,5" still accepted); e2e covers "1,5" |
| 5 | Stock cell printed Go's raw float form (1e+06, no locale separator) | minor | fixed: the cell is formatted client-side with the locale separator (`fmtNum`) |
| 6 | Up to 500 synchronous `publishStockAdjusted` calls in the request | minor | accepted: bounded, same as the existing bulk adjust; follow-up card |
| - | Session auto-lock mid-count loses typed counts (kept in JS memory) | minor | accepted, follow-up card |

## Verification
Build/vet clean. `internal/data` full, `internal/pages` full (batch branch, 6 min) + narrow stock-take tests after the
fixes. Playwright `tests/inventory-stock-take-5852.spec.ts`: 6/6 pass (count/filter/review/apply, cancel, leave-with-counts,
no horizontal scroll and 44px targets at 1024x600 and 360x740, OSK clearance). docs-shots regenerated in the Linux
container; `inventory.png` in en/fa/ar/tr read (Stock-take button aligned in the header). NOT looked at: RTL count mode,
German/Persian longest strings in count mode, kiosk mode, dark theme.
guards data-access / i18n green. Pre-flight: help topic + shots updated; en.json + en.meta.json updated (English only); README/ADR n/a.

## Verdict
Approve. Deferred: finding 3, 6 and auto-lock as a Backlog card.
