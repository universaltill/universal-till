# Review: Next steps card + replacement main till receipt prefix (ut-docs#5058 UT-3, T4)

Date: 2026-11-03. Branch `feat/5058-ut3-next-steps`. Built by a Sonnet dev agent; reviewed by an
independent Opus agent (one round, fixes re-verified). Language packs: ut-plugin-language-{de,es,pt}
branch `i18n/5058-next-steps`.

## What shipped
- **UT-3 (design §3.3 T3):** after a cloud-path setup ("Start selling" with a held my. pairing), a
  manager sees a dismissible **Next steps** card on the sale screen. It lists staff PINs, the card
  terminal and printer, and a stock count. On a plan where the till masters the catalogue, a first
  line says products were not copied (D5; decided from the cached entitlement, or the pairing's own
  value before the first check-in, never from the "no cache = till" default).
  - Storage: one per-till setting `cloudsync.next_steps` (`pending:<mode>` or `done`); no migration.
  - Dismiss: `POST /ui/next-steps/dismiss` (settings permission, 403 otherwise).
  - Not a `.pos-notice` (those auto-fade), not a dialog, takes no focus. Capped at 35dvh.
- **T4:** a cloud setup that replaced a till sets `sync.receipt_prefix` to `M<8hex>-`, derived from
  the new device id and never equal to the old till's own derived value. Before this, the
  replacement numbered receipts and invoice series from `000000001` with prefix `""`, repeating the
  dead till's numbers.
  - The prefix survives restoring the old till's backup.
  - Customer-facing order/display numbers and counter orders keep no prefix.
- i18n: 6 `setup.next_steps.*` keys in en/ar/fa/tr + metadata; de/es/pt pack PRs. Help: two
  sentences in `multitill.md` (en/ar/de/fa/tr). `make docs-shots` on Linux: no PNG changed,
  manifest only.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| F1 | major | `keepLiveCloudIdentity` restored the backup's `sync.receipt_prefix` (`""`), so restoring the old till's backup — which the replace dialog and help tell the owner to do — undid T4. | Fixed: a live `^M[0-9a-f]{8}-$` prefix is kept (`internal/db/backup.go`). Test `TestApplyPendingRestoreKeepsReplacementMainReceiptPrefix`; the mirror matcher is pinned by `TestReplacementPrefixMatcherMirrorsData`. |
| F2 | minor | The 4-hex prefix was compared only with the directly replaced till (about 1/65536 collision along a chain). | Fixed: always 8 hex. |
| F3 | minor | Order display numbers and counter orders reused the prefix ("M3f2a-12" called out). | Fixed: `customerFacingPrefix` treats an M prefix as `""`. Test `display_no_replacement_prefix_test.go`. |
| F4 | minor | The devices line linked to the default-payment selector. | Fixed: links to `#settings-printer`. |
| F5 | minor | The no_catalog line was hidden below the 35dvh cap on a 360px phone (tester). | Fixed: it is now the first line. |
| F6 | nit | Tab order is ✕ before the links (tester). | Accepted: matches every other till notice (heading → ✕ → links). |
| F7 | nit | A failed prefix write is only logged. | Accepted: setup is first boot; logged at warn level. |
| F8 | nit | Redundant `role="region"`; raw rgba tint as on the pairing notice. | Accepted. |
| F9 | nit | A restore drops `cloudsync.next_steps`. | Accepted: the backup brings PINs and settings back. |

## Verification beyond unit tests
- Tester mutation run: 16 mutants, all killed. Reviewer re-verified 3 TDD claims; the orchestrator
  re-verified F1 (fix disabled → `sync.receipt_prefix = "", want "M3f2a9c1e-"`; restored → pass).
- Driven run, emulated (not real touch hardware), Chromium:
  - 1024×600 and 360×740 in en/de/ar: the card shows for a manager and not for a cashier; Pay stays
    on screen; it doesn't fade after 3.5 s; dismiss persists across reload and restart; ✕ is at
    inline-end in RTL; links are ≥44px tall; dark theme was checked.
  - Not looked at: kiosk mode, fa/tr.
- Gate: gofmt, vet, build, golangci-lint 0 issues, and 130/131 ci.yml build-job guards (the
  shellcheck-version guard needs the missing shellcheck binary; no script changed). `go test`
  passes for internal/db, internal/data and internal/pages after the fixes. The full `-race` suite
  timed out locally on an overloaded 4-CPU box, so CI is the full gate.

Pre-flight: till user-visible behaviour — help updated + `make docs-shots` (no PNG changed);
`reference/` n/a (design doc already specifies T3/T4); README n/a. New en.json keys — meta, 4 core
locales, 3 pack PRs updated. Any change — this record; follow-ups as cards (below).

## Deferred (cards)
- Next steps card after a re-pair from Settings → Tills (design non-goal).
- T4 for "Remove, then add with a plain code": `/pair` doesn't say the shop had a main till, so that
  till still numbers from `""`. Needs the cloud to send `store.had_main_till`.

Verdict: approve after F1–F5 fixes.
