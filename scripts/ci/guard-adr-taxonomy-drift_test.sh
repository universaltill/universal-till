#!/usr/bin/env bash
#
# Regression test for guard-adr-taxonomy-drift.sh (ut-docs#2159): proves the
# guard catches a stale plugin-taxonomy count restated in an ADR OTHER than
# ADR-0002, while NOT flagging a legitimate historical count narrated as
# part of that ADR's own decision (the ADR-0010/ADR-0050 shape) — the false
# positive that a naive version of this guard would produce, and the whole
# reason the `<!-- taxonomy-count:historical -->` marker exists. Covers:
# a stale "N-type taxonomy" mention fails; a stale "(N canonical types)"
# mention fails; a stale "N types (ADR-0002..." mention fails; the same
# stale wording WITH the historical marker passes; a correct (live-count)
# mention passes; ADR-0002's own file is never scanned by this guard (that's
# guard-adr-plugin-taxonomy.sh's exclusive territory); and DOCS_DIR/explicit
# args pointing at nothing is a hard failure, never a silent skip.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-adr-taxonomy-drift.sh"
FAIL_COUNT=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

write_go_fixture() {
  # $1 = output path, $2.. = types (22 real ones by default in callers)
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

BASE_TYPES=(page button popup payment device integration report pricing tax
  import export hardware background_job scheduler receipt_template
  customer_facing auth notification delivery theme language layout)
# 22 types — must match BASE_TYPES' actual count, asserted below rather
# than hand-copied as a second source of truth.
LIVE_COUNT="${#BASE_TYPES[@]}"

GO_FIXTURE="${TMPDIR}/manifest_verifier.go"
write_go_fixture "${GO_FIXTURE}" "${BASE_TYPES[@]}"

fresh_adr_dir() {
  local d="${TMPDIR}/adr_$1"
  mkdir -p "${d}"
  printf '%s' "${d}"
}

expect_pass() {
  local label="$1" go="$2" dir="$3"
  if bash "${GUARD}" "${go}" "${dir}" >/tmp/guard_adr_drift_test_out.$$ 2>&1; then
    echo "✓ guard correctly passed ${label}"
  else
    echo "❌ FAIL: expected guard to pass ${label}, but it rejected it" >&2
    cat /tmp/guard_adr_drift_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_adr_drift_test_out.$$
}

expect_fail() {
  local label="$1" go="$2" dir="$3"
  if bash "${GUARD}" "${go}" "${dir}" >/tmp/guard_adr_drift_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_adr_drift_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_adr_drift_test_out.$$
}

if [ "${LIVE_COUNT}" -ne 22 ]; then
  echo "❌ FAIL: test fixture's own BASE_TYPES count is ${LIVE_COUNT}, expected 22 — fixture drifted from the real taxonomy, fix the test" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# 1. A stale "N-type taxonomy" mention (the real ADR-0014/0023 shape) → fail.
d1="$(fresh_adr_dir 1)"
cat > "${d1}/0014-erp-integration-connectors.md" <<'EOF'
# ADR-0014: ERP / external-system integration via connector plugins

- **Relates to:** ADR-0002 (20-type taxonomy — `integration`/`import`/`export`)
EOF
expect_fail "stale N-type taxonomy mention" "${GO_FIXTURE}" "${d1}"

# 2. A stale "(N canonical types)" mention → fail.
d2="$(fresh_adr_dir 2)"
cat > "${d2}/0099-fake.md" <<'EOF'
# 0099 — Fake ADR for testing

Some prose citing the wrong count (20 canonical types) in passing.
EOF
expect_fail "stale (N canonical types) mention" "${GO_FIXTURE}" "${d2}"

# 3. A stale "N types (ADR-0002..." mention (the real ADR-0050 shape,
#    MINUS the historical marker it actually carries) → fail.
d3="$(fresh_adr_dir 3)"
cat > "${d3}/0050-legal-obligation-boundary.md" <<'EOF'
# ADR-0050: Legal obligation boundary

Rejected at the time this ADR was written: the taxonomy was fixed at 21 types (ADR-0002, amended by ADR-0010,
before ADR-0088 Decision B's later addition took it to 22).
EOF
expect_fail "stale N types (ADR-0002 mention, no marker" "${GO_FIXTURE}" "${d3}"

# 4. The exact same ADR-0050-shaped wording, WITH the historical marker on
#    the line carrying the stale-looking number → pass. This is the guard's
#    whole reason for existing in this shape: without the marker mechanism,
#    this legitimate historical narration would be indistinguishable from
#    case 3 above and the guard would have to be disabled the moment a real
#    ADR like this one was written.
d4="$(fresh_adr_dir 4)"
cat > "${d4}/0050-legal-obligation-boundary.md" <<'EOF'
# ADR-0050: Legal obligation boundary

Rejected at the time this ADR was written: the taxonomy was fixed at 21 types (ADR-0002, amended by ADR-0010, <!-- taxonomy-count:historical -->
before ADR-0088 Decision B's later addition took it to 22).
EOF
expect_pass "historical N types (ADR-0002 mention WITH marker" "${GO_FIXTURE}" "${d4}"

# 4b. Same idea for the ADR-0010 shape specifically (the other real
#     historical mention this guard must not flag).
d4b="$(fresh_adr_dir 4b)"
cat > "${d4b}/0010-language-type.md" <<'EOF'
# 0010 — `language` joins the plugin taxonomy

The product owner asked for language packs as plugins; the 20-type taxonomy (ADR-0002) <!-- taxonomy-count:historical -->
had no slot for them.
EOF
expect_pass "historical N-type taxonomy mention WITH marker" "${GO_FIXTURE}" "${d4b}"

# 5. A mention with the CURRENT, correct count → pass regardless of shape.
d5="$(fresh_adr_dir 5)"
cat > "${d5}/0088-declarative-ui-slot-registry.md" <<'EOF'
# ADR-0088: Declarative UI slot registry

Taxonomy after this ADR (22 types; note `language` was already added after ADR-0010).
EOF
expect_pass "correct current-count mention" "${GO_FIXTURE}" "${d5}"

# 6. ADR-0002's own file is never scanned by this guard — that guard
#    (guard-adr-plugin-taxonomy.sh) owns it exclusively, and this guard
#    must not report the same drift a second time under a different name.
#    Put an otherwise-guaranteed-to-fail stale mention in a file literally
#    named 0002-*.md and confirm the guard still passes.
d6="$(fresh_adr_dir 6)"
cat > "${d6}/0002-plugin-type-taxonomy.md" <<'EOF'
# 0002 — Plugin type taxonomy (20 canonical types)

Deliberately wrong — this file is guard-adr-plugin-taxonomy.sh's exclusive
territory and must be skipped by guard-adr-taxonomy-drift.sh entirely.
EOF
expect_pass "0002-*.md is skipped, even with a stale mention inside it" "${GO_FIXTURE}" "${d6}"

# 7. A missing ADR directory, passed explicitly, is a hard failure — never
#    a silent skip (mirrors guard-adr-plugin-taxonomy_test.sh's own
#    equivalent case, and for the same reason: a skip here would make CI
#    silently green forever on exactly the drift this guard exists to
#    catch).
expect_fail "missing ADR dir (explicit arg)" "${GO_FIXTURE}" "${TMPDIR}/does-not-exist-dir"

# 8. A missing Go file, passed explicitly, is also a hard failure.
expect_fail "missing Go file (explicit arg)" "${TMPDIR}/does-not-exist.go" "${d5}"

if [ "${FAIL_COUNT}" -gt 0 ]; then
  echo "❌ ${FAIL_COUNT} guard-adr-taxonomy-drift regression check(s) failed" >&2
  exit 1
fi
echo "✓ all guard-adr-taxonomy-drift regression checks passed"
