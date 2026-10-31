# Review: short side-by-side tender rows at 768-900px (ut-docs#5221)

- **Branch:** `fix/5221-short-side-by-side-tender`
- **Files:** `web/public/app.css`, `e2e/tests/landscape-phone-sale-2918.spec.ts`
- **Author:** Sonnet subagent (Dev + Tester), from an Architect brief based on measurements.
- **Reviewer:** Opus, a different model from the author, inline review plus its own driven run.

## Change
1. **Action row, every window.** `.tender-default-footer` becomes an
   inline-size container. Below 16.45rem (Pay's 7rem basis, plus three 2.75rem
   icons, plus the gaps) Pay takes a full line and Hold, New sale and Open
   orders share the next line evenly. Before, Open orders wrapped alone onto
   its own line. A row that already fits is unchanged (900x414 and desktop).
2. **Scan row, 768-887px wide and at most 540px tall.** The scan form and the
   action row dissolve into three rows of `.tender` (`display: contents`):
   - row 1: the field, qty and +;
   - row 2: the camera, OSK and identify icons, then Hold, New sale and Open
     orders, each 44px or wider;
   - row 3: Pay at full width.

   The tender is no taller than before (180.47px at 768x400). The field went
   from 79.6px to 133px, and from 26px to 133px with the OSK toggle shown.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | check | `container-type: inline-size` sets the footer's intrinsic width to zero, so it would collapse anywhere it is sized to its content. | No change. `.tender` is a column flex with `align-items: stretch` in every layout, and no theme (`web/public/themes/*.css`) or plugin overrides that. No dialog sits inside the footer, so containment traps no `position: fixed` overlay. |
| 2 | minor | In part 2, Pay comes after the icons visually but before Hold, New sale and Open orders in the DOM, so tab order differs from visual order in that one window. | Accepted and documented in the CSS comment. It is a single large target, and the scan, then tools, then Pay order stays coherent. |
| 3 | minor | At 888-900px wide and at most 540px tall, the old one-row scan row stays. The field is 108-120px, but only 55-67px when the OSK toggle is shown. Extending part 2 there would add a third row to a window whose action row fits on one line. | Accepted. Follow-up card filed. |
| 4 | minor | A plugin identify button, the OSK toggle and the camera together make six icons on row 2. Six at 44px do not fit 251px, so one wraps to a fourth row at 768x400-480. | Accepted, a rare combination. Same follow-up card. |
| 5 | nit | The test's 180.5px tender ceiling is a scale-1 pixel constant. | Kept: it is the measured pre-change height, and the spec only runs at scale 1. |

## Verification
- **TDD:** 5 of the 6 new cases fail on the old CSS:
  - three action-row cases, on Open orders alone on its line;
  - two field cases, at 26px against the 87.7px needed.
  The 900x414 case passes on both. After the change,
  `landscape-phone-sale-2918.spec.ts` and `tablet-tier-totals-416.spec.ts`
  pass 39 of 39.
- **Reviewer's driven run** on a freshly built till, at 768x400, 768x600 and 768x400 with `?lang=ar`:
  - typed a code and pressed Enter, and the item was added (form submit intact);
  - opened Pay, and the payment overlay is `position: fixed`, full viewport, with its method button hit-testable;
  - screenshots show three deliberate rows at 768x400, the "Barcode" placeholder whole, and the Arabic layout mirrored;
  - 768x600 shows the field on its own row, then Pay, then three even icons.
- **Guards** (every `scripts/ci/guard-*.sh` named in `ci.yml`): all pass except two that fail on this container's tools, not on the change:
  - `guard-deadcode-baseline.sh`: the container's Go is 1.26 and the repo needs 1.27;
  - `guard-shellcheck-version.sh`: no shellcheck installed.
  CI runs both.
- **Help manual:** no text change. This is a layout adjustment in a narrow window, and no help screenshot is taken at 768px.
