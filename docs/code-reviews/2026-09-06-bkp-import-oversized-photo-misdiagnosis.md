# Code review: .bkp import oversized-photo misdiagnosis (ut-docs#1623)

**Date:** 2026-09-06
**Branch:** `fix/1623-bkp-import-oversized-photo-misdiagnosis`
**Card:** universaltill/ut-docs#1623
**Complexity:** easy → reviewed by a fresh-context Sonnet subagent (per
`scrum-master`'s Model routing by complexity)

## What shipped

`internal/pages/import_page.go`'s `.bkp` commit-time image-write path
reported every `imaging.Decode` failure — including
`imaging.ErrTooManyPixels` for a real, valid, decodable photo over the
~6-megapixel cap (e.g. a 12MP phone photo) — as
`import.status.image_undecodable` ("the item's photo could not be
read"). A shop owner importing a `.bkp` full of ordinary phone photos
would see every one of them wrongly diagnosed as unreadable.

Fix: branch on `errors.Is(derr, imaging.ErrTooManyPixels)`, the same
pattern already used by the two manual-upload handlers in
`internal/pages/catalog/handlers.go` (single-image and variant-image),
and surface a distinct warning (`import.status.image_too_many_pixels`)
for the too-large case. The genuinely-corrupt/unsupported-format case
is unchanged.

**Design note on the locale key name:** the issue's own suggested key
name (`import.status.image_too_large`) turned out to already exist —
used by `translateImageIssue` for `catimport.ImageIssueTooLarge`, a
different, earlier check (archive-resolution-time byte-size cap, not
decode-time pixel-count) with a `%s` parameter for the raw archive
path. Reusing it here would have either produced a literal unformatted
`%s` in the message (no arg supplied) or mixed two different parameter
semantics under one key. Used `import.status.image_too_many_pixels`
instead — non-colliding, and maps directly to the sentinel error name.

**i18n:** new key added to all four `web/locales/*.json` (en/ar/fa/tr).
The homelab Ollama translation endpoint (`192.168.1.231:11434`, per
`reference/translation.md`) is unreachable from this cloud session
(private LAN address, confirmed via a direct connection attempt) — the
three non-English strings were translated directly as part of this
change rather than via the usual pipeline. This is a short, formulaic
sentence following the exact structure of the existing sibling key
(`import.status.image_undecodable`) in each locale, so risk is low, but
it hasn't been through a native-speaker pass — flagging for one if a
human reviewer wants to spot-check.

## TDD

Confirmed the bug first: updated the existing test's assertion to the
correct message and watched it fail against the unfixed code with
exactly the reported symptom (response contained "photo could not be
read" for a 42-megapixel, perfectly-decodable PNG). Then implemented
the fix and watched it pass. Added a new sibling test,
`TestImport_BkpCorruptImageWarnsAndFallsBackToPlaceholder`, with a
genuinely-corrupt (non-image) byte fixture, to pin the unchanged case
so the two messages can't drift back onto each other.

## Independent review

Dispatched to a fresh-context Sonnet subagent in an isolated git
worktree (`isolation: "worktree"`), briefed with the issue, the
established `catalog/handlers.go` precedent, and an explicit instruction
to perform its own revert-then-restore TDD verification rather than
trust the implementer's claim.

**Verdict: SAFE TO MERGE, no blocking issues.**

What it verified, independently:
- The `errors.Is` branch matches the manual-upload precedent exactly;
  `errors` was already imported, no new import needed.
- All four locale files are valid JSON, keys match, and it checked the
  Arabic/Farsi/Turkish translations semantically (not just presence) —
  confirmed each reads as "the item's photo was too large to import —
  imported without a photo" in its language.
- `bash scripts/ci/guard-i18n.sh` passes.
- Read the surrounding code to confirm the two recurring bug classes
  this pipeline watches for weren't introduced: the success path still
  calls `os.MkdirAll` before `os.Create`, and still uses
  `paths.Data(...)`, not a cwd-relative path — neither line was touched
  by this diff.
- Independently reverted the fix, re-ran the tests, confirmed
  `TestImport_BkpOversizedImageWarnsAndFallsBackToPlaceholder` fails
  with the expected symptom, confirmed the new corrupt-file test still
  passes (proving it doesn't accidentally depend on the fix), then
  restored the fix and confirmed both pass.
- Ran the full gate itself: `gofmt`, `go build ./...`, `go vet ./...`,
  `go test ./internal/pages/...`, `guard-data-access.sh`,
  `guard-i18n.sh`, `golangci-lint run ./internal/pages/...` — all clean.
- Confirmed `internal/pages/catalog/handlers.go` (the manual-upload
  handlers, explicitly a non-goal for this card) is untouched.
- No secret-shaped literals or real client/shop names in the diff.

## Verified beyond automated tests

- Full `go test ./...` (whole repo, not just the touched package) run
  personally before handing off to review: all packages green.
- `golangci-lint run ./internal/pages/...`: 0 issues.
- `bash scripts/ci/guard-help-topics.sh`: passes — this change doesn't
  add/remove a page or route, and `web/help/en/catalog.md`'s existing
  prose ("if a referenced photo can't be found... imported without it")
  stays accurate at the level of detail it already describes; no manual
  update needed for a warning-message wording change.

## Safe-to-merge verdict

Yes. Small, well-scoped, TDD'd, independently reviewed with its own
verification (not a rubber stamp), full gate green, no deferred items.

## Explicitly deferred / out of scope (per the issue's own non-goals)

- Does not change the `.bkp` commit's best-effort/never-fail-the-row
  behavior — the row still imports with the placeholder icon either
  way, only the warning text differs.
- Does not touch the interactive upload handlers (`catalog/handlers.go`)
  — already correct since ut-docs#1416.
- The stale-doc finding this cycle also turned up (SKILL.md/label text
  claiming the `:24` lane is "currently disabled" when it's actually
  `enabled: true`) is unrelated to this change and filed separately as
  ut-docs#1633.
