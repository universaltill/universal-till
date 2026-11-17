# Review: camera photo shrink e2e race (ut-docs#6151)

**Change.** Only one test changed: `e2e/tests/catalog-camera-android-5190.spec.ts`, "is shrunk to 1024px…".

- **The race:** the test awaited the upload *request* and then read `#image-file`'s `files[0]`. The page clears that input when the upload's *response* arrives (`catalog.html`, image-form submit handler). So the read sometimes found no file, and `createImageBitmap(undefined)` threw.
- **The fix:** an `addInitScript` wraps `XMLHttpRequest.prototype.send` and keeps the FormData's `image` File. The test then asserts on that file:
  - its type is `image/jpeg`;
  - its long edge is **exactly** 1024. `shrinkPhoto` scales 3264×2448 to 1024×768.
- No production code changed.
- Chromium gives Playwright no `postDataBuffer()` for a file-backed multipart body, so the file is captured at send time rather than parsed from the request.

**Pipeline.** The card fully specified the fix, so BA and Architect were not run as separate phases (easy, one-file test fix). Dev ran on Sonnet 5.5. The Tester (Sonnet 5.5) and the Reviewer (Opus 5.5) were separate agents.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| F1 | nit | The comment quoted the flake rate as ~1/16; the measured rate is 10/50. | **Fixed.** |
| F2 | info | The capture sees XHR only. If the upload moved to `fetch`, the test fails on "an image part was sent" instead of passing falsely. | Accepted (fails closed). |

## Verification
- **Old test:** 40/50 passed with `--repeat-each 50 --workers 4 --trace off`, so the flake reproduced.
- **New test:** 50/50 passed, run separately by Dev and by the Tester. The whole file passed 3/3 (Dev, Tester, Reviewer). The sibling `catalog-camera-viewfinder-1472.spec.ts` passed 16/16.
- **False-pass checks (Tester):** each break below made the new test fail.

  | Break | Failure |
  |---|---|
  | `shrinkPhoto` scale forced to 1 | Received 3264, expected 1024 |
  | Upload type changed to png | Type assertion fails |
  | `name="image"` removed | "an image part was sent in the upload" |

- **Guards:** fixed-dates, e2e-no-browser, e2e-no-lan-discovery, e2e-no-update-check and e2e-fixtures-import all pass. No Go, shell or locale file changed.

Pre-flight: web/help n/a (test only); reference n/a; en.json n/a; README n/a; no follow-ups.

**Verdict:** approve.
