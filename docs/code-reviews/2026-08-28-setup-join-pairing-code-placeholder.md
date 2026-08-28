# 2026-08-28 — Setup wizard "Join an existing shop": non-technical pairing-code placeholder

**Card:** universaltill/ut-docs#1179 (complexity: easy)
**Reported by:** product owner, 2026-08-27, on a real Pi 5 till's second-till setup flow.

## What shipped

The manual pairing-code textbox on the first-boot setup wizard's "Join an
existing shop" step (`web/ui/pages/setup.html`) had a placeholder showing
the raw wire shape: `{"token":"…","url":"http://…"}`. A shop owner is never
going to hand-type or even recognise that — the screen's own copy already
says "scan or paste the code here," implying QR-scan is the real primary
path.

Fix: reuse the `tills.join_code_ph` i18n key ("Paste the code shown on the
other till"), which the sibling settings-page join form (`web/ui/pages/tills.html`)
already uses for the identical field. The key is pre-existing — already
present in all four shipped locale files (`en`, `ar`, `fa`, `tr`) — so this
is a pure reuse with zero i18n-key surface added, no lang-pack-drift
exposure to the external `ut-plugin-language-{de,es}` packs.

Added `TestSetupJoinCodeFieldHasNoRawJSONPlaceholder` (`internal/pages/setup_page_test.go`)
asserting the raw JSON shape is absent from `GET /setup`'s rendered body and
the friendly placeholder is present, following the TDD pattern already used
in this file (`TestSetupPageLoadsHTMXForTheJoinForm`).

## Ticket's second sub-issue — no code change, and why

The ticket also described LAN-discovered tills showing "My Store — Till ID:
`<uuid>`" instead of a real name, with two possible readings: (a) the ID
should never be the *primary* identifier, (b) a phantom, never-configured
till instance is discoverable at all (tracked separately as ut-docs#1169).

Read the actual rendering (`setup.html`'s `setup-discover-results` JS,
identical in structure to `tills.html`'s own discovery-result rendering):
the till's `name` is always rendered first/prominent, with the `till_id`
second inside `<span class="muted">` — never the reverse, and never the
only identifier shown. This is the same pattern already shipped and
reviewed on `tills.html`. Traced `p.name`'s origin to
`internal/discovery/{browse,discovery}.go`: it's the real `store.name`
setting when configured, falling back to the literal `"this shop"` only
when unset — i.e. the "My Store"/GUID-looking symptom in the report is the
phantom-till data gap (#1169), not a display-hierarchy defect in either
page. No code change made for this sub-issue; independently re-verified by
the review pass below, which agreed.

## Independent review (Sonnet, fresh context, isolated worktree — complexity:easy routing)

**Verdict: SAFE TO MERGE.** No blocking findings.

Verified independently (not taken on the implementer's word):
- `gofmt -l .`, `go build ./...`, `go vet ./...` — all clean.
- `go test ./internal/pages/...` (full package, `-count=1`) — all pass
  (~88s pages, 0.36s catalog, 4.2s common).
- `scripts/ci/guard-i18n.sh`, `guard-htmx-loaded.sh`, `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh` — all pass.
- **TDD claim re-verified by revert-then-restore**, done by the reviewer
  itself in its own isolated worktree: reverted only the template change,
  re-ran the new test, confirmed it fails with exactly the claimed
  assertion errors, restored the fix, confirmed green again with a
  non-cached run.
- Confirmed `tills.join_code_ph` is pre-existing in all 4 shipped locale
  files, and that `setup.html`/`tills.html` now render byte-for-byte
  identical placeholder markup for this field.
- Independently traced the discovery-result rendering and `p.name`'s data
  origin and agreed the second sub-issue needs no code change here.
- Checked `reference/ux-guidelines.md`'s checklist directly: no new
  hardcoded colors/spacing, text-only so trivially RTL-safe, no new modal
  blocker, reuses an existing i18n key/pattern rather than inventing one.
- Checked `web/help/` for a stale manual topic describing this field —
  none claims `/setup` with this placeholder in prose or a captured
  screenshot; no manual update required.
- Checked for the two recurring bug classes (missing `os.MkdirAll`,
  cwd-relative path vs. `paths.Data(...)`) — N/A, diff touches only a
  template string and a test, no file I/O.
- No secrets, no real client/shop names in the diff.

One non-blocking style nit noted: the new test's placeholder-text
assertion matches a literal attribute-order substring, mildly brittle to a
future attribute reorder in the template — functionally harmless, not
worth a follow-up on its own.

## Deferred / explicitly out of scope

- ut-docs#1169 (phantom till instance reachable on LAN) — separate,
  already-tracked card; this diff doesn't touch it.
- The non-blocking test-brittleness nit above — accepted as-is, too minor
  to spend a follow-up card on.
