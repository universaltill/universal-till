# Test fix: TestReaderLinkRequiresPinnedTLS built a "wrong" pin that could be the real one

Branch `fix/reader-pin-test-flake`. Found by lane:cloud-41 while checking `main` after
merging #2648 (ut-docs#6691): `test-heavy (internal/pages)` on `82a1a818` failed
`TestReaderLinkRequiresPinnedTLS` (reader_link_test.go:401, "the phone accepted a
certificate that does not match its pin"). It passed on the PR head and 5/5 locally.

## Root cause
The test made the mismatching pin as `"00"+pin[2:]`. A pin is lowercase hex SHA-256
(`lantls.PinOf`), and the rig's client compares exactly, so whenever a fresh test
certificate's pin already began with `00` (1 run in 256) the "wrong" pin equalled the real
one and the dial succeeded. Test-only: the product's pin check is unchanged and correct.

## Change
`otherPin` changes the first hex digit (to `1` if it was `0`, else `0`) and fails the test if
the result still equals the pin; `TestOtherPinNeverMatches` pins the `00…`/`0…` cases that
the old expression got wrong (`"00"+"00ab"[2:] == "00ab"`).

## Review (independent, Sonnet; author Opus)
Approve, no findings. Confirmed the exact, case-sensitive compare in the rig
(reader_link_test.go:207) and that no other test uses the pattern. Nit: panics on an empty pin
(not reachable in the test).

## Verification
gofmt/vet clean; `go test ./internal/pages/ -run 'TestReaderLinkRequiresPinnedTLS|TestOtherPinNeverMatches' -count=5`
green; fixed-dates and docs-shots guards green.

Pre-flight: test-only change; no help, i18n, ADR, README or migration impact.
