# Independent review — sale screen nav rail (ut-docs#1332), 2026-08-31

**Branch:** `sale-screen-nav-rail` @ `0798df44` · **Base:** `main`
(merge-base `ba91df98`) · **Reviewer:** a different model (Opus) from the one
that implemented the change (Sonnet), per this repo's standing `reviewer`
practice.

This is a **second** document alongside the implementer's own
`2026-08-31-sale-screen-nav-rail-1332.md`, not a replacement for it. That
document is accurate and unusually honest about what it had and had not
verified; this one records what an independent pass found on top of it.

Worked in an isolated worktree off `origin/sale-screen-nav-rail`, never in
the shared checkout (an autonomous pipeline is using that concurrently).

## What I checked

- Full diff `git diff origin/main...HEAD` — 17 real source files, the rest
  regenerated screenshots. Base confirmed as `main`.
- The seven specific questions in the review brief (VAT/tax correctness,
  RTL, accessibility, the `<=480px` fallback cascade, ADR-0054's soft-gate,
  `hx-sync`, and the standard build/test gate).
- **Ran the app** (own instance on port 8099, to avoid the shared e2e
  ports) and drove it with a real browser at 1280×800, 1024×600 and
  360×640, in LTR and RTL (`?lang=fa`), with and without dining tables
  configured. Screenshots were looked at, and every layout claim below is
  backed by a `getBoundingClientRect()` / `getComputedStyle()` /
  `elementFromPoint()` measurement, not by reading CSS.
- `gofmt -l .`, `go build ./...`, `go vet ./...`, **all 30 guards** in
  `.github/workflows/ci.yml`'s build job (the implementer's doc listed 6),
  `go test`, and the full `e2e/tests/` suite re-run independently.

## Findings

Severity-ranked. "FIXED" means I changed it in this worktree and re-ran the
full gate afterwards.

### HIGH

#### H1 — The default theme silently overrode the new rail's layout · FIXED

`web/public/themes/monarch.css` is loaded **after** `app.css`
(`web/ui/layouts/base.html:21` then `:23`) at equal specificity, so every
declaration in it wins. It carried a full copy of the *old horizontal top
bar's layout* alongside its colours:

```css
.nav { …; padding:.6rem 1rem; display:flex; justify-content:space-between; align-items:center }
.nav a { color:#fff; margin-left:.8rem }
```

Harmless while `app.css` laid the nav out the same way. Once `app.css` made
`.nav` a vertical rail it broke it, measured on the running app:

| measured on `.nav`, default theme | value | app.css intended |
|---|---|---|
| `padding` | `10.71px 17.86px` (`.6rem 1rem`) | `.6rem .5rem` |
| `align-items` | `center` | `stretch` |
| rail content box | **44.6px** | 62.5px |
| `scrollWidth` vs `clientWidth` | **89 vs 80 → horizontally scrollable** | no overflow |
| `.logo img` height | **45.53px (2.55rem)** | 1.6rem |

Consequences on a real till:

- Every `.nav-toggle` has `min-width: 48px` but the rail's content box was
  only 44.6px, so **the rail's own items overflowed it and the rail became
  horizontally scrollable** — on a touchscreen, a stray sideways swipe
  slides the icon column.
- `.nav a { margin-left:.8rem }` (a leftover for spacing inline links in
  the old bar) made every `<a>` rail item **exactly .8rem narrower than
  every `<button>` one** in the same column — measured 48.0px for
  Till/Menu/Inventory vs 62.28px for Deposit refund, i.e. a visibly ragged
  rail. It is also a **physical** property, so under RTL (fa/ar), where the
  rail flips to the right, it landed on the wrong side.
- The rail logo shrink this change documents ("2rem → 1.6rem in the rail")
  **never actually took effect** — monarch's `.logo img { height: 2.55rem }`
  won.

`monarch` is the **default** theme (`internal/config/config.go:104`,
`getenv("UT_THEME", "monarch")`), so this was the out-of-box appearance on
every till, not an edge case. `fresh`/`slate`/`amber` only set `.nav`/`.nav a`
*colours* and were unaffected.

**Fixed** by reducing monarch's `.nav`/`.nav a` to colours only (layout
belongs to `app.css`, which now owns both the rail and the `<=480px`
fallback). After the fix, measured: no horizontal overflow, `align-items:
stretch`, and all four rail items a uniform **62.5px**. I deliberately left
`.logo img { height: 2.55rem }` alone — a theme choosing a logo size is
defensible, and with the padding fixed it no longer overflows — but it does
mean the documented 1.6rem rail logo is still not what ships. See follow-ups.

#### H2 — The VAT switch's tap target shrank and stopped tracking `--ui-scale` · FIXED

`.order-type-switch { min-height: 48px }`. The control it replaced was
`.order-type-toggle .btn`, i.e. the base `.btn`'s own `min-height: 3rem`.
These are only equal at a 16px root, and this file's baseline is
`html { font-size: calc(var(--ui-scale,1) * var(--fluid-fs)) }`. Measured:

| | old (`.btn`, 3rem) | new (48px) |
|---|---|---|
| 1280×800, default scale (root 17.858px) | 53.6px | **48px** |
| ui-scale 1.5 (root 26.787px) | 80.4px | **48px** floor |

So the switch became **smaller than the control it replaced** at the default
fluid size, and dramatically smaller at any raised UI scale — on the one
control that decides a fiscal receipt's VAT rate (the German §12 UStG
dine-in/takeaway split). This directly contradicts both its own comment
("still a real `.btn`-equivalent touch target … only the SHAPE changed, not
the tap-target floor ut-docs#161 fought to protect") and `app.css`'s own
standing rule 20 lines above it ("rem, not px: … must follow `--ui-scale` /
the fluid baseline like everything else", ut-docs#213).

The brief's requirement for this control was that it "never be
smaller/easier-to-mis-tap than before". As written, it was.
`.table-picker-trigger { min-height: 48px }` had the same defect (and, being
later in the file at equal specificity, overrode `.btn`'s 3rem).

**Fixed**: both → `min-height: 3rem`. Re-measured 53.6px at 1280×800 and
51px at 1024×600 — scaling again.

#### H3 — `hx-sync` would be silently lost when this merges · FIXED (pre-applied)

The brief flagged this as a possibility. It is now a live hazard: **PR #668
has already merged** — `origin/main` is at `53f1cbad "Merge pull request
#668 from universaltill/fix/1337-basket-hx-sync-race"`. It added
`hx-sync="#basket:replace"` to every `#basket`-targeting control, including
precisely the ones this branch rewrote:

| on current `main` | this branch |
|---|---|
| `basket.html:23,27` — the two order-type buttons | deleted, replaced by `.order-type-switch`, **no `hx-sync`** |
| `table_picker.html:17,24` — clear + option buttons | rewritten into the dialog, **no `hx-sync`** |
| `index.html:17` — `kiosk-checkout-start` | moved to the tender footer, **no `hx-sync`** |

To answer the brief's question plainly: **before my change, `hx-sync`
appeared nowhere in `web/ui/` on this branch** — the branch predates #668,
so all four of these controls were un-synced.

I ran a trial merge of `0798df44` into current `main`: it produces content
conflicts in **exactly** `web/ui/pages/index.html`,
`web/ui/partials/basket.html` and `web/ui/partials/table_picker.html` (plus
the regenerated screenshots, which is expected). A human resolving those
three in favour of the branch — the natural instinct, since the branch's
markup is the newer rewrite — would **silently reintroduce the exact race
#668 just fixed**, on the VAT switch, both table-picker buttons and New
Sale. Nothing would look wrong.

**Fixed** by pre-applying `hx-sync="#basket:replace"` to all four controls
on this branch, so the merge is safe however the conflicts are resolved.
`buttons.html` merges cleanly and keeps #668's own addition.

### MEDIUM

#### M4 — Rail padding applied to pages that render no rail · FIXED

`body { padding-inline-start: var(--rail-width) }` is on bare `body`, but
`.nav` comes from `base.html`, and `login.html` / `setup.html` /
`order_tracking.html` are standalone pages that load `app.css` *without*
`base.html`. Measured at 1280×800 and 1024×600 on `/login` and `/setup`:
`padding-inline-start: 80.36px` with **no `.nav` in the DOM**. Because
`body.login-screen` centres its card with `justify-content: center`, the
card is centred inside a content box shifted 4.5rem inward — drawn ~40px off
true centre (card centre x=680 vs viewport centre 640), on the first-boot
wizard and lock screen, i.e. the first screens a new shop owner ever sees.

`self_order.html` / `self_order_shop.html` escape this only by accident
(their own `padding` shorthand resets it — measured 32px/16px).

**Fixed**: `body.login-screen, body.tracking-screen { padding-inline-start: 0 }`.
Re-measured: card centre 640 = viewport centre.

#### M5 — The switch no longer states *which* state is active, to a screen reader · NOT FIXED

Mechanically the WAI-ARIA switch pattern is wired correctly (`role="switch"`,
`aria-checked` flips, Space works — all verified at runtime). But
`aria-label="{{ T "basket.order_type.label" }}"` → **"Dine in or takeaway"**
overrides the element's contents as the accessible name, so the
announcement is:

> "Dine in or takeaway, switch, **off**"

"off" does not say whether the sale is dine-in or takeaway; a user has to
know the convention that unchecked = dine-in. The two-button version it
replaced announced each state unambiguously ("Dine in, pressed" /
"Takeaway, not pressed"). Sighted users are fine — the visible label reads
"🍽️ Dine in" / "🥡 Takeaway".

For the control that sets VAT on a fiscal receipt this is worth closing.
Not fixed here because it needs an i18n/product call, not a reviewer's
guess. Cheapest correct fix: make the accessible name the *thing being
toggled* rather than the question — `aria-label` = "Takeaway" — so it reads
"Takeaway, switch, on" / "Takeaway, switch, off". That needs a new locale
key in all four locales, hence a card rather than an inline edit.

#### M6 — New Sale is below the fold at phone width · NOT FIXED

`index.html`'s comment says New Sale moved to `.tender-default-footer`
because "that row renders at every width uniformly, so New Sale needed
neither a rail copy nor a phone-fallback copy". It *renders* at every width;
it is not *visible* at phone width. Measured at 360×640:

- `kiosk-checkout-start` rect `top: 635, bottom: 689`, viewport height 640
- `document.elementFromPoint()` at its centre → **`null`**
- reachable only by scrolling `.pos-container` (`overflow: auto`)

Before this change New Sale lived in `.kiosk-header` — the row that now
carries only Inventory and Deposit refund — and was always visible at the
top at phone width. So a frequently-used action went from always-on-screen
to scroll-to-reach on phones.

Not fixed: where New Sale lives is the product owner's own live decision
(they explicitly asked for it next to Hold Sale and Payment), and the
phone-width fallback row directly above is the obvious home if they want it
back. Flagging that the code comment's justification is not quite right.

### LOW

#### L7 — The rail's vertical budget is tight at 1024×600 with a manager session · NOT FIXED

Simulated the real `session_chip.html` markup (3 admin links + operator +
Lock) on `/settings` at 1024×600:

- `.nav` `scrollHeight` 614 vs `clientHeight` 600 → overflows by 14px
- Lock button `bottom: 604` → ~4px clipped, though still hit-testable at
  its centre, and the rail scrolls

Not broken, but there is no headroom: one more rail item (a plugin nav
entry, or a `sync-chip`/`fiscal-chip` wrapping to two lines) pushes the Lock
button off-screen on the most common kiosk resolution. The sale screen
itself is fine (`body.sale-screen .session-admin-link { display: none }`
removes three items). Worth a regression test asserting Lock stays fully
visible at 1024×600 with a manager session — the kind of thing
`tender-panel-reachable.spec.ts` already does for the tender panel.

#### L8 — Two comments described behaviour that does not exist · FIXED (comments only)

- `session_chip.html` claimed "`.session-user`'s own CSS clamps it to one
  line with an ellipsis (app.css)". It does not — `.session-user
  { font-weight: 600 }` is the entire rule. At `<=480px`
  `.nav-toggle-label` sets `white-space: nowrap` with no `overflow` /
  `text-overflow`, so a long operator name **overflows rather than
  ellipsizes**, in exactly the phone-width top bar whose two-row wrapping
  budget ut-docs#413 fought to fit. I corrected the comment to describe
  reality and left the behaviour alone — clamping it is a real visual change
  that wants its own card and its own phone-width test.
- `app.css`'s `.table-picker-trigger` claimed "`flex: 1` so the two share
  `.order-type-row` evenly". It is inert: the flex item of `.order-type-row`
  is `table_picker.html`'s own `<span id="table-picker">` wrapper, not the
  button. Measured switch 316.2px vs trigger 113.1px — not "evenly". The
  resulting layout is fine (arguably better), so I corrected the comment
  rather than the CSS.

#### L9 — The soft-gated table picker still costs a flex gap · NOT FIXED

With zero tables configured the fragment correctly renders
`<span id="table-picker">` with nothing in it (see the ADR-0054 result
below). But that span is still a flex item of `.order-type-row`
(`gap: .5rem`), so the switch is ~8.9px narrower than the row even when no
table chrome exists. Purely cosmetic.

#### L10 — `<dialog>` nested inside `<span>` is invalid HTML · NOT FIXED

`table_picker.html` puts a `<dialog>` (flow content) inside
`<span id="table-picker">` (phrasing content). Browsers parse and run it
correctly — verified `showModal()` puts it in the top layer (`:modal` true)
— but it is invalid markup. Making the wrapper a `<div>`, or
`display: contents`, would fix both this and L9.

## Checked and found correct

These were specifically probed and are **not** defects:

- **`$nextOrderType` / `hx-vals` is exactly right, with no edge case.**
  Runtime-verified both directions: dine-in → `{"order_type":"takeaway"}`,
  takeaway → `{"order_type":""}`, `aria-checked` and the visible label flip
  in step, and Space toggles it. The domain is provably binary — the handler
  (`internal/pages/pos_api.go:562`) clamps to exactly `""` or `"takeaway"`
  ("Any value other than `pos.OrderTypeTakeaway` is treated as
  dine-in/standard"), so the template's computed value is always the true
  opposite of `.OrderType`. `jsonVals` is escape-safe (returns a plain
  string that `html/template` escapes in attribute context).
- **The visually-hidden rail labels really are in the accessibility tree.**
  Computed `position: absolute` + 1px + `clip-path: inset(50%)` — *not*
  `display:none` or `visibility:hidden` — and `innerText` still returns
  "🧾 Till", "☰ Menu", etc. The icon spans are correctly `aria-hidden`.
  The bug-report chip has no label span but carries `aria-label`, so its
  icon-only form is fine.
- **Focus order / keyboard reachability** is unchanged by the fixed
  positioning — the rail is still first in DOM order, exactly as the old top
  bar was.
- **The `<=480px` fallback cascade does what the comments claim**, verified
  by measurement rather than by reading: at 360px the rail copies of
  Inventory and Deposit refund compute to height 0 (`.nav-rail-only` →
  `display:none`) while `.kiosk-header.phone-fallback-only` is shown, and at
  ≥481px the reverse. The `-phone` testid suffixes mean no Playwright
  strict-mode locator can resolve to two elements. Inventory deliberately
  has no phone counterpart (it has its own ☰ Menu tile) — a real reason, not
  an omission.
- **ADR-0054's soft-gate genuinely still holds.** With zero tables the
  fetched fragment is literally `<span id="table-picker">\n  \n</span>` — no
  trigger button, no dialog, no chrome. With tables inserted, the full
  flow works: trigger renders, dialog opens as a true modal, both options
  listed, selecting "Table 1" round-trips through `/api/pos/table`, the
  dialog closes and the trigger relabels — with **no JS console errors** at
  either width.
- **RTL is correct in the new code.** At `?lang=fa`, `dir="rtl"`, the rail
  flips to the inline-end side (`navX` 1199.6 of 1280) and the body padding
  follows (`padding-right: 80.36px`). The new rail/switch/dialog CSS uses
  logical properties throughout. The single physical `left` in the nav path
  was monarch's `margin-left` (H1), now removed.

## Gate

| check | result |
|---|---|
| `gofmt -l .` / `go build ./...` / `go vet ./...` | clean |
| All **30** CI build-job guards | 30/30 pass (before and after my fixes) |
| `go test` (excl. `internal/plugins`) | pass, except one pre-existing failure — see below |
| `make docs-shots` | re-run; screenshots + manifest regenerated for my CSS changes |
| Full `e2e/tests/ --project=default` | **233/233** on a clean run |

Notes on the two things that *looked* like failures and are not:

- `TestListenWithFallback_WildcardHostFallsBackToLoopback`
  (`internal/server`) fails on this machine — and **also fails on
  `origin/main`**, verified in a separate worktree. macOS binds `[::]` where
  the test expects loopback-only. Pre-existing and environmental, nothing to
  do with this branch.
- My first full e2e run reported 232/233 and a later one 150/233. Neither
  was real: the first was a single flake under load (the failing spec passed
  3/3 in isolation), and the second was the **port collision the brief
  warned about** — a concurrent session was running its own e2e suite from a
  different worktree against the same hardcoded 127.0.0.1:8091/8092, and its
  harness `kill -9`s whatever holds those ports before starting. Re-run on
  an unloaded machine: **233/233**. The implementer's claimed 233/233 is
  independently confirmed.

## What I changed

All in this worktree, on top of `0798df44`. **Not pushed** — these need to
be pulled into the branch/PR before merge.

| file | change |
|---|---|
| `web/public/themes/monarch.css` | H1 — reduce `.nav` / `.nav a` to colours only |
| `web/public/app.css` | H2 — `.order-type-switch` and `.table-picker-trigger` `min-height: 48px` → `3rem`; M4 — `body.login-screen, body.tracking-screen { padding-inline-start: 0 }`; L8 — corrected `.table-picker-trigger` comment |
| `web/ui/partials/basket.html` | H3 — `hx-sync="#basket:replace"` on the switch |
| `web/ui/partials/table_picker.html` | H3 — `hx-sync` on the clear and option buttons |
| `web/ui/pages/index.html` | H3 — `hx-sync` on the relocated New Sale button |
| `web/ui/partials/session_chip.html` | L8 — corrected the false ellipsis comment |
| `web/help/img/**`, `web/help/img/manifest.json` | regenerated via `make docs-shots` (required by `guard-docs-shots` after any `web/public/**` or `web/ui/**` change) |

## What I'd block on

If this were pre-merge sign-off, **H3 is the one hard blocker** — not
because the branch is wrong today, but because merging it into current
`main` conflicts in the three files whose `#basket` controls #668 just
fixed, and the natural conflict resolution silently undoes that fix. Either
take my pre-applied `hx-sync` edits, or rebase onto `main` and re-apply
them deliberately; do not resolve those three conflicts without checking
`hx-sync` survived on all four controls.

H1 and H2 I would also want in before merge — both are small, both are now
done, and H2 in particular is a measurable regression on the tax-path
control the ticket's own comments go out of their way to protect.

M5 and M6 are legitimate follow-up cards, not merge blockers.

## Verdict

**Safe to merge with the fixes in this worktree applied, plus the noted
follow-ups.**

The change itself is good work — the rail structure is sound, the
`.nav-rail-only`/`.phone-fallback-only` split genuinely does what its
comments claim at both widths (I traced and measured it rather than trusting
them), ADR-0054's soft-gate survives intact, RTL works, the switch's
opposite-value computation is provably correct, and the visually-hidden
labels are real accessible names rather than the `display:none` trap. The
implementer's own doc was accurate and flagged its own gaps honestly.

What a same-model self-review structurally could not catch is what this pass
found: three of the defects were **invisible in the diff**. H1 lived in a
theme file the diff never touched, and quietly overrode the new CSS because
it loads later. H2 looks correct in isolation — `48px` reads like the same
number as `3rem` until you know the root font-size is not 16px. H3 did not
exist when the branch was written and only became a hazard when #668 landed
on `main` an hour or so earlier. All three needed either running the app and
measuring, or looking outside the diff.

**Not safe to merge as-is** only in the narrow sense that H3 would regress
`main` on conflict resolution; with these fixes carried across, this is
ready.
