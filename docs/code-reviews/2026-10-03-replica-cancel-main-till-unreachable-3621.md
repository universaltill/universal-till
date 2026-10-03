# Review: refuse Cancel order on a replica while the main till is unreachable (ut-docs#3621)

Date: 2026-10-03 · Lane: `lane:cloud-54` · Built by Opus 5.5 · Reviewed by Fable (independent subagent)

## What shipped

- `cancelHeldSale` (`internal/pages/hold_api.go`) gets a new outcome,
  `cancelMainTillUnreachable`. When `heldSaleClaimForResume` returns a row it
  did not claim and that row is a `primary_synced` mirror, the primary gave no
  usable answer and still owns the order. The cancel is now refused before
  anything happens: the local row is kept, the table claim is untouched, and no
  audit row or `fiscal.order.cancel` goes out. Before, it deleted the local
  copy, wrote the audit row and dispatched the event, and the order came back
  on reconnect (so it could be cancelled twice).
- The sale-screen popup shows an error toast. `/open-orders` redirects with
  `err=open_orders.cancel.error.main_till_unreachable`. There is a new key in
  en/ar/fa/tr, and follow-up PRs go to ut-plugin-language-{de,es,pt}.
- Review finding 1 fix: `pos.HeldOrigin.PrimarySynced` carries the mirror
  flag through an offline resume, and `parkCurrentBasket` re-parks such an
  order (unclaimed) as a confirmed mirror. Without this, resume → re-park
  during the outage reset the row to `primary_synced=0` and the cancel went
  through anyway.
- An order first parked during the outage (`primary_synced=0`) still cancels
  locally, as before. Resume is unchanged: it must keep a sale moving offline
  (ADR-0003, ADR-0093 bounded-outage limit).
- Help: `web/help/{en,de,tr,ar,fa}/open-orders.md`, multi-till note extended;
  `make docs-shots` re-run (PNGs byte-identical, manifest hash updated).

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Resume of a synced mirror while the primary was unreachable, followed by a re-park, re-inserted the row at `primary_synced=0`, so the cancel still went through while the primary owned the order | **Fixed** (HeldOrigin.PrimarySynced). Regression test `TestCancelOnReplica_MainTillUnreachable_RefusesResumedAndReparkedMirror` fails without the fix and passes with it. Side effect checked: the flag's only other use is reconcile dropping a mirror the primary no longer lists, which is correct here because the primary does hold this id. There is no push loop for `primary_synced=0` rows, so no offline edit loses a push path it had before. |
| 2 | minor | `ok=false` covers more than transport failure: non-200, malformed, or claimed-without-row replies | **Accepted, documented** in the code comment. Refusing is the safe direction, and the next successful reconcile settles the mirror. |
| 3 | minor | tr/ar terminology drift ("birincil kasa" vs the page's "ana kasa"; ar help used a different noun from its own bullet) | **Fixed**: tr key and help now use "ana kasa"; ar help uses the bullet's "الكاشير الرئيسية" with gender agreement. |

The reviewer verified that the refusal returns before every side effect, that
the elevation-prompt path stays non-destructive, that kiosk, give-back and
outage-parked rows still cancel, and that the `?err=` key passes
`QueryErrKey`. It also checked that the first test fails with the fix
reverted (the toast says "Order cancelled").

## Verified

- TDD: both refusal tests were watched failing before their fix and passing after.
- `go build ./...`, `go test ./...`, `golangci-lint run ./...` (0 issues),
  `gofmt -l .` all clean. The `build` job guards pass locally, except
  `guard-shellcheck-version`, which fails only because this container has no
  shellcheck binary. Re-run after the review fixes: `go test ./internal/pages/
  ./internal/pos/`, guard-docs-shots/i18n/help-drift/data-access/kiosk-engine.
- e2e `tests/open-orders-cancel-3582.spec.ts`: 2/2 passing (the
  cancel happy path on both surfaces is unchanged).
- Visual: not looked at in a driven multi-till run (needs a replica with a
  dead primary). The refusal reuses the existing error toast and `?err=`
  banner, and the handler test checks the rendered popup HTML with the error
  toast and message.

## Verdict

Safe to merge. Merge core first; `lang-pack-drift` on main is already red
from ut-docs#3582's es/pt keys and is fixed by the pack PRs in the same cycle.
