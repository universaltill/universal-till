# Code review — catalog EAN-13 validation

- **Task:** universaltill/ut-docs#192
- **Branch:** `fix/catalog-ean13-checksum-validation`
- **Scope:** runtime catalog barcode validation, handler error mapping, tests,
  locale strings, and the barcode data-model contract

## What shipped

`CatalogRepo.AddBarcode` now enforces the EAN-13 mod-10 check digit whenever
the caller explicitly supplies `EAN13` (case-insensitive). An omitted type is
inferred as `EAN13` for a 13-digit value and therefore receives the same
validation; other omitted shapes are stored as `CODE128`, preserving ADR-0021's
arbitrary scanner/keypad/PLU path. Explicit `CODE128` remains permissive, so a
shop can deliberately store a 13-digit internal code that is not EAN-13. The
repository never auto-corrects an entered identifier.

Invalid EAN-13 input is returned as a typed repository error. The catalog
create/attach handlers map it to locale keys in English, Persian, Arabic, and
Turkish rather than exposing repository English to the operator. The data-model
documentation records the inference and validation contract. No schema change,
migration, money path, plugin boundary, network dependency, or README claim is
affected.

## BA and architecture

- The stale premise was confirmed against current `origin/main`: the demo rows
  fixed under ut-docs#17 are valid, but the runtime repository still defaulted
  every omitted type to `EAN13` without validating length/checksum.
- This belongs in core `universal-till/internal/data`: barcode integrity is a
  catalog repository invariant used by local UI, imports, and cloud directives.
- Rejecting is safer than auto-correcting because the barcode is an external
  identifier. Permissive non-EAN symbologies retain the intentional escape hatch.
- ADR-0005 and ADR-0021 are preserved. This follows existing repository/input-
  validation patterns and does not introduce an architectural decision, so no
  new ADR is warranted.

## TDD evidence

The repository regression test was written before the implementation. Against
the original code:

```text
--- FAIL: TestAddBarcode_ValidatesExplicitEAN13AndPreservesArbitraryCodes
catalog_repo_crud_test.go:729: expected an invalid EAN-13 check digit to be rejected
```

After implementation it passed. During Review, the production validation block
was independently removed again while leaving the finished test intact; the test
failed at the same defect (`untyped 13-digit barcode ... expected ... rejected`).
The block was restored and the same focused test passed.

The review's i18n finding was also handled test-first: the handler test requested
Persian and initially failed because the response was the raw English repository
error. Typed-error mapping and locale entries were then added; the focused handler
test passed with the Persian response.

## Independent review

A fresh-context reviewer performed a read-only adversarial pass over the tracked
diff, read the repository rules and ADRs, and ran diff checks, affected tests,
build, vet, the full Go suite, and the data-access guard.

| Severity | Finding | Resolution |
|---|---|---|
| Should-fix | Only invalid omitted-type inference was covered; an implementation that rejected every untyped 13-digit value could false-pass. | Fixed: a valid untyped `9780306406157` is persisted and asserted to have inferred type `EAN13`. |
| Should-fix | The new repository error flowed through the catalog handler as hardcoded English, and the handler test locked that wording in. | Fixed: added `ErrInvalidEAN13`, locale-aware handler mapping, four locale entries, and a Persian handler regression test. |

The reviewer found no blocker in checksum weighting, digit rejection, explicit
`CODE128`, case normalization, no-autocorrection behavior, SQL placement,
security/secrets, personal-name handling, or documentation. The recurring
file-write (`MkdirAll`/stable path) hazards are not applicable because this change
writes no files.

## Verification

Final verification after all review fixes:

- `go build ./...` — pass
- `go vet ./...` — pass
- `go test ./...` — pass
- `bash scripts/ci/guard-data-access.sh` — pass
- `bash scripts/ci/guard-i18n.sh` — pass; locale key sets match
- `git diff --check` — pass
- Focused repository and real-mux handler regressions — pass

Beyond automated tests, a freshly built binary was booted against an isolated
temporary data directory with authentication disabled. A real POST to
`/api/catalog/barcode?lang=fa` using invalid `5449000000995` returned HTTP 400
with the Persian translation, and a direct query of the running app's SQLite DB
confirmed zero matching barcode rows. The process was stopped gracefully, the
temporary data removed, and the port confirmed closed. There is no matching
Playwright spec for this server-side attach error path; the real-process HTTP/DB
check exercised the applicable runtime layer directly.

## Verdict and deferred work

**Safe to merge once PR CI is green.** Both independent-review findings were
fixed and the full gate was rerun afterward. No work is deferred from this card.
The three pre-existing untracked planning documents were excluded from the diff.
