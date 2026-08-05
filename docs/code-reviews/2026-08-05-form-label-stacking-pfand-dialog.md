# Code review — wrapped-control labels stack (ut-docs#300)

**Date:** 2026-08-05
**Card:** universaltill/ut-docs#300 (p1, `complexity:medium`)
**Branch:** `fix/300-stacked-form-labels`
**Reviewer:** independent subagent, Opus (the implementation ran on Opus inline —
the diff was ~20 lines across files already fully in context, so briefing a
separate Sonnet dev agent would have cost more than the work; the review model
is unchanged from the medium-tier routing).

## What shipped

The deposit-refund (Pfandrückgabe) payout dialog rendered its two fields as one
broken row: "Amount (£)" and its input on one line, "Manager PIN" to the *right*
of that input, and the PIN's own input wrapped below. On a form that pays cash
out of the drawer, it was not obvious which input belonged to which label.

The cause was not this dialog's markup. `web/public/app.css` styled `label` with
no `display`, so a `<label>` wrapping its own control stayed inline. Four forms
each declared their own stacking rule (`.catalog-form`, `.split-tender-form`,
`.translations-controls`, `.setup-card`); stacking was therefore accidental and
opt-in, and every form that never got such a rule inherited the bug. A second
live instance was found while scoping: the change-Manager-PIN form
(`web/ui/pages/pin.html`), three password fields in a plain `.card`.

So the fix is the default, not the dialog:

```css
label:has(> input:where(:not([type="checkbox"]):not([type="radio"]))),
label:has(> select),
label:has(> textarea) { display: flex; flex-direction: column; gap: .2rem; }
```

plus a reusable `.stacked-form` for field rhythm, applied to the payout dialog
and the change-PIN form.

The card explicitly forbade fixing this with a `.modifier-modal label` rule,
which would have out-ranked `.modifier-option` (0,1,0) and flipped every
ADR-0020 item-customization option into a column. Excluding checkbox/radio *in
the selector* satisfies that **structurally** rather than by specificity:
`.modifier-option` wraps a radio/checkbox, so the rule cannot match it at all,
and no future reordering can change that. Confirmed by the reviewer against both
`modifier_picker.html` and `self_order_modifier_picker.html` — no variant wraps
anything else.

## What the independent review found

**1. BLOCKER — three manager-facing Settings rows silently stacked and centred.**
`.set-row` is `display:flex; align-items:center` with **no `flex-direction`** —
structurally the identical trap the card warned about for `.modifier-option`,
which I guarded, and this one, which I missed. Specificity is irrelevant here:
the cascade is per-property, so a container that never declares
`flex-direction` cannot out-rank a rule that does, whatever its selector weight.
Three `<label class="set-row">` wrap a `<select>` (settings.html:89, 101, 117 —
on-screen keyboard, device profile, default payment method); each became a
centred vertical stack, 54px → 134px, while the `<div class="set-row">` beside
them stayed a row, leaving the card internally inconsistent. No test covered it:
`pages.spec.ts` only asserts `/settings` contains the text "System Settings".

Verified independently before fixing (the failure is real and reproducible),
fixed by making `.set-row` declare `flex-direction: row` explicitly, and
**confirmed by eye** on the rendered Display card, not only by assertion.

**2. SHOULD-FIX — the specificity rationale in my comment was wrong, with two
live consequences.** `:has()` takes the specificity of its *most specific
argument*, so `label:has(> input:not([type=checkbox]):not([type=radio]))` scored
(0,2,2) — each `:not([attr])` contributes (0,1,0) — and therefore **outranked**
all four scoped rules, while the sibling `label:has(> select)` branch at (0,0,2)
did not. Measured effects: `.setup-card label` lost its `display:block` on
`/setup` and `/login`, and `.translations-controls label` lost its `gap` for the
search input but kept it for the select, so two adjacent fields in one control
bar had different gaps.

Fixed properly rather than by correcting the prose: wrapping the `:not()` chain
in **`:where()`** (which contributes zero specificity) puts all three branches at
a uniform (0,0,2), below every scoped rule, which is what the comment claimed all
along. The comment now explains why `:where()` is load-bearing and must not be
"simplified" away.

**3. SHOULD-FIX — `expectStacked` asserted nothing on an empty list.** It was two
`for` loops with no length check; since `fieldGeometry` drops any label without a
wrapped control, a refactor to `<label for=x>…</label><input id=x>` would have
emptied it and passed silently. Given this spec exists *because* of a silent
false pass, that mattered. A length guard was added to the shared helper, so
every caller gets it.

**4. NIT — modifier-admin Min/Max rows grew ~25px taller** (`catalog_variants.html`
Min/Max labels wrap a number input inside a compact flex-wrap row). Fixed with a
scoped `flex-direction: row`.

**5. NIT — first use of `:has()` in this repo, no documented browser floor.**
Noted in the comment: Chromium ≥105 / Safari ≥15.4 / Firefox ≥121; the kiosk
Chromium and Android WebView are well past it, and below the floor the rule is
dropped wholesale so the form degrades to the old inline layout rather than to
something worse.

**6. NIT — accepted, not fixed:** some previously-inline controls on `/import`,
`/receipt-designer` and `/tills` now stretch to full container width. That is the
intended consequence of the fix on forms that were broken-inline before, and
matches `.catalog-form`'s existing look. Called out here rather than silently
absorbed; the receipt-designer header fields (`maxlength="42"`) are the one place
a future `max-inline-size` might read better, and were eyeballed as acceptable.

## A false-pass this pipeline caught in its own new test

The first draft of the German test drove `?lang=de` and asserted
`toContainText('Manager')`. It passed — on **English**. German ships as a plugin
language pack (`ut-plugin-language-de`, ut-docs#292), not a core locale (the
shipped core locales are en/ar/fa/tr), so a till without that plugin resolves
`de → en`. The assertion was satisfied by the English string "Manager PIN" and
proved nothing.

This was found by *looking at the screenshot*, which is precisely the gap
ut-docs#301 was opened about. The test now substitutes the real German terms into
the live DOM and guards the substitution itself, which is what the acceptance
criterion was actually about: the layout tolerating the longest translation.

## Verified beyond automated tests

- **Screenshots read, not merely captured**, for the payout dialog at 1280×800
  and at the 10-inch kiosk 1024×600, in English, with the real German terms
  (`Pfandrückgabe`, `Manager-PIN`), and in **fa/RTL** — where the labels and
  their inputs correctly share an inline-*start* (right) edge and the buttons
  mirror, confirming logical properties throughout.
- **The surfaces the global rule could regress**, since the rule is not scoped to
  the reported dialog: `/catalog`, `/settings`, `/receipt-designer`, `/inventory`.
  Checkbox and radio toggle rows stayed horizontal on every one. Only five
  classes are ever applied to a `<label>` in this codebase
  (`chip-add-primary`, `field-checks`, `modifier-option`, `offline-toggle`,
  `set-row`); four wrap checkboxes and are excluded by the selector, and
  `.set-row` is finding 1.
- **`/pin` driven with a real authenticated session**, which the default e2e
  project cannot reach (auth is off there, so `GET /pin` only redirects to
  `/login`); its geometric assertion therefore lives in `login.spec.ts`, the one
  spec with a genuine logged-in operator.
- **Theme:** re-rendered under a non-default curated theme. Worth stating plainly
  — the card asked for a dark-theme check and **no dark theme ships**: all four
  curated themes (`amber`, `fresh`, `monarch`, `slate`) are light. The change is
  colour-free, and no theme stylesheet touches `label`.
- An apparent "invisible inputs" defect on `/pin` was measured rather than
  assumed: all three are real 41.6×392 boxes with a very low-contrast
  `rgb(226,232,240)` border. Pre-existing, app-wide, colour-only — filed as
  ut-docs#305 rather than folded into a p1 layout fix.

## TDD, re-verified independently

Six tests were written first and confirmed failing against the unfixed
stylesheet, with the right message
(`"pfand-amount" must start BELOW its own label text, not beside it`). The
reviewer re-ran that itself — stashing the three implementation files gave
exactly **6 failed / 36 passed**, every failure for the correct reason, none
incidental. The Settings regression test added for finding 1 was likewise
confirmed to fail with `.set-row`'s `flex-direction` removed and pass with it
restored.

## Gate

`go build ./...`, `go vet ./...`, `guard-data-access.sh`, `guard-i18n.sh`,
`go test ./... -race`, and the full Playwright suite across **both** projects:
**45 passed**. No Go files in this diff, so the two recurring bug classes
(missing `os.MkdirAll`, cwd-relative path instead of `paths.Data(...)`) have no
surface here — confirmed rather than assumed. No secrets; test data is generic.

## Verdict

Safe to merge. The blocker and both should-fix findings are fixed and
re-verified; the remaining items are documented nits, one deliberately accepted
and one deferred to ut-docs#305.
