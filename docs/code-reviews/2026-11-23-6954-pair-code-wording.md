# ut-docs#6954 - which pairing code goes where (combined Tester + Reviewer, incident mode)

Branch fix/6954-pair-code, uncommitted diff vs main. One combined pass (owner order: fast, minimal safe fix).

## What shipped
- `isCloudShopCode` (8 ASCII alnum, optional 4+4 space/hyphen) used only in `joinPrimary`'s bad-code branch: the
  my.universaltill.com code typed under Additional till/Satellite gets `tills.join_error.cloud_code`.
- Main till wizard: a main till's own 6-char code (`normaliseShortCode`) is answered locally with
  `setup.cloud.error.main_till_code`, no cloud call.
- Cloud 409 `device_exists` / `device_revoked` get specific messages; invalid_code, refused, unreachable and the help texts reworded.
- en.json + en.meta.json only (ADR-0160).

## Findings
- No defects found. Cloud code is 8 symbols, a main-till code 6 (normalised), so the checks never collide; the long
  enrol code is decoded first in `joinPrimary`, so ordering is safe; cloud-code check sits after the short-code parse
  and is never an accept path. No message formats the typed code (only the pre-existing `unreachable` detail, which is a network error).
- Old strings: only comments/historic review records and an updated pair of tests mention them; no e2e/ or tests/ asserts the changed strings.
  The placeholder "Code shown on the main till" is unchanged and still asserted by setup_page_test.go.
- Accepted gap: no test asserts a message never contains the typed code (verified by reading the format strings); other locales come from Weblate.
- Not looked at visually (no screenshot of the wizard); text-only change.

## Verification
- `go build ./...`, `go vet ./internal/pages/` clean.
- `go test ./internal/pages/ -count=1 -run 'Join|Cloud|Pair|Setup'`: ok.
- `scripts/ci/guard-i18n.sh`, `guard-i18n-meta.sh`, `guard-data-access.sh`: pass.

Pre-flight: i18n en-only, no SQL, no new files outside pages/locales; no help topic changed (wizard wording only).

Verdict: PASS.
