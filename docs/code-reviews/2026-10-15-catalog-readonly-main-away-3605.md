# Review: catalogue read-only up front while the main till is unreachable (ut-docs#3605)

- **Branch:** `fix/3605-catalog-readonly-main-away`
- **Lane:** `lane:cloud-24`
- **Built by:** Opus 5.5 (Dev + Tester subagents)
- **Reviewed by:** Fable (independent, detached worktree)

## What shipped

On an additional till whose main till counts as unreachable, every control
that posts to a catalogue write-through route (`catalogsync.Routes`) is
disabled before the operator types. Before this change the save was only
refused afterwards (unchanged, still the backstop).

- **Convention.** A `data-main-till-lock` fieldset (`display: contents`,
  sharing `.set-lock`'s frame from #2981) or a lone control carrying the
  attribute. It is rendered `disabled` server-side from
  `d.MainTillUnreachable`.
- **Live follow.** `base.html`'s `followChip()` toggles `.disabled` on every
  `[data-main-till-lock]` when the status chip's `data-link-state` changes
  to or from `unreachable`. It also runs on `htmx:load` for swapped-in
  partials. It acts on a state change only, never on a plain 5 s poll, and
  never sets a button's own `disabled`, so a Save that a script disabled
  mid-request is left alone.
- **Banners.** The `/catalog` and `/inventory` banners render both texts and
  flip between them live. Before, they were only correct at page load.
- **Surfaces covered:**
  - the catalog item dialog: Save, Delete, details fields, keypad capture;
  - the variants panel: barcodes, cost, lead time, reorder level, variant
    rows, option sets, station routes;
  - modifier attach/detach;
  - Categories, via new optional `mainTillLock` params on `record_dialog`;
  - the Designer's category list in `/ui/buttons?mode=edit`. That path is
    edit mode only, so the sale-screen render and its cache are untouched
    (`buttons_api.go` short-circuits on `editMode`).
- **Kept usable:** Close, the dialog tabs, search and filters, label
  printing, export, item and variant photos (image files only, never
  `requirePrimary`).
- **`record-dialog.js`.** `serialize()` briefly re-enables locked fieldsets
  so FormData sees their fields. Without this, a lock flip read as an unsaved
  edit and Close asked to discard (found by Tester, fixed TDD-style).
- **Disabled text inputs** are dimmed (`opacity: .55`, like
  `.btn:disabled`). Before, they looked editable (Tester).
- **Help.** `web/help/{en,de,tr,fa,ar}/multitill.md` step 7 gets one
  sentence. No new locale keys: the notes reuse
  `sync.banner_replica_main_till_unreachable`.

## Tests

- **`internal/pages/main_till_lock_3605_test.go`.**
  - Categories page, Designer edit-mode fragment and the inventory banner,
    each locked, unlocked and standalone.
  - A template guard that tokenises 14 templates and fails if any `hx-post`
    or `<form action>` to a `catalogsync.Routes` path sits outside a lock.
    It pins 13 routes, so it cannot pass vacuously.
- **`internal/pages/catalog/main_till_lock_3605_test.go`.**
  - `/catalog`, the variants panel and the modifiers panel are locked while
    unreachable. The usable controls, including the variant photo, stay
    unlocked.
  - Nothing is disabled when the main till is reachable or the till is
    standalone.
  - The banner carries both states.
- **`e2e/tests/categories-record-dialog-main-till-lock-3605.spec.ts`.** A lock
  flip, or the lock lifting, causes no discard prompt. A real edit still
  prompts.
- **TDD re-verified by the reviewer.** With the non-test changes reverted,
  every Go `MainTillLock` test fails. The guard reported 19 unlocked
  controls. Restored, they all pass. I watched the variant-photo assertion
  fail on the pre-fix template.

## Verified beyond automated tests (Tester, real two-till pairing)

- **Setup.** Main on :8092 and an additional till on :8093, paired through
  the real `pair-start` / approve flow, link `linked`. Playwright, no
  reloads.
- **`kill -9` of the main till.**
  - The chip and the catalog lock flipped in 2.0 s.
  - Categories flipped in 4.0 s.
  - The Designer flipped in 2.5 s.
- **Clean `SIGTERM` of the main till.** The flip took 61 s. This is
  existing chip logic: the goodbye falls through to the PrimaryWatch window.
  Filed as ut-docs#4422.
- **Restart.** Everything re-enabled in 3–8 s.
- **Locked view.** Close, every tab, search, the Labels tab and the Image
  tab stayed usable. A fresh page load while the main till was down was
  server-rendered locked.
- **Screenshots, looked at.**
  - Viewports and locales: en and fa (RTL), each at 1024×600 and 360,
    locked vs unlocked.
  - The dialog head buttons sit at identical coordinates.
  - The note pushes the tabs down by 61 px at 1024 and 110 px at 360, where
    it wraps to 3 lines.
  - No horizontal overflow.
- **Not verified:**
  - real touch hardware;
  - a pulled network cable (the process was killed instead);
  - German length, since the `de` pack isn't installed on a throwaway
    till; no new keys either way;
  - dark theme;
  - modifier attach/detach live (the demo items have no groups);
  - the `/inventory` banner flipping live (template covered by Go tests).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1 | minor | Category drag/Alt+Arrow reorder (`list-reorder.js`, `data-reorder-url` → `/api/categories/reorder`, `/api/designer/categories/reorder`) is not locked. The guard only sees `hx-post` / `form action`. | Accepted. The server refuses and the rows snap back, as before. Tracked on ut-docs#4418. Blind spot documented in the guard test's comment. |
| R2 | minor | Over-lock: the panel-wide fieldset disabled the per-variant photo upload (`/api/catalog/variant/image`), which never needs the main till. This was a regression. | **Fixed.** The variants panel now uses per-region fieldsets, and the photo cell stays outside. A test asserts it stays usable (failed on the old template). |
| R2a | — | That fix moved the row's Save inside a `display: contents` fieldset, which broke `.vg-cols > :last-child` sticky (#1967 e2e: 5 failures). | **Fixed** in `app.css`. The #1967 spec's direct-child selector was updated. 16/16 variant + lock e2e pass. |
| R3 | minor | Designer inline-form Cancel buttons sit inside the lock. | Accepted. The row toggle still closes the form. Noted on ut-docs#4418. |
| R4 | nit | The guard's route matcher double-reports a templated path. | Accepted (harmless). |
| R5 | nit | A partial swapped in under a stale chip can disagree with the server render until the next poll. | Accepted (reconciles within 5 s). |
| T1 | major | The record dialog prompted to discard after a lock flip. | **Fixed** (Tester, TDD). |
| T2 | minor | Disabled text inputs looked enabled. | **Fixed** (CSS dim). |
| T3 | minor | "+ New" openers stay enabled and open an already-locked form. The Designer has no lock note. | Deferred to ut-docs#4418. |

## Gates

- `gofmt`, `go build ./...` and `go vet`: clean.
- `go test ./internal/pages/... ./internal/ui/... ./internal/httpx/...`: ok.
- `golangci-lint`: 0 issues.
- Every guard in `ci.yml`'s build job passes. Exceptions, both down to this
  environment and not the change:
  - `guard-shellcheck-version`: no shellcheck binary here; no shell
    changes.
  - `guard-deadcode-baseline`: the binary was built with go1.26, but the
    repo needs go1.27.
- Docs-shots: the change is pixel-neutral on a main till (the locks are
  `display: contents` and only disable on an additional till). I refreshed
  the surface hash with `update-docs-shots-surface-hash.sh`. Dev's `make
  docs-shots` run left the catalogue and categories shots pixel-identical.
- Related e2e: 259 passed (Tester, 61 specs). After the review fixes, the
  variant and lock specs pass 16/16.

## Verdict

Safe to merge.

## Deferred

- ut-docs#4418: the sale screen's quick-button edit mode, category drag
  reorder, the "+ New" openers, a Designer note.
- ut-docs#4422: the chip is slow to show unreachable after a clean main-till
  shutdown.
