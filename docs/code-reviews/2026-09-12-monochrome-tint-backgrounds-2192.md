# Code review: theme-overridable tint tokens for semantic notice/tag backgrounds

**Card:** universaltill/ut-docs#2192
**Branch:** `fix/2192-monochrome-tint-backgrounds`
**Complexity:** easy — Dev inline (Sonnet), review at Sonnet (independent
fresh-context subagent, no shared reasoning with the implementer)

## What shipped

Follow-up from `universaltill/ut-docs#2176`'s own review: the Monochrome
theme ("no colour accent carries meaning on its own") still showed a
faint green/red/amber wash behind otherwise-colourless chrome, because
several `web/public/app.css` rules painted a semantic-tint **background**
as a literal `rgba(...)` rather than through any CSS custom property, so
no theme could reach them.

- Added `--success-tint` / `--danger-tint` / `--warning-tint` to
  `app.css`'s base `:root`, defaulting to the exact literal values every
  affected rule already used (`rgba(22, 163, 74, .12)` /
  `rgba(220, 38, 38, .1)` / `rgba(217, 119, 6, .12)`).
- Pointed 8 rules at the matching token instead of a literal rgba — **4
  more than the card's own inventory of 4** (`.tag.warn`,
  `.pos-notice.success`, `.pos-notice.error`, `.sync-banner`): grepping
  the three literal values across `app.css` also found
  `.journal-replica-notice`, `tr.row-warn td`, `.notice-block-warn`, and
  `.notice-block-success` using the identical literals as backgrounds.
  Missing these would have left the catalogue-import warning table, the
  block-level notices, and the journal cross-till replica banner still
  showing colour under Monochrome.
- `monochrome.css` sets all three tokens to a neutral grayscale wash
  (`rgba(0, 0, 0, .08)`) — no reason for one to read "heavier" than
  another in a theme that already erases the hue distinction between
  them.
- `dark.css`: **no change.** It never overrode `--success`/`--warning`
  (only `--danger`, for text contrast, per its own #2176 review comment),
  so leaving the new tint tokens unset there is byte-for-byte identical
  to what it rendered before this diff.
- **Explicit non-goal**, matching #2176's own "not a general
  design-system refresh" scope: border colors reusing the same three
  literal rgba values at `.35` alpha (`.pos-notice`, `.notice-block-*`)
  are left untouched. A follow-up card is being filed for that
  separately (borders are a thinner visual element than a background
  wash, and Monochrome's `--warning`/`--danger`/`--success` ink tokens
  already resolve those borders to black wherever a rule reads the ink
  token directly, e.g. `.tag.warn`'s `border-color: var(--warning)`).

New regression test: `TestSemanticTintBackgrounds_MonochromeIsColourless`
(`internal/pages/themes_test.go`) — reads the real embedded `app.css` and
each theme's CSS via `registerStatic`/`registerThemes` (not a copy of the
CSS), asserts none of the 8 rules still paints a hardcoded tint
background, asserts monochrome's three tokens are grayscale, and asserts
every other built-in theme (amber/fresh/monarch/slate/dark) does **not**
define the new tokens — i.e. genuinely inherits `app.css`'s default
rather than merely looking unchanged by coincidence.

## Independent review — findings

Spawned a fresh-context Sonnet subagent (`complexity:easy` routing — a
clean-context instance rather than a different model, per the model-
routing rubric for this tier) in an isolated git worktree, with
instructions to read the diff cold, check it against `CLAUDE.md`, run the
build/vet/test/lint gate itself, independently re-verify the TDD claim by
reverting one of the 8 substitutions and confirming the test actually
fails, verify the literal-value math byte-for-byte against `main`'s prior
content, and judge whether skipping the e2e Playwright run was
defensible.

**Verdict: safe to merge, no code/test defects found.**

| # | Severity | Finding | Resolution |
|---|---|---|---|
| 1 | Process (not a code defect) | This review record didn't exist yet at the time the independent review ran — `CLAUDE.md`'s "before committing" gate requires one before merge. | This file. Written and committed before merge, per the same gate. |
| — | Verified, not a bug | `.statusbar .sb-power` (`app.css:3333`) reads `var(--warning)` directly (not a tint token) for its text color, so it renders dark-brown-on-black under Monochrome (`--warning:#000000`). Pre-existing, untouched by this diff — a `#2176`-lineage observation, out of scope for `#2192`'s own review. | Not fixed here (not this card's regression). Left as a note for whoever next touches that selector. |

The independent review re-derived the literal-rgba inventory itself (a
repo-wide grep for all three literal values, not a re-read of this
record's own claim) and confirmed all 8 affected rules used exactly the
same three literals before this diff, that the new tokens' defaults
reproduce them exactly, and that no ninth instance was missed anywhere in
the tree (HTML, Android, other CSS).

## Verified beyond automated tests

- **TDD claim independently re-verified** by the reviewer, in its own
  isolated worktree: reverted `.sync-banner`'s background back to the
  literal `rgba(217, 119, 6, .12)`, re-ran the new test — failed, naming
  the exact offending selector and value. Restored the file exactly,
  confirmed the diff was clean, re-ran — passed.
- **Driven-run visual verification** (by the implementer, before
  handoff): built and ran the real server, fetched the actual served
  `/public/app.css` and `/themes/{monarch,monochrome}.css`, and rendered
  all 8 affected elements (`.pos-notice.success/.error`, `.tag.warn`,
  `.journal-replica-notice`, a `tr.row-warn` row, `.notice-block-warn/
  -success`, `.sync-banner`) in a real headless-Chromium page under both
  themes. Default theme: green/red/amber washes, pixel-unchanged.
  Monochrome: all 8 now a neutral grayscale wash; text, icons, and
  borders (deliberately out of scope) still carry their existing colors.
  Also checked at 360px width — no layout regression (color-only change,
  no markup/geometry touched).
- Full `go build ./...`, `go vet ./...`, `go test ./...` (whole suite),
  `golangci-lint run ./...` (0 issues), `gofmt -l .` (clean) — run by
  both the implementer and, independently, the reviewer in its own
  worktree.
- Every CI-blocking guard applicable to this diff, run locally and green:
  `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh`, plus the reviewer's own additional pass over
  `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-page-http-error.sh`,
  `guard-webkit-version.sh`, `guard-kiosk-launch-flags.sh`,
  `guard-android-status-address.sh`, `guard-android-i18n.sh`,
  `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `guard-e2e-fixtures-import.sh`,
  `check-brand-assets.sh`, `guard-makefile-version.sh`.
  `guard-shellcheck-version.sh` not runnable (no `shellcheck` binary in
  this sandbox) — unrelated to this diff, which touches no shell scripts.
- `guard-docs-shots.sh`: this diff touches `web/public/app.css` (in the
  guarded surface fileset), so it failed freshness on the raw literal
  file hash. Independently confirmed no captured screenshot's rendered
  pixels change: `e2e/tests-docs/docs-shots.spec.ts` never switches
  themes, so every one of the 31 routed topics × 4 locales is captured
  under the seeded default theme (`monarch`), where every new token
  resolves to its unchanged literal default; Monochrome — the only theme
  whose rendering visibly changes — appears in zero manual screenshots.
  Used the documented escape hatch
  (`scripts/ci/update-docs-shots-surface-hash.sh`), which recomputes and
  writes only the `surface_sha256` field (verified: the resulting
  `manifest.json` diff touches that one field only), with a
  `Docs-Shots-Unchanged: true` commit trailer — rather than a full
  104-screenshot regeneration that would conflict with every other open
  PR by construction.
- **No e2e Playwright run** — npm deps aren't installed in this sandbox.
  Independently confirmed defensible rather than assumed: a repo-wide
  grep of `e2e/` for the three literal tint values and the three new
  token names returned zero hits, so no existing spec asserts on these
  colors or could regress from this change; the diff touches zero
  markup/JS/class-lists.
- A merge conflict appeared mid-cycle (`universaltill/universal-till#1137`,
  ut-docs#2147's own PR, merged to `main` while this branch was open) —
  resolved by merging `main` into this branch. Only `web/help/img/
  manifest.json`'s `surface_sha256` field conflicted (a generated field);
  resolved by recomputing it fresh against the merged tree with the same
  escape-hatch script, then re-ran the full local gate
  (`gofmt`/`build`/`vet`/`go test ./internal/pages/...`) again post-merge
  — all green.

## Deferred / follow-up candidates (not fixed here, filed separately)

- Border colors on `.pos-notice`/`.notice-block-*` reusing the same
  three literal rgba values at `.35` alpha — same "not a general
  design-system refresh" reasoning #2176 used to defer the background
  gap this card fixes. Filed as a new Backlog card.
- `.statusbar .sb-power`'s `color: var(--warning)` renders dark-brown
  text on Monochrome's black background (contrast concern, not a
  regression from this diff) — noted above, left for a future card if it
  turns out to matter (that selector's actual visibility/reachability
  under Monochrome wasn't re-derived here).

## Verdict

**Safe to merge.** No correctness, security, i18n, money, offline-first,
or data-access issues found by either pass. The only real gap the
independent review found — this review record's absence — is closed by
this file.
