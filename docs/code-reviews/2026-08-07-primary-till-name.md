# The primary till gets a name (ut-docs#396)

## What shipped

The primary till had no name and no way to set one — a replica gets one
at join time (`sync.till_name`, defaulting to "Till 2"), but the wizard
never asked the primary, and there was nowhere to set it afterwards
either. Field evidence (ut-docs#396, ut-docs#405's comment): this shows
up on the busiest screen in the product — because the primary has no
name, the nav sync chip falls back to rendering an enrolled-till *count*
where a replica renders its actual name.

This card scopes to giving the primary a name only (mirroring the
existing `store.name` pattern); fixing the nav chip itself is ut-docs#405,
a separate Ready card that depends on this one.

- New setting key `till.name` — distinct from a replica's own
  `sync.till_name` (different concept, kept separate; verified by grep
  that no handler reads/writes the wrong key).
- `/setup` wizard (step 3, alongside the shop-name field): asks for the
  till's name, pre-filled via the input's `value=` (not `placeholder=`)
  so the default submits correctly on an untouched form — "accepted with
  one tap" was the card's own acceptance criterion.
- `POST /api/setup` persists the submitted (trimmed) name; a blank
  submission relies on default-on-read rather than writing an explicit
  default, matching `store_name`'s own existing handling exactly.
- `tillNameOrDefault(ctx, d, locale)` (`internal/pages/ask_api.go`,
  next to `storeNameOrDefault`) — default-on-read, no migration needed;
  covers every pre-existing/upgraded install automatically.
- Settings page: a **dedicated** editable field in the existing "🔗
  Tills" card (`POST /api/settings/till-name`, manager-gated, mirroring
  `POST /api/settings/display-mode`'s shape) — not just the generic raw
  settings.all key/value table, per the card's explicit ask.
- `/tills` "Enrolled" list: previously listed only replica tills from the
  `tills` table, so the primary was invisible on its own tills page. Now
  shows the primary's own row (name only, no revoke control — it isn't a
  `tills`-table row), gated to devices that are actually the primary
  (`SyncPrimaryURL(ctx) == ""`) so a replica never gets a fabricated
  self-row.
- i18n: new keys in all four locale files (ar/en/fa/tr); manual topics
  updated in all four locales (`users.md` for `/setup`, `display.md` for
  `/settings`, `multitill.md` for `/tills`); screenshots regenerated
  (`make docs-shots`, run from this session directly — see Environment
  note below).

## Independent review (Opus, fresh context, different model from the
Sonnet subagent that implemented this)

Ran the real gate itself (not just read the diff): `go build`, `go vet`,
targeted + full `go test ./...`, all four guard scripts, and personally
re-verified two TDD claims by reverting the specific implementation piece
and confirming the test failed with the expected error before restoring.
Findings, all fixed in this same pass (none were blocker-class — no
money/tax/data-loss/security issue — so per this pipeline's process rules
this stayed a single review round, no second full pass):

1. **Real-but-minor — dead control on a replica.** The Settings "Till
   name" field rendered unconditionally, but nothing reads `till.name` on
   a replica (`/tills` already correctly hid the primary-row for a
   replica; the nav chip uses `sync.till_name`). A manager on a joined
   till could edit and save the field and nothing anywhere would change.
   **Fixed:** gated the field behind the same `SyncPrimaryURL(ctx) == ""`
   check `/tills` already uses (`"IsPrimaryTill"` in the `GET /settings`
   data map). New test:
   `TestSettingsPage_TillNameFieldOnlyOnPrimary` (verified red on
   unmodified `main` via `git stash`, green after).
2. **Real-but-minor — untranslated default leaks into translated UI.**
   `tillNameOrDefault`'s fallback was a bare Go string `"Till 1"`, while
   the wizard prefills with the **translated** `setup.till_name.default`
   key. On any upgraded install (`till.name` unset — every existing shop
   at release), `/tills` and Settings would show the Latin "Till 1" in an
   RTL Farsi/Arabic UI, and a manager saving without editing would
   **permanently persist the English string** — while the manual topics
   in fa/ar/tr document the translated default. The original design
   instruction ("mirror `storeNameOrDefault`'s shape exactly") didn't
   transfer cleanly here: `storeNameOrDefault`'s bare-string default only
   ever reaches non-template contexts (receipts, mDNS, AI prompts); every
   caller of `tillNameOrDefault` renders into a template that already has
   `T`. **Fixed:** `tillNameOrDefault` now takes a `locale` parameter and
   falls back through `httpx.T(locale, "setup.till_name.default")`,
   mirroring the precedent at `settings_page.go`'s existing
   `httpx.T(locale, …)` calls. New test:
   `TestTillNameOrDefault_TranslatesPerLocale` (fa/ar).
3. **Real-but-minor — silent success/failure on the new form.** The
   till-name form had no `hx-on::after-request`, so a 403 (cashier) or a
   500 was visually identical to success. **Fixed:** added the same
   `if(event.detail.successful){window.location.reload()}else{…hidden=false}`
   pattern six sibling forms on this page already use (reveals the
   page's shared `#settings-save-error`).
4. **Nitpick — wrong i18n key on the save button.** `{{ T
   "settings.update" }}` is the software-update key family's root, not a
   generic "save" label; `{{ T "common.save" }}` already exists and is
   already used for this exact purpose on the sibling auto-update-time
   form. **Fixed.**
5. **Nitpick — no server-side length cap on the new endpoint.**
   `maxlength="60"` was client-only. **Fixed** on the new
   `POST /api/settings/till-name` endpoint only (rune-safe truncation,
   not byte-slicing, since till names are commonly non-ASCII). Left
   `POST /api/setup`'s `store_name`/`till_name` handling as-is — it
   already matches `store_name`'s own existing (uncapped) precedent
   right next to it, and capping only `till_name` there would be a local
   inconsistency for no real gain; the store_name gap is pre-existing and
   out of scope for this card.
6. **Nitpick, accepted as-is — a blank Settings submit can't clear the
   name.** Consistent with `store_name`'s existing behavior; the 204 on
   a no-op blank submit is a shared, pre-existing pattern, not something
   this card introduced.
7. **Nitpick, accepted as-is — "Enrolled tills" header/`tills.none` copy
   drifts slightly** now that the list also carries a non-`tills`-table
   primary row. The `(this till)` label disambiguates it and the manual
   explains it; a header reword would be a separate, small, cross-cutting
   polish item, not blocking.
8. **Nitpick, accepted as-is — fa uses Eastern Arabic-Indic digits
   (`۱`) for its default name, ar uses ASCII (`1`).** Both are normal,
   defensible choices in their own locale; noted only because the two
   RTL locales differ from each other, not because either is wrong.

## Verified beyond automated tests

- Drove a real fresh-install till (`e2e/run-till-auth.sh`, genuine
  first-boot, not a mocked handler) through the actual wizard in a real
  Chromium instance and looked at the screenshots:
  - Step 3 (till name), English: `value=` pre-fills "Till 1" as real
    (non-placeholder) text — confirmed it submits correctly untouched.
  - Step 3, Farsi (RTL): labels right-aligned, translated default
    "صندوق ۱" pre-filled, no overlap, direction correct.
  - Settings → Tills card, English (light and a second theme): "Till
    name" field showing the persisted name, "Save" button, no layout
    overlap.
  - Settings → Tills card, Farsi (RTL): correctly mirrored (icon side,
    text alignment), translated label, no CSS regression from a
    logical-properties-only stylesheet.
  - `/tills` Enrolled list, English (dark-ish theme) and Farsi (RTL):
    primary's own row ("Till 1 (this till)" / "Till 1 (این صندوق)")
    with no revoke control, en dashes for the enrolled/last-seen columns
    it doesn't have data for.
  - Re-checked the Settings card after the review fixes: button now
    reads "Save", still renders cleanly.
- Did **not** re-drive a second real-browser pass in Arabic or Turkish
  specifically (fa was used as the RTL representative, ar as the second
  RTL locale for the translated-default fix) — accepted gap, low risk:
  no new CSS was authored, every new element reuses existing
  `<label>`/`<input>`/`<form>`/`<table>` patterns already proven RTL-safe
  elsewhere on the same pages.
- Did not attempt a true "dark" theme (the built-in theme list is named
  Amber/Fresh/Monarch/Slate, not Light/Dark; picked "Slate" as the
  closest palette-variance check — it turned out to be a light-blue
  variant, not literally dark). Low risk: no new CSS, existing card
  styling only.

## Environment note (unrelated to correctness, worth recording)

The Dev subagent's sandbox couldn't reach `cdn.playwright.dev` to
`playwright install`, so it could not run `make docs-shots` and flagged
the guard as failing. This orchestrating session has a pre-installed
Chromium at a different bundled version than the one this repo's
`@playwright/test` expects; resolved by pointing
`chromium_headless_shell-1228` at the installed
`chromium_headless_shell-1194` build via symlinks (session-local
workaround, not a repo change) rather than editing the repo's Playwright
config. `make docs-shots` then ran for real and the guard is green.

## Safe to merge

Yes. `go build`, `go vet`, full `go test ./...` (only the known
pre-existing, unrelated `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`
sandbox failure — reproduced identically on unmodified `main`),
`guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`, and
`guard-docs-shots.sh` are all clean. No repository-pattern, money, or
plugin-signing surface touched.

## Explicitly deferred (new Backlog candidates, not this diff)

- The nav sync-chip fix itself — ut-docs#405 (already queued, depends on
  this card).
- `check-lang-pack-drift.sh` (a separate CI check, not part of this
  repo's own "before committing" gate or the pipeline's dev/tester gate
  list) flags that this diff's 4 new i18n keys are missing from the
  external `ut-plugin-language-de`/`-es` pack repos. Confirmed this is
  **not a regression this diff caused going from green to red**: the
  `es` pack was already missing 42 unrelated keys before this diff
  touched anything (pre-existing, large, unrelated drift). Adding 3-4
  more core keys without also touching those external plugin repos
  (out of this session's repo scope) is a pre-existing, standing gap in
  those packs' own maintenance cadence, not something this card can
  responsibly fix by guessing Spanish/German/Turkish translations on its
  own authority.
- "Enrolled tills" header wording once the list can carry both
  `tills`-table rows and the primary's own synthetic row (nitpick #7
  above) — small, cross-cutting, not urgent.
