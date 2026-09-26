# Review: macOS notarization via App Store Connect API key (ut-docs#2870)

- **Date:** 2026-09-26 · **Branch:** `feat/2870-macos-notary-api-key`
- **Author:** Opus 5.5 (lane:local) · **Reviewer:** Sonnet 5 (independent)

## What shipped
- `packaging/macos/notary-args.sh`: picks notarytool credentials — App Store
  Connect Team API key (role Developer) first, then keychain profile, then
  Apple ID; the `.p8` goes to a 0600 temp file (path on argv, never the key)
  and is removed after the submit / on exit.
- `make-dmg.sh`: uses it; after stapling, `stapler validate` + `spctl
  --assess` must pass (the old `spctl … || true` let an unaccepted image
  ship).
- `release.yml`: passes the API-key secrets; imports Apple's Developer ID G2
  intermediate (SHA-256 pinned) and fails fast if no *valid* Developer ID
  identity after the keychain is on the search list.
- `docs/arch/desktop-app.md`: secrets table, Key Vault source of truth,
  rotation.
- Secrets set 2026-09-26: Key Vault `kv-unitill-dev` (6) + GitHub
  `universal-till` (7); written via files/stdin, never printed.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | low | Cleanup trap installed after `notary_args` wrote the `.p8`. | Fixed: trap first. |
| 2 | nit | `g2.cer` not removed after import. | Fixed. |
| 3 | nit | G2 import failure message claimed "already present". | Reworded to a warning; `find-identity -v` is the real gate. |
| (orchestrator) | — | Identity check originally ran before `list-keychains` → would always report 0 valid identities (reproduced locally). | Fixed before review. |

## Verified beyond unit tests
- `xcrun notarytool history --key …` with the real key: authenticated.
- Real codesign of a probe with the real `.p12` in a throwaway keychain on
  the search list + G2: chain Developer ID Application → Developer ID CA →
  Apple Root CA, TeamIdentifier B45H898ZBK, secure timestamp. User keychain
  search list restored after.
- Reviewer re-pinned the G2 hash independently and mutation-tested the
  fail-closed check.
- Not yet verified: a full release — AC requires the next release's `.dmg`
  to pass `spctl` as "Notarized Developer ID" (DevOps step).

**Verdict:** safe to merge.
