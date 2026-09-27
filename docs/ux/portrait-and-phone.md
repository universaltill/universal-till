# Portrait tablets and phones — sale screen UX record

Card: ut-docs#3050 (folds ut-docs#1985). Phone build: ut-docs#3059. Other
screens at these sizes: ut-docs#3060.

Binding constraints (product owner, 2026-09-12): the left rail stays; room
for product tiles wins ties; it must not feel like a web app.

## Surfaces

| Surface | CSS viewport | Basket line |
|---|---|---|
| 10in Android tablet upright (TECLAST, wall or counter mount) | ~800×1280 | one row |
| iPad upright, 7–8in tablet upright | 768×1024, ~600×960 | name above the controls |
| Phone, one hand | 360–414 × 740–900 | ≤ 480px tier (ut-docs#3059) |

Landscape (≥ 901px) and landscape phones (height < 481px, ut-docs#2918) are out
of scope and unchanged. Short stacked landscape windows in the same width range
(900×600, 768×600) get the shorter lines too: 119 → 59px at 900×600.

## Portrait tablet (built in #3050)

```
┌──┬──────────────────────────────────────────────┐
│  │ BASKET (3)   [Dine in|Takeaway]              │
│R │ Item                      Qty        Price Total │
│A │ [img] Coca-Cola 330ml  [1][−][+][0] £1.20 £1.20 ✕│  ← one row, ≤ 64px
│I │ [img] Pepsi 330ml      [1][−][+][0] £1.15 £1.15 ✕│
│L │ ……… scrolls inside the basket above 38dvh ………  │
│  │ Subtotal · Tax · Total                        │
│  ├──────────────────────────────────────────────┤
│  │ [Barcode……][1][+][scan]                      │
│  │ [ Pay £3.20                  ][hold][…][…]   │
│  ├──────────────────────────────────────────────┤
│  │ Food | Drinks | Household | Produce  [🔍][✎] │
│  │ ▢ ▢ ▢ ▢ ▢                                   │  ← products: ≥ 55% of
│  │ ▢ ▢ ▢ ▢ ▢                                   │    the height at 800×1280
│  │ ▢ ▢ ▢ ▢ ▢                                   │    with 2 lines
└──┴──────────────────────────────────────────────┘
```

- **One-row basket line.** The basket is full width in the stacked tier, so the
  qty / −+ / discount column no longer has to stack in three rows. That stack
  existed for the narrow landscape column (ut-docs#2217). The steppers are
  46×46px and the inputs 46px tall (the `.btn-touch` floor), in kiosk mode
  too.
- **Chosen by the basket's width in rem** (a container query), not the
  viewport. Container `rem` is the root font size, which carries the
  till's display scale, so a scaled-up till picks a roomier line by itself:
  - ≥ 38rem (800px at scale 1): one row;
  - 24–38rem (768px and 600px at scale 1, 800px at scale 1.25): the name gets
    its own full-width row above the controls, the same shape as the phone
    tier, with a one-row header (Item sits beside Qty/Price/Total);
  - < 24rem (scale 1.5–2): the stacked tier exactly as before.
- **Height, not orientation**, gates the layout (`min-height: 481px`), so the
  Android keyboard shrinking the viewport doesn't flip it mid-typing.
- **Basket cap 38dvh** (was 45dvh) on tablets at least 900px tall, about 5
  lines at 800×1280. Longer baskets scroll inside the basket, and Pay never
  moves off screen.
- **Status bar wraps** to a second row instead of scrolling the page
  sideways when every chip shows (Update, Register) on a narrow screen.
- Measured at 800×1280 with 2 lines: products 731px (57%), was ~600px (47%).
  At 600×960, the basket chrome (~180px) plus tender (~127px) caps products
  at ~41%.

## Phone (design; build is ut-docs#3059)

```
┌──┬───────────────────────┐
│R │ Food | Drinks | …  🔍  │
│A │ ▢  ▢                   │
│I │ ▢  ▢                   │   grid scroll container reserves the
│L │ ▢  ▢                   │   pay bar's height (one CSS variable)
│  │ ▢  ▢                   │
│  ├───────────────────────┤
│  │ [ Pay £12.00 · 2 items ]│   ← tap: basket sheet slides up
└──┴───────────────────────┘
```

- **Rail:** stays on the left as a slim icon-only rail. The ≤ 480px top-bar
  fallback (~250px of wrapped chrome today) goes away. Destinations the rail
  can't hold stay reachable through its Menu (☰).
- **Pay bar:** always visible at the bottom, with the live total and item
  count. This is the shape SumUp POS and Square POS use on phones.
- **Basket sheet:** every line is editable (qty ±, discount, remove), and the
  sheet has Pay. A phone build where lines can be added but not edited does
  not ship (#1985 AC).
- Everything a sale needs sits in the bottom two thirds, for one-handed use.

## #1985's three decisions

1. **Bottom-nav overflow:** there is no bottom nav. Binding rule 1 keeps the
   rail, and Menu covers the overflow. This also removes the missing
   Tables/Bookings tab labels (ut-docs#1981).
2. **Scroll padding:** the grid's scroll container gets `padding-block-end`
   equal to the pay bar's height, from one variable. It is tested with the
   last tile fully tappable above the bar.
3. **Selected-card colour:** there is no selected-card state in the code. The
   accent is the theme's `--accent`, so the mockup's blue vs amber came from
   two themes. Nothing to unify.

## Checked

- Playwright `e2e/tests/portrait-tablet-sale-3050.spec.ts`:
  - 800×1280 and 600×960, en + fa (RTL), light + dark;
  - kiosk mode, display scale 1.25 and 1.5, and the keyboard resize to
    800×760;
  - a full status bar at 600px;
  - landscape 1280×800 keeps the stacked qty column.
- Measured at scales 1–2 across 540–900px: at every scale the layout either
  fits or is the pre-existing stacked tier, with the same overflow as
  before (scale 2 already overflows at these widths: ut-docs#391 only
  covers ≥ 901px).
- Screenshots looked at: 800×1280 en, 600×960 fa, 1024×600 and 1280×800 en.
- **Not yet checked on real hardware:** the TECLAST rotated to portrait in the
  Android app. That is a local-lane device check after merge.
