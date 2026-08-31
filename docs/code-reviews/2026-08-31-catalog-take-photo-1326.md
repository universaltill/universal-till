# Code review — Catalog item-image "take a photo" option (ut-docs#1326)

- **Date:** 2026-08-31
- **Branch:** `feat/1326-catalog-take-photo`
- **Reviewer:** independent fresh-context Sonnet subagent (complexity:easy →
  Sonnet reviews Sonnet's own-card work, per the pipeline's model-routing
  table — an isolated worktree, never saw the Dev reasoning), findings
  triaged and fixed by the orchestrating session.
- **Verdict:** Safe to merge.

## Context

ut-docs#1326: the catalog item-image field only offered a plain file-picker
input, so getting a photo onto an item from a tablet's own camera depended
on whatever generic chooser the OS/browser happened to surface (not
guaranteed to offer a direct camera option). Product owner asked for a
dedicated "take a photo" affordance alongside "choose a file", matching
SumUp's item-editor pattern.

## What this change does

`web/ui/pages/catalog.html`'s "Item image" panel now shows two buttons —
**Take a Photo** (opens the rear camera directly via
`capture="environment"` on a hidden file input) and **Choose File** (the
original file-picker input, unchanged behavior) — both feeding one
canonical `<input name="image" id="image-file">` that the existing
upload/validation JS and the multipart submit already key off unchanged.
The camera input's picked file is copied into the canonical field via
`DataTransfer` on `change`. No Go/backend changes — `/api/catalog/item/image`
already accepts any valid image regardless of how the browser obtained it.

New i18n keys `catalog.take_photo`/`catalog.choose_file` added to all four
core locales (en/ar/fa/tr) with real, natural translations (not
English-copied or machine-garbled). `web/help/{en,ar,fa,tr}/catalog.md`
each gained one sentence on the new option; screenshots regenerated
(`make docs-shots`) — the collapsed "Item image" panel isn't visible in
the captured viewport by default, so the PNGs are pixel-identical; only
the tracked surface/topic hashes in `manifest.json` changed, as expected.
`ut-plugin-language-{de,es}` needs its own follow-up for the two new keys
— `lang-pack-drift` will flag this as advisory-only on this PR (it only
touches `en.json`, not `main` directly) and is not a merge blocker.

## Independent review findings

**F1 (should-fix, keyboard-accessibility regression) — fixed.** The first
draft triggered each hidden file input via a bare `<label class="btn
secondary" for="...">`. The reviewer proved empirically (a real
Playwright script pressing Tab from a known point) that neither trigger
was reachable via keyboard Tab at all, and a `<label for>` has no default
Enter/Space activation in any browser — the `hidden` attribute drops the
underlying input out of tab order regardless of its own `tabindex`, and
a label isn't a substitute. This was a genuine regression against the
single visible, natively-focusable file input it replaced, and no
existing pattern in the codebase used "hidden input + label trigger" for
this reason.

**Fix:** swapped both `<label for>` triggers for real `<button
type="button">` elements that call `.click()` on the corresponding
hidden file input. This is the standard, less error-prone pattern the
reviewer recommended — native button focus, Tab-reachability, and
Enter/Space activation come for free, and `.click()` on a hidden file
input still opens its native picker in every target browser.

Added a dedicated regression test (`take-photo/choose-file triggers are
real buttons, keyboard-activatable`) asserting both triggers are actual
`<button>` elements and that keyboard focus + Enter on the camera trigger
opens a native file chooser (a `<label>` would not fire that event this
way). TDD-verified: reverted to the pre-fix label markup, confirmed this
new test fails (`element(s) not found` / no filechooser event, matching
the reviewer's own reproduction), restored the button fix, confirmed all
three tests in the spec pass.

## What else the independent review checked (all clean)

- i18n key parity across en/ar/fa/tr (`guard-i18n.sh`); no hardcoded
  prose — the new `#image-file-name` echo is the browser-supplied
  `file.name`, not translatable UI text, correctly left out of `T`.
- No SQL/money/repository-pattern surface at all (confirmed via
  `guard-data-access.sh`/`guard-kiosk-engine.sh` — this is a pure
  template + inline-JS change).
- `DataTransfer` copy correctness: the camera input's `change` listener
  copies its file into the canonical input; the submit handler reads the
  canonical input directly and needed no changes.
- Dropping `required` from both (now-hidden) inputs is safe — the submit
  handler's own pre-existing `if (!f) { ... 'choose an image' ...}` check
  covers the same validation.
- No RTL/logical-CSS-property issue (`.field-pair` — grid, direction-
  agnostic — is an existing pattern already used 5× in this file); no
  unstyled/broken buttons (reuses `.btn`/`.btn.secondary`).
- e2e test's new item (Apple Juice 1L / itm006 / barcode
  5000000000067) doesn't collide with any other spec's shared
  server-side state (grepped all ~60 spec files).
- Deliberately does **not** assert on the resulting `<img>`'s
  `naturalWidth`/`complete` — that specific assertion is a known,
  separately-tracked, sandbox-environment-specific failure (ut-docs#1362),
  unrelated to this change.
- All four `catalog.md` locale translations read as real, natural
  human translations of the new step, not garbled or English-copied.

## Verification

| Check | Result |
|---|---|
| `gofmt -l .` / `go build ./...` / `go vet ./...` | empty / pass / pass |
| `go test ./...` (full package suite) | pass, all packages |
| `guard-i18n.sh` / `guard-help-topics.sh` / `guard-compliance-claims.sh` | pass / pass / pass |
| `guard-docs-shots.sh` (after regenerating post-fix) | pass |
| `guard-data-access.sh` / `guard-kiosk-engine.sh` | pass / pass |
| e2e full `--project=default` (249 specs) | all pass |
| e2e `catalog-image-to-till.spec.ts` in isolation (3 specs) | all pass |

TDD claims independently re-verified twice: (1) the original "camera
input reaches the server" test, reverting only `catalog.html` — fails
with `element(s) not found` for `#image-file-camera`, restoring returns
it to green; (2) the keyboard-activatable regression test above, same
revert/restore pattern against the label-vs-button markup.

## Deferred / out of scope

- AI photo-to-catalog import (full menu photograph → auto-priced items) —
  explicitly out of scope per the issue body; would need a self-hosted
  vision model (Ollama), not this card.
- `ut-plugin-language-{de,es}` translation follow-up for the two new keys
  — tracked via the standing `lang-pack-drift` advisory, not a new card
  (the mechanism already exists for exactly this gap).

---
_Generated by [Claude Code](https://claude.ai/code)_
