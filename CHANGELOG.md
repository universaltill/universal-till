# Changelog

Release notes for the till. Versions come from the release tag
(`RELEASING.md`); entries under **Unreleased** ship in the next release.

## Unreleased

### Added

- **Users and PINs from my. (ut-docs#2810, ADR-0115 §2 amendment).** The
  main till applies the cloud directives `save_user`, `set_user_pin` and
  `deactivate_user` (contract: ut-docs `reference/till-user-directives.md`).
  A PIN arrives only HPKE-sealed (RFC 9180, X25519 / HKDF-SHA256 /
  AES-128-GCM) to a new main-till directive key,
  `<data dir>/secrets/directive-x25519.key`, created at the main till's first
  cloud check-in and reported as `directive_key` on every check-in. Satellite
  tills never create or report one and leave these directives to the main
  till. The main till's config report now also carries `users` (never PIN
  hashes or sessions).
- ut-cloud: `claims.DirectiveMinTillVersion` for `save_user`,
  `set_user_pin` and `deactivate_user` is the release that ships this entry
  (expected **0.28.0**, the next minor after v0.27.1; confirm against the
  actual tag).
