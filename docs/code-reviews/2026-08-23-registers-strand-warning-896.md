# Code review: warn in-page before a register change strands a till binding

- **Card**: universaltill/ut-docs#896
- **Branch**: `fix/896-registers-strand-warning`
- **Complexity**: easy
- **Reviewer model**: fresh-context Sonnet subagent (independent of the build)

## What shipped

`internal/pos/register_identity.go`: a till's persisted `sync.till_register_id`
naming a now-deactivated register is re-resolved, and with 2+ active
registers this can come back `ErrRegisterIdentityAmbiguous`, which then
makes `internal/pages/shifts_api.go` refuse cash payouts with a 409. The
Registers admin page made both triggers — deactivating a register, or
creating a second one — one click away with no warning. This adds:

- `{{ helpLink "multitill" }}` next to the page's `<h1>` (matches the
  existing pattern in `permissions.html`/`tax_codes.html`) — `/registers`
  is already covered by the `multitill` topic's `routes:` front matter, so
  this is not a competing `routes:` claim, per `CLAUDE.md`'s help-topics
  rule.
- A short, always-visible `<p class="muted">` note under the page title
  (`registers.strand_warning`) pointing the manager at Settings → Tills →
  "This till's register" to check afterward.
- The new locale key added identically to all four `web/locales/*.json`
  files; the ar/fa/tr translations reuse the exact wording already used
  for "This till's register" in `settings.tills.register_label` in each
  locale, so the in-page pointer matches what the manager will actually
  see on the Settings page.
- A new test, `TestRegistersPage_ShowsStrandWarning`.
- Regenerated manual screenshots + `web/help/img/manifest.json` via
  `make docs-shots` (required — `registers.html`'s template is part of the
  guard's whole-surface hash even though no topic's `routes[0]` is
  `/registers`, so no *screenshotted* page's pixels actually changed; the
  other screenshot diffs in this run — `alerts`/`designer`/`sell` — are
  unrelated re-render drift from re-running the Playwright suite, not
  caused by this diff).

## Independent review — findings and resolution

An independent, fresh-context Sonnet subagent reviewed the diff before
this record was written. Findings:

1. **Blocker (fixed): `guard-docs-shots.sh` failed.** The diff touched
   `web/ui/pages/registers.html` without regenerating
   `web/help/img/manifest.json` via `make docs-shots`, which `CLAUDE.md`
   requires for any change to a screen shown in the manual and which is a
   CI-blocking guard. Fixed by running `make docs-shots` (84/84 Playwright
   screenshot tests passed) and committing the regenerated manifest +
   changed PNGs. Guard now passes.
2. **Real gap (fixed): weak test assertion.** The original test asserted
   `data-testid="help-hint"` is present, but that attribute is rendered on
   *every* page unconditionally by `nav.html`'s own automatic "?" (which
   already resolves `/registers` to the `multitill` topic via its
   pre-existing `routes:` claim) — so the assertion passed regardless of
   whether the new `{{ helpLink "multitill" }}` was actually added.
   Replaced with a count of `href="/help/multitill"` occurrences (want 2:
   the nav's auto-link plus the new explicit one), which does exercise the
   change.
3. **No findings** on correctness/acceptance-criteria fit, i18n
   completeness (`guard-i18n.sh` clean), helpLink/route-ownership
   convention (`guard-help-topics.sh` clean), or regression risk to the
   existing `registers_page_test.go` suite (all pre-existing tests still
   pass).

## Verification performed personally (not just trusting the diff/tests)

- `gofmt -l .` — clean.
- `go build ./...` — clean.
- `go test ./...` — full suite green (all packages, including
  `internal/pages` at 55s and `internal/plugins` at 85s).
- All 16 CI-blocking guards from `universal-till/CLAUDE.md`'s "Before
  committing" list run individually — all pass, including
  `guard-docs-shots.sh` after the fix.
- Manually inspected the regenerated PNGs (valid, correctly-sized
  1024×600 images, not corrupted/zero-byte).
- Confirmed `registers.strand_warning`'s ar/fa translations reuse the
  same "This till's register" wording as the existing
  `settings.tills.register_label` translation in each locale file, so the
  in-page pointer is consistent with the actual Settings UI text.

## Non-goals (unchanged from the card)

- No hard block on register creation/deactivation — a warning only, per
  the card's acceptance criteria ("Doesn't need to be a hard block").
- No change to `register_identity.go`'s resolution/ambiguity behavior
  itself — this is a discoverability fix, not a behavior fix.
