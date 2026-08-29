# 2026-08-29 — Import Catalog: `.bkp` files from cloud storage unselectable

**Ticket:** universaltill/ut-docs#1247 (p1, complexity:easy)
**Branch:** `fix/1247-import-bkp-cloud-storage-accept`

## What shipped

`web/ui/pages/import.html`'s file input had
`accept=".csv,text/csv,.bkp,application/zip"`. On Android, `accept`
becomes a MIME-type allowlist for the system document picker. Google
Drive's `DocumentsProvider` (and other cloud-storage providers) reports an
unrecognised binary — a proprietary, renamed-ZIP `.bkp` included — as
`application/octet-stream`, which matched none of the allowed MIME types,
so the file was unselectable, even though the server-side importer
(`internal/catimport.ParseBkp`, dispatched via `sniffZipUpload` in
`internal/pages/import_page.go`) auto-detects a `.bkp` upload purely by
ZIP magic-byte sniffing — never by filename or MIME type.

Fix: widen the `accept` attribute additively to also include
`application/octet-stream`, keeping every existing entry (a purely
permissive change — the server enforces real content validation
regardless of what the client's `accept` hint allowed through).

Added a Go handler-level regression test
(`TestImport_FilePickerAcceptsCloudStorageOctetStream`) asserting the
rendered `/import` page's HTML carries the widened `accept` string.

`web/help/img/manifest.json` was regenerated via `make docs-shots`
(surface hash changed since `web/ui/pages/import.html` is part of the
hashed surface); the regen produced byte-identical PNGs for all 92
screenshots — `/import` isn't itself a screenshotted topic route (it's
`catalog`'s secondary route, not any topic's primary `routes[0]`), and
the `accept` attribute has no visual effect regardless.

## Independent review (Sonnet, fresh-context subagent, isolated worktree — complexity:easy)

**Verdict: PASS, no blocking findings.**

- Verified the root-cause claim by reading `internal/catimport/bkp.go`
  and `internal/pages/import_page.go`'s `sniffZipUpload` directly (not
  the diff's own comment): confirmed dispatch is pure ZIP magic-byte
  sniffing, no filename/MIME check anywhere in the path.
- Confirmed `application/octet-stream` is the real-world MIME Android's
  Storage Access Framework reports for an unrecognised file type — a
  documented Chromium/WebView `accept` + SAF interaction, not a guess.
- TDD re-verified independently: reverted only the `accept` attribute
  edit (test left in place), confirmed
  `TestImport_FilePickerAcceptsCloudStorageOctetStream` fails with the
  real assertion message naming the missing MIME type; restored the fix,
  confirmed it passes again; confirmed `git diff --stat` matched the
  committed fix exactly afterward.
- Full gate re-run independently in the isolated worktree: `gofmt -l .`
  clean, `go build ./...` / `go vet ./...` clean, full `go test ./...`
  green across every package, `guard-i18n.sh` / `guard-data-access.sh` /
  `guard-compliance-claims.sh` / `guard-help-topics.sh` all pass, and
  `guard-docs-shots.sh` independently recomputed the same surface hash
  (`bed8ba51676a…`) already committed in `manifest.json` — confirming the
  regenerated manifest is genuinely current, not stale.
- Scope/hygiene: no real client/shop name, no secret-shaped literal, no
  i18n key needed (`accept` isn't user-visible text — `guard-i18n.sh`
  agrees), and the existing help topic (`web/help/en/catalog.md`)
  documents `/import` only at the "CSV or `.bkp`, detected
  automatically" level — it never described picker MIME-filtering
  mechanics, so no manual prose update is owed by this fix.
- Two **non-blocking** observations, not defects: (1) the widened accept
  list is broad enough that Android's picker now weakly filters almost
  any cloud-provider-reported-`octet-stream` file, not just `.bkp`-shaped
  ones — expected, since the server is the real gate; (2) an alternative
  fix (dropping MIME entries, keeping only extension hints) was
  considered and is a style preference, not a correctness issue — the
  additive approach taken here is simpler and strictly safe.

## Verified beyond automated tests

- Screenshotted `/import` at 1024×600 in both `en` (LTR) and `fa` (RTL)
  after the change (via a throwaway Playwright script against the
  pre-installed Chromium, since `accept` has no visual effect and this
  was to confirm exactly that): both render identically to the
  pre-existing layout — label/input/buttons correctly aligned, RTL
  mirroring correct, "Choose File"/`پیش‌نمایش`/`درون‌ریزی` controls intact.
  No layout impact, as expected for an attribute with no CSS/DOM
  footprint.
- Ran the existing `e2e/tests/catalog-import-friendly-errors.spec.ts`
  Playwright spec against the change (via a local, uncommitted config
  pointing at this session's pre-installed Chromium — the main
  `playwright.config.ts` isn't wired for the offline-Chromium fallback
  the way `playwright.docs.config.ts` is, and this cloud session has no
  network path to `cdn.playwright.dev` to fetch the pinned revision
  otherwise): passes unchanged.
- **Not verified**: real Android device confirmation with an actual
  Google Drive-sourced `.bkp` file, as the ticket's own acceptance
  criteria calls for ("Verify on a real Android device — WebView
  file-chooser behaviour is provider- and OS-version-specific"). No
  Android hardware is reachable from this cloud pipeline session. The
  fix is well-grounded in the documented SAF/Chromium mechanism and the
  server-side code path was read directly to confirm the claim, but this
  specific acceptance criterion remains open pending a real-device check.

## Safe-to-merge verdict

Yes. No blocking findings from independent review; full gate green; fix
is minimal, additive, and cannot regress existing behavior (server-side
validation is unchanged and was already the real gate).

## Explicitly deferred

- Real Android + Google Drive device confirmation (see above) — the
  card's own acceptance criteria names this explicitly; left as an open
  acceptance item rather than something this cycle can close from a cold
  cloud session.
