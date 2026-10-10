# Review: takeaway override field is a text percent input (ut-docs#2974)

Date: 2026-10-10 · Branch: `fix/2974-takeaway-percent-text`
Built by: Sonnet (complexity:easy) · Reviewed by: Opus, fresh context, isolated worktree

## What shipped

- `web/ui/pages/plugin_settings.html`: the typed takeaway-rate override
  input (`takeaway_pct_<taxCodeID>`) is now
  `type="text" inputmode="decimal" {{ percentpatternlocal }}` (was
  `type="number" step min max`). This follows the promotions and
  payment-fee percent precedent from ut-docs#1275 and #2954. The field no
  longer falls into the osk.js `.value = ""` trap for `type=number`, and
  invalid input gets the shop-language message.
- `internal/pages/plugin_settings_page.go`: `parseTaxOverrides` reads the
  value with `httpx.ParsePercentBP`. That grammar uses integers only and
  accepts `.` or `,` with at most 2 decimals. It keeps the old
  `bp <= 0 || bp > 10000` refusal. `strconv.ParseFloat` and `math.Round`
  are gone, so `1e1` (previously saved as 10%) is now refused.
- Tests (all cite ut-docs#2974):
  - `TestPercentTemplates_NotTypeNumber` checks that no percent input in
    `web/ui` is `type="number"`; it covers the `takeaway_pct_` prefix.
  - The GET render test asserts `type="text"`, `inputmode="decimal"` and
    `data-money-local="percent"` on the actual input tag.
  - `…_AcceptsDecimalComma`: `1,5` is stored as 150 and `7.25` as 725, both
    read back.
  - The bad-input list gains `1e1`, `1,555`, `100,01` and `5%`.

## Findings

No blockers and no should-fix findings. The nits below all predate this
change; the old `type=number min=0 max=100` behaved the same way.

1. An out-of-range orphan entry (stored value ≤0 or >10000, e.g. from a
   hand edit) renders a value that blocks saving the whole form. The
   browser rejects it on pattern, or the server answers 400. The workaround
   is to clear the field. Moved to a follow-up Backlog card.
2. An active row with a stored value ≤0 renders blank, and the next save
   drops it silently. Same follow-up card.
3. On a de till the prefilled value shows `7.5`, not `7,5`. This is
   cosmetic; the pattern and parser accept a dot, and promotions behaves
   the same. Same follow-up card.
4. The OSK is not driven end to end; the field is proven to be
   `type=text` + `inputmode=decimal`. This is accepted and matches the
   #1275/#2954 precedent.

## Verified beyond automated tests

- TDD re-check by the reviewer: with the old handler and template, the new
  tests fail as claimed. `1e1` gave 200 "Settings saved", `1,5` gave 400,
  and the template test failed on `type="number"`. With the fix restored,
  they pass.
- Driven run: a throwaway till on :8095 with a `takeaway_rate_overrides`
  row seeded, driven in headless Chromium.
  - With `lang=de`, typing `1,5` gives a valid field. The POST returns 200
    and the page reads back `1.5` (150 bp). `1e1` is invalid and shows
    `<body data-percent-invalid>`'s text. No page errors.
  - Screenshots checked: de at 1024×600, de at 360px (card layout,
    labels above fields), fa at 1024×600 (RTL table, numeric inputs
    right-aligned). Nothing is clipped or overlapping. Dark theme was not
    looked at; this change adds no new style.
- Gate: `gofmt -l .` clean, `go build ./...`, `go vet`, full
  `go test ./...`, `golangci-lint run ./internal/pages/...` (0 issues),
  and the `ci.yml` build-job guards. Three guards failed here for
  environment reasons only:
  - `guard-deadcode-baseline.sh`: its pinned x/tools v0.48.0 reads Go
    1.26 at most, against a go1.27 module. It is unrelated, since this
    change adds and removes no functions.
  - `guard-shellcheck-version.sh`: there is no `shellcheck` binary in this
    container. No shell script changed.
  - `run-docs-shots-guard.sh`: the docs-shots harness captures only each
    topic's first route (`/plugins`), never `/plugins/{id}/settings`, so
    no manual screenshot can change. The surface hash was refreshed with
    `update-docs-shots-surface-hash.sh` (`Docs-Shots-Unchanged: true`).
- Manual: `web/help/en/plugins.md` ("enter the takeaway percentage") and
  `tax-codes.md` (dot or comma) were already accurate, so no prose change.

## Verdict

Safe to merge.
