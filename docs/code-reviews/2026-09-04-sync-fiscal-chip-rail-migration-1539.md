# Sync/fiscal chip rail migration (ut-docs#1539)

## What shipped

The sync chip (multi-till status, `web/ui/partials/sync_chip.html`) and the
fiscal-signing chip (`web/ui/partials/fiscal_chip.html`) were the two rail
items ut-docs#1423 missed when every other left-rail control moved from an
emoji + its own tinted pill to a real `.nav-toggle` + inline SVG icon
(`internal/httpx/icons.go`). Both now render as a plain rail button:

- Two new icons added: `sync` (two-way arrows) and `fiscal` (a shield).
- Both chips are now `.nav-toggle` with a `.nav-toggle-ico`/`.nav-toggle-label`
  structure, matching the bug-report/session/help rail items exactly.
- Status (offline replica, quarantined sync entries, a stale roster, a
  degraded fiscal signer) no longer recolours the whole button — it shows as
  a small `.nav-badge` dot on the icon instead, per the product owner's own
  rule ("if it is a menu button it should be correct — if it's a
  notification it can come up from somewhere else").
- The literal reported bug — "1 Kassen" / "1 tills" — is fixed with real
  `_one`/`_other` locale key pairs (`sync.chip_till_one`/`_other`,
  `sync.chip_queued_one`/`_other`, `sync.chip_quarantined_one`/`_other`)
  across the four core locales (en/tr/fa/ar), plus matching translations
  landed in the external `ut-plugin-language-de` and `ut-plugin-language-es`
  packs (separate repos, not part of this PR's diff) so this change doesn't
  add new lang-pack drift for the keys it touches.
- `/fiscal-register` is gated on the `settings` permission — the fiscal chip
  now only links there for a session that can actually open it; a cashier
  still sees the same status chip, just not as a clickable dead end.
- The user manual (`web/help/{en,tr,fa,ar}/{sell,multitill}.md`) updated to
  describe the new no-recolour-plus-badge behaviour, and `make docs-shots`
  re-run.
- A new e2e test (`e2e/tests/nav-rail-lock-reachable-1346.spec.ts`) enrols a
  real satellite till via the same two-call API round-trip a real replica
  uses, then verifies the rail's Lock button — the item this exact file
  already found to have **zero headroom** at 1024x600 with a full manager
  session — is still a real hit-test target with the migrated, taller sync
  chip also present.

## Independent review

Reviewed by an Opus subagent in an isolated worktree, per the `reviewer`
skill (`complexity:medium` → Sonnet builds, Opus reviews). First pass found
**3 BLOCKER-class findings**, all fixed in this same round; a second local
pass (this session, after the fixes) re-verified every fix and ran the full
gate again.

### Fixed

1. **BLOCKER — the manual described behaviour this diff deleted.**
   `web/help/en/sell.md` still said "**✓ Fiscal signing OK**, or **⚠ Fiscal
   signing unavailable**" (the exact glyphs this diff removes) and
   `multitill.md` said the chip "turns amber"/"comes through as the chip's
   colour" — exactly the whole-button recolouring this card exists to stop.
   Fixed in all four locales (en/tr/fa/ar), for both `sell.md` and
   `multitill.md`.
2. **BLOCKER — the fiscal chip became a manager-gated link shown to every
   cashier, dead-ending in a 403.** `/fiscal-register` requires the
   `settings` permission (`requireManager`, `fiscal_register_page.go`);
   before this migration the OK state was a plain, non-clickable `<span>`,
   so this dead end was unreachable. Fixed: `fiscalChipHandler` now passes
   `canManage: canPerform(dp, r, "settings")`, and the template only emits
   `href="/fiscal-register"` when `.canManage` is true — an `<a>` with no
   `href` attribute renders as plain inline text (not focusable, not a
   link), so a cashier still sees the same status, just not as a link.
   `TestFiscalChip_CashierSessionGetsNoLink` added; the existing OK-state
   test now runs under an explicit manager session
   (`newFiscalTestDeps` forces real auth — `UT_AUTH=""` — so the earlier,
   session-less version of that test was only passing by accident of
   `canPerform`'s fail-open assumption, which turned out to be wrong for
   this handler).
3. **BLOCKER (unverified risk) — the rail's own documented zero headroom.**
   `nav-rail-lock-reachable-1346.spec.ts`'s own comment already measured
   `.nav` scrollHeight 614 vs clientHeight 600 (zero headroom) with a full
   manager session at 1024x600, and named the exact hazard: "one more rail
   item ... or a sync-chip/fiscal-chip wrapping to two lines pushes Lock
   further off-screen." This migration does more than wrap them — each
   chip goes from a ~21px pill to a full `.nav-toggle` (`min-height: 48px`).
   Nothing in the repo had ever rendered either migrated chip in a real
   browser before this review (`scripts/e2e_seed` never enrols a till or
   configures fiscalisation; the existing rail-icon specs target specific
   always-present icons and never trigger either chip). Fixed by extending
   that same spec file: it now enrols a real satellite till via
   `POST /api/sync/enroll-token` + `POST /api/sync/enroll` (the same two
   calls a real replica makes — no DB-level shortcut), confirms the sync
   chip renders as `.nav-toggle` with its badge in the worst-case (`warn`)
   state, and re-runs the exact Lock hit-test + real click #1346 already
   uses. **Passes**: Lock stays a genuine hit-test target and completes a
   real logout with the taller sync chip also occupying `.nav-right`. The
   test revokes the till itself (before the Lock click that would end its
   session) so it doesn't leak enrolled-till state into whatever spec runs
   next in the shared `auth`-project server — confirmed by running the full
   `auth` project (19 tests) afterward with no regression.
   **Known residual gap**: this verifies the sync chip specifically; the
   fiscal chip shares the identical `.nav-toggle` box model but needs a live
   TSE-provisioning flow to trigger for real, which is heavier tooling this
   pass didn't add. Not silently assumed safe — recorded here as a real,
   deliberately scoped gap, not closed.
4. **SHOULD-FIX — alert state conveyed by colour alone (WCAG 1.4.1).** The
   original template had a real `" ⚠"` TEXT node in the (accessible, if
   visually-hidden) label for the offline/warn case; this migration moved
   that signal into an `aria-hidden` badge with no text equivalent. Fixed:
   both anchors now carry an explicit `aria-label` restating the same
   state text already used for `title`, plus the label's full content
   (name + counts) — takes over as the whole accessible name, so
   `.nav-toggle-label`'s own text is marked `aria-hidden` alongside it to
   avoid double-announcing the same content in browse-mode screen readers.
5. **SHOULD-FIX — queued surfaced as neither a badge nor visible text.**
   The replica-side badge only fired on `.offline`; a replica that's online
   but has a real queued-sales backlog showed nothing at all at rail width.
   Fixed: the badge trigger is now `{{ if or .offline .queued }}`.
6. **NIT — badge-dot test coverage gap.** The stale-roster warn path
   (`{{ if or .quarantined (eq .class "warn") }}`) had no badge assertion —
   only the quarantine-driven warn path did. Added to
   `TestSyncChip_PrimaryModeWithTills`.
7. **NIT — comment inaccuracy.** The `.nav-badge` border comment overclaimed
   an exact colour match against `--brand`; the badge actually sits on
   `.nav-toggle`'s own translucent tint, one layer further in. Reworded.

### Accepted as-is / deferred (not blocking)

- **4 of 6 new locale keys are identical `_one`/`_other` pairs** in every
  locale (`queued`/`quarantined` are adjectival — "queued"/"in
  Warteschlange"/etc. don't change with count in any of en/tr/fa/ar/de/es).
  Linguistically correct, if a little more key surface than the literal bug
  strictly needed; left as-is for forward consistency with `till`, which
  does change.
- **Arabic only gets a binary one/other split** (`chip_till_other`:
  "صناديق" renders for count=2, where Arabic grammar wants the dual
  "صندوقان"). The `_one`/`_other` key naming implies full CLDR categories
  that aren't actually honoured. Out of scope for the reported bug (which
  was specifically the English/German "1 X" case); a genuine follow-up if
  Arabic pluralisation gets its own card.
- **The retained `.sync-chip`/`.fiscal-chip` state classes
  (`ok`/`warn`) have no CSS rule left** — kept only as DOM state markers
  (existing Go test assertions, e.g. `sync-chip warn`) and are otherwise
  dead. Defensible: cheap to keep, and removing them would be a second,
  unrelated diff just to chase test assertions that already pass.
- **No new notification-tray UI.** The ticket's own body text explicitly
  sanctions "at most leave a small badge/dot on the menu button" for the
  transient states — this codebase has no existing global notification
  surface at the nav level to hook into (checked: only page-local basket
  toasts and Settings-page banners exist), and building one is a separate,
  larger UI concept. Tracked as a follow-up
  (ut-docs#1547 — see close-out comment on ut-docs#1539) rather than
  invented unreviewed here.
- **Phone-width (≤480px) layout is unmeasured.** At that width both chips
  become full-width `.nav-toggle` rows with a *visible* label (till name +
  up to two counts) — `session_chip.html` already documents a comparable,
  unfixed overflow risk for a long operator name at the same width. Not
  re-litigated here; a real measurement (mirroring what this review just
  did at 1024x600) is reasonable follow-up work if it's ever reported live.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l .` clean.
- `go test ./...` green (full suite, not just the touched packages).
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job green,
  run from repo root, including `guard-i18n.sh`, `guard-docs-shots.sh`
  (after `make docs-shots`), `guard-help-topics.sh`,
  `guard-e2e-fixtures-import.sh`.
- `e2e/tests/nav-rail-svg-icons-1423.spec.ts`,
  `nav-rail-icon-consistency-1348.spec.ts`,
  `nav-rail-lock-reachable-1346.spec.ts`, and
  `nav-rail-svg-icons-lock-1423.spec.ts` all pass, in file order, across
  both Playwright projects — the full 19-test `auth` project run confirms
  the new till-enrolment test doesn't leak server state into a later file.
- A real screenshot of the enrolled-till, warn-state sync chip at 1024x600
  (`.nav-right`, both projects' shared server) — the badge dot renders
  cleanly on the icon with no visible clipping or overlap, same visual
  weight as its siblings (help/bug/users/tag/globe/user/lock).
- Both external language packs' own `check-key-drift.sh`, run against this
  branch's `en.json` (`UT_CORE_EN_JSON=<path>`), report only pre-existing,
  unrelated drift (`settings.update.android.*`, `tills.pairing.error.*` —
  tracked separately, ut-docs#1543) — nothing about `sync.chip_*`.

## Safe to merge

Yes, with the three blockers and two should-fix items above resolved. The
one recorded residual gap (fiscal-chip-specific live rail-height
verification) is a deliberate, documented scope boundary, not an unknown.

## Deferred / follow-up

- A notification-tray UI for offline/quarantined/degraded-signer states
  (ut-docs#1547).
- Live e2e verification of the fiscal chip specifically occupying the rail
  (needs a TSE-provisioning e2e flow this pass didn't build).
- Phone-width (≤480px) measurement of both chips' visible-label rows.
- Arabic dual-form pluralisation, if it's ever reported as a real gap.
