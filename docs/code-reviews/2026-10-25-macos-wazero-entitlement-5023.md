# macOS app killed at launch: hardened runtime without wazero's entitlement (ut-docs#5023)

## What shipped
`packaging/macos/entitlements.plist` (only `com.apple.security.cs.allow-unsigned-executable-memory`), applied to `unitill-pos` in `build-app.sh`; bundle pass no longer uses `--deep`; build fails if the signed binary lacks the entitlement or the bundle signature is invalid; `packaging/macos_entitlements_test.go`; `docs/arch/desktop-app.md`.

## Review (independent Opus 5.5 subagent, ran build/vet/tests and real hardened builds)
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Blocker | First version granted `allow-jit`; wazero uses plain mmap+mprotect, so a real hardened build still crashed. Our earlier "verified" copies had lost the runtime flag after a `--deep` re-sign. | Fixed: `allow-unsigned-executable-memory`; re-verified by building with `MACOS_SIGN_IDENTITY` (hardened, `flags=0x10002(adhoc,runtime)`): server answers 303, no crash report. `allow-jit` build: crashes. |
| 2 | Medium | Tests passed under five mutations. | Fixed: tests read only non-comment lines, match the single real sign call per target, forbid `--deep` on the bundle, check key=true. Mutation-checked: no `--entitlements`, `--deep` back, verify demoted to `&&`, plist key changed: each fails. |
| 3 | Low | `codesign --verify ... && echo` never fails under `set -e`. | Fixed (`|| exit 1`). |
| 4 | Low | `grep -q` pipeline against the repo rule. | Fixed (captured, `case` on key + `[Bool] true`). |
| 5 | Low | Docs silent on entitlements/`--deep`. | Fixed. |

## Not verified / deferred
- Developer ID signature, notarization and stapling not run locally (CI release does); inside-out order is Apple's flow, no Mach-O is left unsigned (reviewer checked).
- Pre-existing, out of scope: `make-dmg.sh` staples `$APP` after the DMG copy, so the app inside the DMG is not stapled (offline first launch). To be carded.
- amd64 not built (bundle is arm64 only).

## Verdict
Safe to merge once CI is green. Final proof is a launch of the notarized DMG from the next release.
