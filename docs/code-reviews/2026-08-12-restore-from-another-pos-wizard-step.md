# 2026-08-12 — Setup wizard "restore from another POS?" step (ut-docs#617)

## What shipped

A new setup-wizard step, inserted between the shop-type step and the
admin-PIN step: "Are you moving from another till system?" — **No** /
**Yes** / **Later**.

- **No** — instant skip, nothing persisted, wizard continues as before.
- **Yes** — reveals a picker: a generic **CSV / Excel** option (the only
  real path today — zero `ut-plugin-import-*` plugins exist yet, so the
  copy says so honestly rather than listing fabricated vendor names).
  Picking it and completing the wizard redirects straight into the
  existing `/import` catalog importer instead of the till's home screen —
  no detour through Settings → Catalog.
- **Later** — persists a new settings key
  (`common.KeyRestorePromptStatus = "deferred"`); Settings → Data then
  shows an "Import from another POS" resume block (link into `/import`
  + a "No thanks" dismiss action, manager-gated
  `POST /api/settings/dismiss-restore-prompt`).

No network call anywhere in the new code path — it's pure local UI
state + a settings write, so offline-first is a non-issue by construction
rather than something bolted on.

Renumbers the wizard's Alpine.js step indices (PIN 5→6, Done 6→7) and
adds `data-step="N"` to every `<section>` for stable test targeting.
i18n keys added to all 4 locales (ar/en/fa/tr). Help topic
(`web/help/*/users.md`, the topic that already claims the `/setup`
route) documents the new step. `web/help/img/**` + `manifest.json`
regenerated via a real `make docs-shots` run.

## Independent review (Opus, fresh context, worktree-isolated)

**One blocking issue found and fixed before merge:**

- **B1 — the Yes → CSV/Excel path was unreachable through the actual UI.**
  The Alpine `choice` variable was doing double duty as both "which panel
  is shown" (`x-show="choice === 'ask'"`) and "what did they answer." The
  radio's `@change="choice = 'csv_excel'"` satisfied the *first* use by
  accident and broke the *second*: the moment `choice` became
  `'csv_excel'`, the panel's own `x-show` condition (`=== 'ask'`) went
  false and the whole panel — including the now-enabled "Next" button —
  disappeared. Worse, it was a genuine dead end: re-tapping **Yes**
  re-showed the panel with the radio still checked (Alpine state doesn't
  reset on re-entry) but `choice` reset to `'ask'`, so Next was disabled
  again, and a checked radio's own `change` never refires from a second
  click on the same option. So `restore_choice=csv_excel` could never
  reach `POST /api/setup` from any click sequence, even though the
  server-side handling of it (redirect to `/import`) was correct and had
  a passing Go test — the test posted the form value directly, bypassing
  the broken UI entirely. Confirmed live via a DOM probe (computed
  `display`/`disabled` per panel before and after picking the radio).

  **Fix**: split the state — `asking` (boolean, which panel) is now
  independent of `choice` (string, the answer). Back inside the CSV
  panel now explicitly resets both (`asking = false; choice = ''`).

Fixed in the same commit, verified for real (not just re-read): added a
genuine e2e regression test in `e2e/tests/login.spec.ts` that drives
Yes → check the CSV radio → assert the "Next" button was disabled before
and enabled after → click it → complete the wizard → assert the browser
actually lands on `/import` (not home). This is real coverage of exactly
the acceptance criterion ("Yes → CSV/Excel … no detour through
Settings/Catalog navigation") that was previously untested at the UI
layer.

**Non-blocking findings, addressed in the same pass** (cheap and
directly improve correctness/clarity, so fixed rather than deferred):

- The e2e spec comments claiming the old `.setup-nav button:visible`
  locator was "flaky due to N same-text buttons" were themselves
  inaccurate — `:visible` genuinely disambiguated correctly (Playwright
  strict mode would have raised on real ambiguity). The real reason the
  helpers needed updating: the new step 5's default panel has *no* "Next"
  button at all, so the old flat click sequence would hunt for one that
  isn't there. Rewrote the comments in both `login.spec.ts` and
  `docs-shots.spec.ts` to say so accurately, and switched the locators
  from a whitespace-fragile `section[x-show="step === N"]` attribute
  match to the new `data-step="N"` attribute (a reformatting of the
  Alpine expression would otherwise silently break four specs).
- Dead form field: the CSV radio's `name="restore_source"` was posted to
  `/api/setup` and never read anywhere (`restore_choice` is the real
  carrier). Removed.
- `tr` ("Hayır, sıfırdan başlıyorum") overflowed the fixed-width
  `.setup-nav` flex row by ~11px at a 360px kiosk/phone viewport — the
  other three locales fit. `.setup-nav` gained `flex-wrap: wrap`,
  matching the sibling `.setup-langs` rule two lines above it in
  `app.css`.
- Naming collision: Settings → Data already has a **destructive**
  "Restore" (from a local backup, replaces all current data) right above
  where this **non-destructive** "Restore from another POS" block would
  have sat, in all 4 languages (e.g. Turkish "Geri yükle" used for both).
  Renamed the new copy to "Import from another POS" (and each locale's
  translation) to keep the two clearly distinct; updated the matching
  help-topic sentence in all 4 `users.md` files to match.

**Accepted, deferred, explicitly not fixed here** (judgment calls, not
defects in this card's scope):

- `restore_choice` is lost on a PIN-format-error re-render of the wizard
  (`renderWizard` only re-threads country/currency/tax on a POST
  re-render, by explicit pre-existing design — see its own comment). An
  operator who picks "Later" and then mistypes their PIN loses the
  deferral. This is the same existing gap `store_name`/`shop_type`/
  `demo_data` already have, not a regression introduced here — consistent
  with the current product boundary rather than a new defect.
- Choosing "Yes" → CSV/Excel does not set any safety-net/resume flag, so
  an operator who lands on `/import` and navigates away without actually
  importing has no way back short of Catalog → Import manually — arguably
  the *less* committed "Later" answer is better protected than the *more*
  committed "Yes" answer. Real product-shape question, not obviously
  right or wrong either way; left as-is rather than guessing at a design
  the card didn't ask for.
- `/import`'s "must not duplicate a catalog already imported" holds for
  any row with a barcode or SKU (`internal/data/catalog_repo.go`'s
  `ON CONFLICT` upsert, confirmed by reading the repo, not assumed) but
  not for a row with neither — pre-existing `/import` behaviour, not
  introduced or worsened by this card.

## Verified beyond automated tests

- Real browser, real till, both fixed and pre-fix states — not just a
  rendered-HTML-string assertion: DOM probes of computed `display`/
  `disabled` confirmed both the original bug and the fix, in a live
  Chromium (`/opt/pw-browsers/chromium`, this environment's pre-installed
  revision, via a temporary uncommitted Playwright config override —
  see `ut-docs#620` for why the default resolution doesn't work in a
  cloud session).
- Visual check: en + fa (RTL) screenshots of the initial step, the
  Yes/CSV sub-panel, and the Settings resume block + its dismiss action —
  RTL mirrors correctly (buttons, radio position, dot progress), Farsi
  renders without overflow. ar/tr were not separately screenshotted by
  hand, but i18n key parity is guard-enforced and the `tr` overflow the
  review found (N5) was caught by measurement, not a screenshot, and is
  fixed.
- Full `make docs-shots` regeneration (68 real captures, 2 real till
  servers) run twice — once before the B1 fix (baseline, confirmed the
  auth-till helper broke exactly where the UI bug was), once after (all
  68 pass). Only `alerts`/`designer` PNGs changed pixel-wise, both
  already documented in `docs-shots.spec.ts` as carrying non-deterministic
  timestamp content unrelated to this change — no other topic's rendered
  output changed, consistent with `/setup` not being independently
  screenshotted by this harness.
- `go build`, `go vet`, full `go test ./...` (all 40-odd packages, not
  just `internal/pages`), and all 6 repo guards (`guard-data-access`,
  `guard-kiosk-engine`, `guard-plugin-menu-read`, `guard-i18n`,
  `guard-help-topics`, `guard-docs-shots`) — all green, run fresh after
  the B1 fix, not just before it.
- No real client/shop name anywhere in test or seed data ("Demo Shop",
  "Corner Shop", "E2E Test Shop" only). No secret-shaped literal added.

## Safe to merge

Yes, after the B1 fix above (already in this branch, not a follow-up).

## Deferred / follow-up

Nothing new filed — the three accepted-deferred items above are
judgment calls within this card's existing scope, not separate defects,
and don't warrant their own backlog cards.
