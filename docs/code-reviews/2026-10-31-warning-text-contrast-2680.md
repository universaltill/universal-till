# Review: warning text contrast on the warning tint (ut-docs#2680)

Date: 2026-10-31. Branch `fix/2680-warning-text-contrast`. Lane `lane:cloud-41`.
Built by a Sonnet Dev subagent; reviewed by an independent Opus subagent
(fresh context, own worktree); Tester run by the orchestrator.

## What shipped

- `web/public/app.css`: new token `--warning-text: #92400e` in `:root`.
  `.tag.warn`, `.journal-replica-notice` and `.sync-banner` draw their text
  with it instead of `--warning`. `.tag.warn` keeps the `--warning` border.
- `themes/dark.css`: `--warning-text: var(--warning)`, the bright amber.
  `themes/monochrome.css`: `#000000`. Other light themes use the default.
- **Found in Tester's run, already broken on `main`:** the comment above
  `.sync-banner` contained `TestSyncChip_*/TestFiscalChip_*`. That `*/`
  ended the comment early, so the browser parsed the rest of the comment
  plus the `.sync-banner { … }` block as one invalid rule and dropped it.
  Walking Chromium's CSSOM showed only `.sync-banner a` present. Replica
  sync banners (catalog, inventory, vouchers) had no tint and no warning
  colour at all. The comment is reworded.
- Tests (`internal/pages/themes_test.go`):
  - `TestWarningTextContrastOnWarningTint`: for the default theme and every
    `web/public/themes/*.css`, resolves `--bg`, `--surface`, `--warning`,
    `--warning-tint` and `--warning-text` (app.css `:root` overridden by
    the theme, following `var()` chains). It composites the tint over
    `--bg` and over `--surface` and requires >= 4.5:1. It also asserts the
    three rules use `var(--warning-text)`.
  - `TestShippedCSS_NoCommentClosedEarly`: walks every embedded
    `web/public/**/*.css` (not `vendor/`) and strips well-formed comments.
    A leftover `*/` (a comment closed early) or `/*` (a comment never
    closed) fails.
  - One existing literal in `TestSemanticTintBackgrounds_MonochromeIsColourless`
    is updated.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| T1 | high (pre-existing) | `.sync-banner` rule dropped by an early-closed comment; 742c262's substring test was a false pass for it | Fixed in this branch + guard test |
| F1 | medium | A third-party dark plugin theme that overrides `--bg`/`--surface` but not `--warning-text` gets dark brown on a dark tint (~2.2:1) | Documented in ut-docs `reference/ux-guidelines.md` (same session). Plugin themes are not checked by the test, which is accepted |
| F2 | low | Comment guard missed an unterminated `/*` | Fixed: a leftover `/*` fails too (verified with a planted unterminated comment in slate.css) |
| F3 | low | Comment guard false-positive on `content: "*/"` | Accepted; documented in the test's doc comment. Nothing ships like that |
| F4 | low | Theme list hard-coded; `Fatalf` stopped at first broken theme in random map order | Fixed: themes read from the embedded `public/themes` dir, sorted, `Errorf` + continue |
| F5 | low, out of scope | `.import-system-secrets dd[data-secret-state="not-set"]`: `--warning` text on plain `--surface`, ~3.2:1 | Filed ut-docs#5228 |

## Verified beyond unit tests

- **TDD:** Dev saw the new test fail before the CSS change. The reviewer
  re-verified in its own worktree:
  - CSS reverted to `main`: fails (token unresolved, rule substrings missing).
  - `--warning-text: #d97706`: fails in every light theme (2.58:1 / 2.80:1).
  - dark override removed: fails (2.32:1 / 2.17:1).
  - The comment guard fails with the old comment restored.
- **Real browser (Chromium via Playwright)** against a running till
  serving the built binary's embedded CSS:
  - Measured computed colour on the computed tint for all three classes ×
    7 themes. All 21 pairs are >= 4.5:1: light themes 5.74–5.90, dark
    5.16, monochrome 17.62.
  - After the comment fix, CSSOM contains the `.sync-banner` rule.
  - Screenshots looked at, default and dark, 1024×600: amber-brown text on
    the light tint, bright amber on the dark tint, nothing clipped.
- **Not looked at:** the real replica pages (the banners only render on a
  replica till), RTL, or real touch hardware. This is a colour-only change
  with no layout or interaction change. The banner does gain its intended
  padding and radius back.

## Gate

gofmt, go build, go vet, golangci-lint, `go test ./...` (see PR checks), plus
the guards `guard-rtl-logical-css`, `guard-i18n`, `guard-competitor-naming`,
`guard-compliance-claims`, `guard-emoji-font`, `guard-no-showmodal` and
`guard-mail-addresses`. In the Dev run, `internal/plugins`
`TestSchedule_PausesDuringSaleBurstThenFires` hit the 600s package timeout
under full parallel load. It passes in isolation (0.3s), and this branch
doesn't touch that code.

## Verdict

Safe to merge.
