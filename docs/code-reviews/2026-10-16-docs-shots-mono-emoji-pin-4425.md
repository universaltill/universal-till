# Review: docs-shots pins monospace and emoji fonts (ut-docs#4425)

Date: 2026-10-16 · Branch: `chore/4425-docs-shots-mono-font` · Follow-up to ut-docs#4407.
Author: the `:24` build lane (Opus 5.5). Reviewer: an independent Fable subagent (a different model).

## What shipped

- `e2e/fontconfig/fonts.conf` aliases `monospace` (and, as a fallback, `ui-monospace`) to
  DejaVu Sans Mono.
- `e2e/tests-docs/lib.js`: `probePlatformFonts` adds two monospace probes
  (`ui-monospace, monospace` and `monospace`) and an emoji probe that inherits the body stack
  (🐞🧾🔒, all three shown in the UI). `fontPinProblem` fails unless monospace resolves to
  exactly DejaVu Sans Mono and emoji to exactly Noto Color Emoji. `PINNED_FONTS` gains
  `mono` and `emoji`. The install hint now names `fonts-noto-color-emoji`.
- `docs-shots-determinism.yml` installs `fonts-noto-color-emoji`.
- `e2e/README.md`: the "accepted gap" note is replaced. Symbol glyphs Inter lacks are still
  unchecked; the README says so.

Emoji decision (from the card): many screenshotted pages show emoji (nav 🐞, ☰, receipts 🧾), so
they need a pin. app.css's body stack already names Noto Color Emoji, so the pin is the
check plus the package. No fontconfig change was needed, and none was made: reordering
fallback would have moved symbol glyphs such as ⚠ and changed screenshots.

## Verification

- A full `docs-shots.sh` run passed 120/120. It changed no PNG and no manifest (a clean
  `git status` afterwards). Container Chromium: 141, against a pin of 149, with the harness's
  own warning printed. The run committed nothing.
- Negative cases:
  - With the monospace alias pointed at Liberation Mono, the harness failed with
    `monospace text resolved to [Liberation Mono], expected exactly [DejaVu Sans Mono]`.
  - With NotoColorEmoji rejected through `<rejectfont>`, it failed with
    `emoji resolved to [Unifont Upper], expected exactly [Noto Color Emoji]`.
  - The shipped config was restored after each case.
- What the reviewer checked:
  - `xmllint` passes.
  - A standalone CDP probe confirmed every resolution.
  - VS16 and ZWJ emoji sequences still report a single family, so there is no false fail.
  - `fontPinProblem` returns the right result on synthetic inputs.
  - The only caller is `docs-shots.spec.ts`, which takes the new return shape unchanged.
  - `fonts-noto-color-emoji` exists on noble (ubuntu-latest).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Minor | The README said ✓ and ⚠ come from DejaVu Sans. Probed, both come from Inter; only ☰ falls back. | Fixed: the wording now says "glyphs Inter lacks (e.g. ☰)". |
| 2 | Nit | The `ui-monospace` alias changes nothing for current CSS, which always pairs it with `monospace`. | Kept as belt-and-braces; the comment says so. |
| 3 | Nit | `fc-match ui-monospace` without escaping misreads the hyphen as a size and reports Inter. | Added to the README as a half-line. |

## Verdict

Safe to merge. No product code, templates or screenshots changed. Deferred: nothing.
