# Store registration sends the shop's country (ut-docs#3849)

## What shipped

- `internal/enroll/enroll.go`: `register()` reads `store.country` once. It
  uses the value for the existing region hint and also sends it as
  `"country"`. `countryCode` upper-cases and trims the value and accepts
  exactly two ASCII letters; anything else is left out of the payload. The
  till has no country list (core-neutral). ut-cloud already accepts the
  optional `country` and answers 403 for a country on
  `CLOUD_REFUSED_COUNTRIES` (ut-docs#3629).
- Tests: `TestRegisterSendsCountryNormalised`,
  `TestRegisterOmitsCountryWhenUnsetOrInvalid`, `TestCountryCode`.
- Consent and disclosure copy now names the country, as ADR-0071 §1
  requires:
  - the `setup.auto_register.hint` and `settings.enrol.auto_register.help`
    strings in en/ar/fa/tr;
  - the help topics `claim` and `users` in en/de/tr/fa/ar;
  - the de/es/pt language packs (ut-plugin-language-de#393, -es#392,
    -pt#42);
  - the ADR-0071 payload list (ut-docs#3859).
- The `users` screenshots were regenerated (`make docs-shots`) because its
  prose changed.

## Review

The change was built by Sonnet and reviewed by Opus in a fresh context.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Blocker | The wizard and Settings consent strings (locale keys) still listed the payload without the country, which breaks ADR-0071 §1 ("name that payload and no more"). Only the help pages had been updated. | **Fixed:** core locales, the three language packs and the ADR amendment. |
| 2 | Minor | Settings → Register now shows a refused shop the raw `register returned 403: {...}` error string. This is not new, but a refused shop now reaches it. | **Deferred:** follow-up Backlog card. |
| 3 | Nit | "shop country, region" is loose: the region is only sent for some countries. The old text already said "region" for everyone. | **Accepted** as is. |

Also checked:

- **Offline-first:** `register()` runs only through `RegisterNow` (Settings
  button, the wizard's 5-second bounded attempt, plugin install, TSE
  setup). A 403 is logged and swallowed like any other failure. There is no
  background retry loop on this call, so a refused till calls the cloud no
  more often than before.
- **Recurring bugs:** no file writes or paths in the diff.

## TDD re-verified

The reviewer, working in an isolated worktree, broke the code twice:

- Replacing `fields["country"] = country` with `_ = country` made
  `TestRegisterSendsCountryNormalised/{lowercase_gb,padded_de}` fail with
  `country = <nil>, want ...`.
- Removing the length check made the omit tests fail for `DEU` and `Ü1`,
  and made `TestCountryCode` fail.

After restoring the code, all tests passed.

## Gate

The following ran green after the last edit:

- `gofmt`, `go build ./...` and `go test ./...` (all packages);
- `guard-i18n`, `guard-help-drift`, `guard-help-topics`,
  `guard-core-neutral`, `guard-compliance-claims` and
  `guard-competitor-naming`;
- each pack's `validate.sh` and `check-key-drift.sh`.

Some checks could not run locally because the container's tools are older
than CI's: `golangci-lint` and deadcode are built with an older Go, and
`shellcheck` is not installed. No shell scripts changed. CI covers these.

## Verdict

Safe to merge once the language-pack PRs land, since this changes existing
keys.
