#!/usr/bin/env bash
#
# Regression test for guard-adr-plugin-taxonomy.sh (ut-docs#2134): proves the
# guard actually compares CanonicalTypes against ADR-0002's own taxonomy
# line — not two hardcoded copies of the same list, which is the defect this
# guard replaces (TestCanonicalTypesMatchTaxonomy diffed CanonicalTypes
# against a second literal in its own test file and could never see the ADR
# drift). Covers: matching lists pass; an extra type in code only fails; an
# extra type in the ADR only fails; and DOCS_DIR/explicit-arg pointing at a
# missing ADR file is a hard failure, never a silent skip.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-adr-plugin-taxonomy.sh"
FAIL_COUNT=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

write_go_fixture() {
  # $1 = output path, $2.. = types
  local out="$1"; shift
  {
    echo "package plugins"
    echo ""
    echo "var CanonicalTypes = []string{"
    for t in "$@"; do
      printf '\t"%s",\n' "${t}"
    done
    echo "}"
  } > "${out}"
}

write_adr_fixture() {
  # $1 = output path, $2 = pipe-joined type list, $3 = title count override
  # (default: the real count derived from $2 — pass a wrong number to
  # simulate a stale title, e.g. the N4 title-drift case below).
  local out="$1" list="$2"
  local real_count title_count
  real_count="$(printf '%s' "${list}" | tr '|' '\n' | grep -c '.')"
  title_count="${3:-${real_count}}"
  {
    echo "# 0002 — Plugin type taxonomy (${title_count} canonical types)"
    echo ""
    echo "## Decision"
    # shellcheck disable=SC2016 # literal backtick markdown, no expansion intended
    echo '`canonical_type` and `entries[].type` share one taxonomy:'
    echo "\`${list}\`"
  } > "${out}"
}

BASE_TYPES=(page button popup payment device integration report pricing tax
  import export hardware background_job scheduler receipt_template
  customer_facing auth notification delivery theme language layout)
BASE_LIST="$(IFS='|'; echo "${BASE_TYPES[*]}")"

expect_pass() {
  local label="$1" go="$2" adr="$3"
  if bash "${GUARD}" "${go}" "${adr}" >/tmp/guard_adr_taxonomy_test_out.$$ 2>&1; then
    echo "✓ guard correctly passed ${label}"
  else
    echo "❌ FAIL: expected guard to pass ${label}, but it rejected it" >&2
    cat /tmp/guard_adr_taxonomy_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_adr_taxonomy_test_out.$$
}

expect_fail() {
  local label="$1" go="$2" adr="$3"
  if bash "${GUARD}" "${go}" "${adr}" >/tmp/guard_adr_taxonomy_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_adr_taxonomy_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_adr_taxonomy_test_out.$$
}

# 1. Matching lists (the real, current 22-type taxonomy) → pass.
go_match="${TMPDIR}/match.go"
adr_match="${TMPDIR}/match.md"
write_go_fixture "${go_match}" "${BASE_TYPES[@]}"
write_adr_fixture "${adr_match}" "${BASE_LIST}"
expect_pass "matching 22-type lists" "${go_match}" "${adr_match}"

# 2. Code has a type the ADR doesn't (the real historical bug: `layout`
#    added to code, ADR-0002 never touched) → fail.
go_extra="${TMPDIR}/go_extra.go"
write_go_fixture "${go_extra}" "${BASE_TYPES[@]}" "widget"
expect_fail "code type missing from ADR" "${go_extra}" "${adr_match}"

# 3. ADR has a type the code doesn't (the reverse drift) → fail.
adr_extra="${TMPDIR}/adr_extra.md"
write_adr_fixture "${adr_extra}" "${BASE_LIST}|widget"
expect_fail "ADR type missing from code" "${go_match}" "${adr_extra}"

# 4. A missing ADR file, passed explicitly, is a hard failure — never a
#    silent skip (a skip here would make the CI job silently green forever
#    on exactly the drift this guard exists to catch).
expect_fail "missing ADR file (explicit arg)" "${go_match}" "${TMPDIR}/does-not-exist.md"

# 5. A missing Go file, passed explicitly, is also a hard failure.
expect_fail "missing Go file (explicit arg)" "${TMPDIR}/does-not-exist.go" "${adr_match}"

# 6. A code-side type containing a character outside [a-z0-9_] (independent
#    review, ut-docs#2134, finding B2: an earlier grep pattern of
#    `"[a-z_]+"` silently DROPPED any non-matching quoted value from the
#    comparison instead of failing loudly — e.g. a hypothetical `oauth2`
#    type just vanished and the guard reported a false pass). This must be
#    a hard failure naming the bad value, never a silent drop.
go_digit="${TMPDIR}/go_digit.go"
write_go_fixture "${go_digit}" "${BASE_TYPES[@]}" "oauth2"
expect_fail "code type with a digit is validated, not silently dropped" "${go_digit}" "${adr_match}"

# 7. A `//` comment inside the CanonicalTypes block containing a quoted,
#    lowercase-looking word must not be mistaken for a real entry
#    (independent review finding N8-D). Comment-stripping must happen
#    before extraction.
go_commented="${TMPDIR}/go_commented.go"
{
  echo "package plugins"
  echo ""
  echo "var CanonicalTypes = []string{"
  for t in "${BASE_TYPES[@]}"; do
    printf '\t"%s",\n' "${t}"
  done
  echo '	// "widget" was rejected, see ut-docs#123'
  echo "}"
} > "${go_commented}"
expect_pass "quoted word inside a // comment is not a real entry" "${go_commented}" "${adr_match}"

# 8. The ADR's title count ("(N canonical types)") drifting from its own
#    taxonomy line — independently of whether that line agrees with the
#    code — is a hard failure (independent review finding N4: a 23rd type
#    could be added to the pipe line alone and pass unnoticed while the
#    title still said 22).
adr_stale_title="${TMPDIR}/adr_stale_title.md"
write_adr_fixture "${adr_stale_title}" "${BASE_LIST}" 21
expect_fail "ADR title count disagrees with its own taxonomy line" "${go_match}" "${adr_stale_title}"

if [ "${FAIL_COUNT}" -gt 0 ]; then
  echo "❌ ${FAIL_COUNT} guard-adr-plugin-taxonomy regression check(s) failed" >&2
  exit 1
fi
echo "✓ all guard-adr-plugin-taxonomy regression checks passed"
