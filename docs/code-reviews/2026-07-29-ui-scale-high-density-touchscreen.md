# 2026-07-29 — UI scale ceiling + basket flex-collapse on high-density touchscreens

## What shipped

Farshid reported live, testing the till on a real Pi with a 10.1"
1920×1200 touchscreen (~224 PPI):

1. **"the scale on 150% it still small to read"** — the settings page's
   `ui_scale` dropdown maxed out at 150%, but the backend
   (`internal/pages/settings_page.go`) already validates and accepts
   0.5–2.0. At 150% on this panel, computed text cap-height was still
   only ~1.9mm — under any reasonable legibility floor. Fixed: added
   `175%`/`200%` options to `settings.html` (no backend change needed —
   the range was already there, just never exposed).

2. **"it even can't show an item in the basket"** — a separate, more
   serious bug found while investigating #1: at high `ui_scale` on a
   ~1200px-tall viewport, a scanned item was correctly added to the sale
   (the total updated) but its row was **completely invisible** — not
   clipped, genuinely zero rendered height (confirmed via Playwright
   `boundingBox()`). Root cause: `.pos-container`'s grid column width
   (`330px`/`420px`) and `.line-item`'s `max-width: 158px` were raw
   pixels, never scaling with the rem-based root font-size `ui_scale`
   drives — so container width stayed fixed while content inside grew.
   Combined with `.basket-scroll { min-height: 0 }` inside a flex column
   whose siblings (toggle row + pinned totals) also grew, the item
   list's flex-basis got squeezed to true zero once those siblings
   outgrew the grid row's allotted height. Fixed: `330px`/`420px` →
   `20.625rem`/`26.25rem`, `158px` → `9.875rem`, `.basket-scroll` given a
   `min-height: 6rem` floor, and `.pos-container > .basket`'s
   `overflow: hidden` → `overflow-y: auto` so if the floor still can't
   fit, the panel becomes genuinely scrollable rather than silently
   hiding sale contents.

Real root cause for #1 was independently confirmed against hard evidence,
not assumption: `wlr-randr` identified the live panel as a Waveshare
10.1" 1920×1200 (confirmed via Waveshare's own product page), giving
~224 PPI — about 2.3× a typical 96 PPI assumption.

Also corrected `ut-docs/hardware/diy-pos.md`'s existing scale guidance,
which recommends this exact panel style and gave backwards advice
("smaller panel → lower scale") — the real determinant is pixel density,
not physical size, and a small high-resolution panel needs *more* scale,
not less.

## Independent review (different model, opus)

**Verdict: PASS**, no blocking findings. Everything re-verified from
scratch:

- Full `go build`/`go vet`/`go test` clean; all three CI guards pass
  (data-access, i18n, webkit-version).
- Playwright `default`+`auth` projects (19 specs) run twice, both green,
  no flakiness.
- **Independently re-verified both TDD claims** by reverting each fix in
  isolation and re-running: reverting `app.css` → the new Playwright spec
  fails on the exact `.basket-scroll` zero-height assertion; reverting
  `settings.html` → the new Go test fails; both restored and passing
  again, diffs confirmed byte-identical to the working versions after.
- Confirmed the 200% option's value (`2`) is within the backend's
  already-inclusive `f > 2.0` rejection bound (2.0 itself is accepted).
- Confirmed neither new test is tautological (both proven red against
  `main` before green).
- Confirmed no scale=1 regression: `overflow-y: auto` only shows a
  scrollbar on genuine overflow, no double-scrollbar nesting at normal
  scale.
- Confirmed no `os.MkdirAll`/cwd-path bug class applies (no Go
  file-writing code in this diff) and no secrets/real client names.

Two non-blocking nitpicks: the `.receipt-view` variant rule now
redundantly repeats `overflow-y: auto` (harmless, `display: block` still
does the meaningful override); three unrelated pre-existing untracked
files in the working tree must not be swept into this commit (they
weren't).

## Verified beyond automated tests

- Real screenshots at 100%/150%/200% scale on both the actual live Pi
  (via `grim` over the real Wayland kiosk session) and a headless
  Playwright run at the identical 1920×1200 viewport — the exact same
  clipping/collapse bug reproduced in a plain browser, confirming it's a
  general CSS bug, not a Pi/kiosk-specific rendering quirk.
- Visually confirmed the fix: full basket row (name, barcode, qty, price,
  total, remove button) readable and untruncated at 150%; at 200%, the
  row renders correctly and the panel becomes scrollable to reach the
  totals, rather than losing the row entirely.

## Explicitly deferred / accepted gaps

- Auto-detecting physical screen density to pick a smarter default scale
  — real potential improvement, no reliable browser-side signal for
  physical panel size without extra setup-wizard input; not attempted
  here, logged as a possible follow-up if it recurs.
- Backend's scale ceiling stays at 2.0 (not raised) — computed to be
  sufficient for the reported panel (~112 effective PPI at 2.0, close to
  a normal comfortable range); no evidence yet of a denser panel needing
  more.

## Safe to merge

Yes. Feature branch `fix/ui-scale-high-density-touchscreen`.
