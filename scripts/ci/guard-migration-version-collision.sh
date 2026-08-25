#!/usr/bin/env bash
#
# Enforces unique migration version numbers (ut-docs#1056, found live: two
# concurrent pipeline lanes independently created internal/db/migrations/
# 067_vouchers.sql and 067_shift_cash_reconciliation.sql, each branched
# from a similar point in history).
#
# internal/db/migration.go's loadMigrations sorts migrations by their
# leading NNN_ version number; sort.Slice has no defined tie-break for two
# equal keys, so a collision could silently let one file win the sort and
# the other lose — not a build failure, not a test failure, a silently
# missing table/column on some fraction of installs (this schema is
# fiscal/compliance-relevant, so that's a real data-loss class, not
# cosmetic). loadMigrations itself now hard-fails on a duplicate too
# (checkNoDuplicateVersions) — this guard is the earlier, faster signal at
# PR time, without needing a Go build.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

MIGRATIONS_DIR="internal/db/migrations"

versions_file="$(mktemp)"
trap 'rm -f "${versions_file}"' EXIT

for f in "${MIGRATIONS_DIR}"/*.sql; do
  [[ -e "${f}" ]] || continue
  name="$(basename "${f}")"
  if [[ "${name}" =~ ^([0-9]+)_ ]]; then
    echo "${BASH_REMATCH[1]} ${name}" >>"${versions_file}"
  fi
done

dup_versions="$(cut -d' ' -f1 "${versions_file}" | sort -n | uniq -d)"

if [[ -n "${dup_versions}" ]]; then
  echo "❌ migration-version-collision guard: duplicate migration version number(s) found" >&2
  while read -r v; do
    [[ -n "${v}" ]] || continue
    echo "  version ${v}:" >&2
    grep "^${v} " "${versions_file}" | awk '{print "    - " $2}' >&2
  done <<<"${dup_versions}"
  echo "   (rename one file's leading number so every version is unique — ut-docs#1056)" >&2
  exit 1
fi

echo "✓ migration-version-collision guard: every ${MIGRATIONS_DIR}/*.sql version number is unique"
