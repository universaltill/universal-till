# Code review: remove dead `help.feat.*`/`help.guide.*`/`help.features.*` locale keys

**Card:** universaltill/ut-docs#352
**Date:** 2026-08-23
**Author:** Farshid Mirza (autonomous pipeline cycle)
**Reviewer:** independent fresh-context Sonnet subagent (complexity:easy tier)

## Summary

ut-docs#325 (the two-pane `/help` shell) migrated the old `/help` accordion
content into Markdown topics under `web/help/en/*.md`, but deliberately left
the original `help.feat.*` (16 topics), `help.guide.*`, and `help.features.*`
locale keys in place across all four `web/locales/*.json` files rather than
touching every locale file in that already-large card. This change removes
the now-dead keys.

## BA finding — the issue's premise was incomplete

The card as filed said "no template changes needed since nothing references
these keys anymore," and asked for wholesale removal of `help.feat.*`,
`help.guide.*`, and `help.features.*`. Verification before touching anything
found this was **not quite true**: `help.guide.title` and `help.guide.intro`
are still live — rendered by `web/ui/partials/help_topic.html`'s
no-topic-selected landing view (the `/help` page before a topic is picked).
Removing them wholesale would have blanked that page's heading and intro
paragraph, a real regression the issue's own acceptance criteria (verify via
grep before removing) exists to catch.

Corrected scope, verified per-key via grep across `.go`/`.html`/`.js`:

| pattern | total keys (×4 locales) | removed | kept | why |
|---|---|---|---|---|
| `help.feat.*` (16 topics) | 75 | 75 | 0 | zero live references anywhere |
| `help.features.*` | 2 | 2 | 0 | zero live references anywhere |
| `help.guide.*` | 34 | 32 | 2 (`title`, `intro`) | those two render in `help_topic.html`'s landing view; the other 32 (login/sell/pay/receipt/hold/noscan/shift/language/trouble step-by-step keys) have zero live references |

109 keys removed per locale × 4 locales (en, fa, ar, tr) = 436 lines deleted,
pure deletions, no other changes.

## What was verified

- Every removed key grepped against `.go`/`.html`/`.js` (excluding
  `web/locales/`) — zero live references. The only other hit,
  `help.feat.<id>.s1/.s2/.s3` in a code comment in `internal/manual/manual.go`,
  is a comment, not a render.
- The two kept keys (`help.guide.title`, `help.guide.intro`) confirmed live in
  `web/ui/partials/help_topic.html` and left byte-identical in all 4 locales.
- All 4 locale files remain valid JSON and stay in exact key-set parity with
  each other (1510 keys each, post-removal).
- `scripts/ci/guard-i18n.sh` — pass (1154 template keys resolve; locales match
  en.json).
- `scripts/ci/guard-help-topics.sh` — pass.
- `gofmt -l .` — clean.
- `go build ./...` — clean.
- `go test ./...` — all packages pass.
- Diff scope — only the 4 locale JSON files touched; pure deletions, no
  reordering/reformatting of surviving keys (verified non-ASCII fa/ar strings
  untouched).

## Independent review

A fresh-context Sonnet subagent (no prior context on this change) re-derived
all of the above independently: re-grepped every removed key against the
whole repo, diffed the exact key sets across all 4 locales, re-ran both
guards plus build/test, and confirmed the two kept keys are genuinely live.
**Verdict: PASS, no findings.**

## Non-goals (unchanged from the card)

- No template changes — `help_topic.html` already worked correctly with the
  keys as they exist post-removal; nothing needed to change there.
- Not a blocker for ut-docs#325 (cosmetic/hygiene, no data-loss/security/money
  involved).
