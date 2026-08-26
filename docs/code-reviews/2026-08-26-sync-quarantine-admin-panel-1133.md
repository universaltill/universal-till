# Code review: quarantined LAN-sync journal entries admin panel (ut-docs#1133)

**Date:** 2026-08-26
**Card:** ut-docs#1133 — follow-up from the ut-docs#1127/ADR-0065 independent
review ("Not decided here": no browsing UI shipped with the original
poison-entry quarantine fix)
**Complexity:** medium — build: inline (Sonnet), review: Opus (fresh-context
subagent, isolated worktree of `feat/1133-sync-quarantine-admin-panel` @
`806a390`, one WIP commit)

## What shipped

`sync_journal_quarantine` (ADR-0065, ut-docs#1127) already recorded a
poison LAN-sync journal entry the primary can't apply, but nothing ever
read it back: the only operator-visible signal was a `Warn`-level line in
the 50-entry in-memory Problems ring, gone after a restart, while both the
replica's own sync chip and the primary's own sale counts read "fully
synced" — a completed sale could vanish from the primary's ledger with
both ends showing green.

- `internal/data/sync_quarantine_repo.go` — new `POSRepo.CountJournalQuarantine`
  (cheap `COUNT(*)`, no row materialization) alongside the existing
  `ListJournalQuarantine`.
- `internal/pages/sync_quarantine_page.go` (new) — `GET /sync-quarantine`,
  gated on the `sync_management` permission, **primary-only** (a replica
  redirects to `/settings`: `InsertJournalQuarantine` only ever fires
  while applying a replica's pushed batch, so a replica has nothing
  meaningful to show). Lists till name (resolved via `TillsRepo.ListTills`,
  falling back to the raw id for a since-revoked till), receipt, a
  translated reason, and timestamp — capped at 200 rows with an explicit
  truncation notice if hit.
- `internal/pages/settings_page.go` / `web/ui/pages/settings.html` — the
  Tills card links to the new page and shows the count when nonzero,
  primary-only, and hidden entirely on a single-till shop that has never
  enrolled a replica.
- `internal/pages/sync_admin.go` / `web/ui/partials/sync_chip.html` — the
  primary-side nav chip turns amber and links to `/sync-quarantine`
  (title included) when a nonzero quarantine count exists — a standing,
  always-rendered signal instead of a point-in-time log line.
- i18n keys added to all four locales (`en`/`ar`/`fa`/`tr`); the
  `multitill` help topic extended with the new route and a short section
  in all four locales; manual screenshots regenerated (`make docs-shots`).
- Tests: `TestSyncQuarantineRepo_Count`, `TestSyncQuarantinePage_ManagerOnlyAndRendersRealData`,
  `TestSyncQuarantinePage_ReplicaRedirectsRegardlessOfRole`,
  `TestSyncChip_PrimaryModeWarnsAndLinksToQuarantineWhenEntriesExist`,
  `TestSettingsPage_QuarantineSectionOnlyWhenRelevant`.

## Independent review (Opus, fresh-context subagent, isolated worktree)

Ran `go build ./...`, `go vet ./...`, `gofmt -l .`, the full
`go test ./internal/data/... ./internal/pages/...`, and
`guard-data-access.sh` / `guard-i18n.sh` / `guard-help-topics.sh` /
`guard-docs-shots.sh` / `guard-compliance-claims.sh` — all clean. Did TDD
revert-then-restore verification on three of the five new tests (deleting
the replica guard, the chip's quarantine-read, and the permission gate in
turn), each producing a genuine assertion failure with the exact expected
wrong value, then confirmed green again after restoring.

**No blockers.** Confirmed clean: no file writes anywhere in the diff (the
two recurring `os.MkdirAll`/`paths.Data` bug classes are structurally n/a);
primary-only gating consistent across all four surfaces (page, Settings
query, Settings template, chip) with no way for a replica to reach the
data another way; the permission gate fails closed with no session
(defence in depth: `auth.Middleware` also redirects to `/login` first);
offline-first untouched (zero interaction with checkout, one local
`COUNT(*)` on the 30s nav-chip poll); no raw error leakage; no literal
RTL-unsafe `left`/`right`; no money/float handling; no real client/shop
name in test/demo data; ADR-0065's "Not decided here" scope (manual
re-apply) not pre-empted — this page is strictly read-only.

**Three should-fix findings, all fixed same-session:**

1. **The nav chip's own early-return could delete the signal it exists to
   add.** `registerSyncAdmin`'s `GET /ui/sync-chip` read the quarantine
   count *after* an early return that fires when `ListTills` is empty —
   so revoking the misbehaving till that caused a quarantine entry (the
   obvious operator response) made the chip vanish entirely, silently
   undoing the "standing, always-rendered signal" this card exists to
   add. Fixed: the quarantine count is now read first, and a nonzero
   count alone justifies rendering the chip even with zero enrolled
   tills; the "· N tills" segment only renders when there's a nonzero
   roster to report. Covered by
   `TestSyncChip_PrimaryModeWarnsAndLinksToQuarantineWhenEntriesExist`
   (already existed) plus manual verification (below) with the till
   actually revoked.
2. **The chip's tooltip didn't follow the same branch as its `href`.**
   When quarantined entries exist, the link's `title` still read
   "Enrolled tills — open the Tills page" while the link pointed at
   `/sync-quarantine` — the one accessible description of the link
   actively misstated its destination in exactly the state the feature
   exists for. Fixed: `title` now branches on `.quarantined` the same
   way `href` does, backed by a new `sync.chip_quarantine_title` key in
   all four locales.
3. **The Reason column rendered untranslated English under a translated
   header.** `permanentJournalFailureReason` (sync_sales.go) persists a
   closed 2-value English allowlist straight into the DB; the new page
   rendered it verbatim next to a translated "Reason" column header —
   invisible to `guard-i18n.sh` (a DB value, not a template literal) but
   a real violation of the no-hardcoded-user-facing-strings rule now
   that this is a first-class UI column, not a log line. Fixed: a
   `quarantineReasonKeys` map in `sync_quarantine_page.go` translates the
   two known reasons via `httpx.T`, falling back to the raw string for
   anything unrecognised (forward-compat safety net, not silently
   blanked) if `permanentJournalFailureReason` ever grows a case without
   a matching map update. New locale keys added to all four locales.

**Two nits fixed same-session (cheap, directly actionable):**

- The 200-row cap had no truncation notice — a shop with >200 entries
  would see a silently partial list next to an accurate total on
  Settings/the chip. Fixed: `Truncated` flag + `sync.quarantine_truncated`
  message when the cap is hit.
- The Settings card's quarantine help text + button rendered even on a
  single-till shop that has never enrolled a replica and structurally
  cannot have a quarantine row yet — premature noise contradicting the
  chip's own "nothing to sync yet" design. Fixed: a `ShowQuarantineSection`
  flag, true when a replica has ever been enrolled OR `QuarantineCount >
  0` (the OR, not "currently enrolled" alone, matters: it's what keeps
  the section visible after the finding-1 revoked-till scenario).
  Covered by the new `TestSettingsPage_QuarantineSectionOnlyWhenRelevant`
  (asserts hidden → shown-on-enrol → still-shown-after-revoke-with-count).

**One nit fixed (docs wording), one accepted-deferred:**

- The English help topic's example trigger ("a voucher redeemed twice on
  two tills at once") matched neither real ADR-0065 trigger. Fixed to
  name the actual two cases (a colliding voucher code issued offline on
  two tills; a voucher redeemed that the primary has never heard of).
  ar/fa/tr already used a generic "extremely rare case" phrasing with no
  specific (and therefore no misleading) example — left as-is.
- **Accepted, deferred:** a primary later demoted to a replica (joins
  another primary) makes its own pre-existing quarantine rows
  unviewable through the UI until it's promoted back — the page
  redirects on `sync.primary_url != ""` regardless of history. Low
  likelihood (a shop demoting its established primary is rare), no data
  loss (rows persist in `sync_journal_quarantine` and remain queryable
  directly), and building cross-role visibility for this edge case is
  more machinery than a follow-up like this one should carry — noted
  here rather than a new backlog card, since it only matters if that
  demotion path is ever used at all.

## Verified beyond automated tests

- **Real running app**, three throwaway auth-off tills (fresh temp data
  dirs, killed after each check): (1) settings + `/sync-quarantine` with
  two seeded quarantine rows against a live enrolled till; (2) a
  single-till shop with nothing ever enrolled; (3) a till enrolled,
  quarantined, then revoked — reproducing the exact scenario finding 1
  fixes. Confirmed via real DOM queries (`.sync-chip a`'s `href`/`title`/
  text/class, `a[href="/sync-quarantine"]` presence) after each fix, not
  just rendered-HTML-string assertions.
- **Visual check, read not just asserted:** screenshots taken and looked
  at for `/sync-quarantine` and the Settings Tills card in **en** and
  **fa** (RTL) at the product's 1024×900 kiosk-adjacent viewport, plus
  the nav bar alone in **ar** to check the longest chip text
  ("١ · ١ صندوق · ٢ قرنطينة‌شده ⚠"-shaped strings) doesn't overflow. RTL
  table columns mirror correctly (no hardcoded left/right), the
  Settings card's help text + button wrap cleanly under Persian/Arabic
  text, nothing overlaps or clips. **Not separately checked:** an
  alternate curated theme (Monarch, the default, was what rendered) —
  the page introduces no bespoke CSS, only classes (`.card`, `.table`,
  `.muted`, `.btn`) already proven across themes on `audit.html`/
  `tills.html`, so this is a low-risk, explicitly-named gap rather than
  a silent one.
- **TDD**: five new/changed-behavior tests, three independently
  re-verified by the reviewing subagent via revert→fail→restore→pass
  (see above); the other two (`TestSyncQuarantineRepo_Count`,
  `TestSettingsPage_QuarantineSectionOnlyWhenRelevant`) are additive/new
  logic where a revert is either a compile error or was authored and
  run against the fix directly in this same session.
- Full `go test ./...` (every package, not just the two touched) and
  `go vet ./...` clean after the should-fix/nit round of edits, not only
  before it.

## Safe to merge

**Yes.** No blockers found or remaining; all three should-fix findings and
three of four nits were fixed and re-verified same-session; the one
accepted-deferred nit is genuinely low-likelihood, causes no data loss,
and is recorded here rather than silently dropped.
