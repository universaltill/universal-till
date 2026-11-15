# Review: 888-900px short sale screen, three tender rows when a plugin identify button shows (ut-docs#5235)

- **Branch:** `fix/5235-identify-three-rows`
- **Files:** `web/public/app.css`, `e2e/tests/landscape-phone-sale-2918.spec.ts`,
  `web/help/img/manifest.json` (surface hash only)
- **Author:** Sonnet (Dev subagent, build cycle `lane:cloud-41`). The brief and design were written by the cycle's Opus orchestrator.
- **Reviewer:** Opus, a different model from the author, in a fresh context. It got the diff and the card, not the author's conclusions.

## Change
The window is 888-900px wide and at most 540px tall. With a plugin identify button, the OSK toggle and the camera all shown, the two-row tender left the barcode field 73-85px. Its English placeholder "Barcode" needs 87.71px.

The card's option (a) is now in place. While `#plugin-identify-open` is visible, the tender uses the three-row layout of the 768-887px block. In `.pos-container > .tender:has(#plugin-identify-open:not([hidden]))` the same rules are repeated inside the 888-900px query, and both blocks' comments say to keep them in sync. Without a visible identify button, 888-900px keeps its two rows (126.92px), unchanged.

The card's condition holds. At 900x414 the first tile row still shows unscrolled:

| window | field before | field after | tender | products panel |
|---|---|---|---|---|
| 888x414 | 73.44px | 234.08px | 180.47px | 176.33px |
| 900x414 | 85.44px | 246.08px | 180.47px | 176.33px |
| 900x540 | 85.44px | 246.08px | 180.47px | 302.33px |

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (test) | `wholeAndHit` checks the first tile only against the viewport. The products panel clips it, so 104 of 141px show. The case would pass with a far shorter panel. | Fixed. The new case also asserts that at least half the tile shows inside `.products`, and that it can be hit. 104 of 141px is the same as the existing 768-887px layout at 414px tall. |
| 2 | minor | The comment said "a tile row unscrolled" without saying the row is partly clipped. | Fixed. It now says "104 of 141px visible, as at 768-887px". |
| 3 | minor (test gap) | Nothing covered the `:not([hidden])` part. With a selector simplified to `:has(#plugin-identify-open)`, a hidden button would still get three rows. | Fixed. A new negative case puts a hidden button at 900x414 and expects two rows. Checked with a mutation: on the simplified selector it fails with tender 180.47px > 127.5. |
| 4 | nit | Nine rules copy the 768-887 block. | Both comments now say "keep the two in sync". |
| 5 | info, not caused by this change | No identify at 888-900x414 with UI scale >= 1.15: the field is 30px. | Out of scope. With identify, this change makes it better (152px). Noted on the close-out. |

The reviewer also checked these and found them fine:
- no overlapping media ranges (541-700, 768-887, 888-900);
- specificity beats the base rules, and themes and plugins don't style `.tender`, `.scan-row` or the footer (amber's taller Pay gives 192px, the same as the 887px layout);
- app.js unhides the button live, and `:has()` reacts to that;
- UI scale 0.8 to 1.3: no horizontal overflow, icons >= 47px;
- logical properties only, no new strings, no `showModal`.

## Verification
- **TDD:** the reviewer re-ran the cases in a separate worktree with only `app.css` reverted to `main`. All 3 new cases fail on the field-width check (73.44, 85.44 and 85.44px against 87.71px). With the branch CSS they pass.
- **Gate:** after the review fixes, `landscape-phone-sale-2918.spec.ts` + `tablet-tier-totals-416.spec.ts` pass 49 of 49 on 2 workers and 49 of 49 on 4 workers (`UT_UPDATE_CHECK=0`, see below).
- **Separate flake found, ut-docs#5237:** the per-worker tills check the real GitHub releases. Once a newer release exists, an "Update now" chip wraps the status bar, and `667x375: Pay pinned whole` fails. Locally: 1 failed / 47 passed without the variable, 48 passed with it. It happens without this change too, depending on run timing.
- **Screenshots, looked at:**
  - 900x414 with identify, OSK and camera, English: three rows, "Barcode" whole, a tile row and Pay unscrolled, totals whole.
  - The same window in `?lang=ar`: mirrored, placeholder whole.
  - 888x414 and 900x540, English.
- **Unchanged windows:** every rule sits inside the 888-900px x <=540px query and needs a visible identify button. The help screenshots are all at 1024x600, so no pixel changes. Surface hash refreshed (`Docs-Shots-Unchanged: true`).
- **Guards:** every `scripts/ci/guard-*.sh` named in `ci.yml` passes except two, both because of this container:
  - `guard-shellcheck-version.sh`: there is no shellcheck binary.
  - `guard-deadcode-baseline.sh`: the installed deadcode was built with Go 1.26 and the module needs 1.27. It fails the same way on `main`.

  No Go or shell file changed. CI runs both.
- **Not checked:** real touch hardware with a real identify plugin. The worker till has no plugin answering `catalog.identify`, so a stand-in with the real id is used, as in #5227.

**Verdict:** safe to merge.
