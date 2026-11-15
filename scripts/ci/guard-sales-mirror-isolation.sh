#!/usr/bin/env bash
#
# ut-docs#5074 (ADR-0011 amendment 2026-10-26 §1, design §3.4): every
# additional till keeps a read-only copy of the shop's sales in mirror_*
# tables, and the main till keeps a change log (sales_mirror_log,
# sales_mirror_acks). "A copied sale never counts in this till's totals, Z
# report, stock, vouchers, receipt numbers, cloud uploads or journal" holds
# by construction ONLY while nothing but internal/data/sales_mirror_repo.go
# touches those tables: its read methods return MirrorSale types that no
# report function accepts. A query against mirror_sales from any other file
# is exactly how a copy would start being double counted.
#
# This guard fails when a non-test Go file outside that repository, or any
# .sql file outside internal/db/migrations/, names one of those tables —
# case-insensitively, as SQLite reads identifiers. Full-line // and --
# comments are ignored (prose may name the tables). A deliberate, reviewed exception (e.g. a table-classification
# list that must name every table) carries a same-line
# "sales-mirror:allow <reason>" comment.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

ALLOWED_FILE="internal/data/sales_mirror_repo.go"
# The repository is split by side into sales_mirror_repo.go (main till) and
# sales_mirror_repo_copy.go (additional till): still one SalesMirrorRepo.
ALLOWED_COPY_FILE="internal/data/sales_mirror_repo_copy.go"
PATTERN='(^|[^A-Za-z0-9_])(mirror_sales|mirror_sale_[a-z_]+|mirror_payments|mirror_closes|mirror_state|sales_mirror_log|sales_mirror_acks|peer_closes)([^A-Za-z0-9_]|$)'

if [[ ! -f "${ALLOWED_FILE}" ]]; then
  echo "❌ sales-mirror isolation guard: ${ALLOWED_FILE} is missing" >&2
  echo "   (moved or renamed? update this guard rather than let it go blind)" >&2
  exit 1
fi

violations=""
while IFS= read -r f; do
  [[ -z "${f}" ]] && continue
  case "${f}" in
    "${ALLOWED_FILE}" | "${ALLOWED_COPY_FILE}" | *_test.go | internal/db/migrations/*) continue ;;
  esac
  [[ -f "${f}" ]] || continue
  hits="$(grep -inE "${PATTERN}" "${f}" \
    | grep -vE '^[0-9]+:[[:space:]]*(//|--)' \
    | grep -v 'sales-mirror:allow' || true)"
  if [[ -n "${hits}" ]]; then
    violations+=$'\n'"${f}:"$'\n'"${hits}"$'\n'
  fi
done < <(git ls-files --cached --others --exclude-standard -- '*.go' '*.sql')

if [[ -n "${violations}" ]]; then
  echo "❌ sales-mirror isolation guard: only ${ALLOWED_FILE} may touch the sales copy or its change log" >&2
  echo "   (a copied sale must never reach a report, Z report, stock, voucher, receipt counter," >&2
  echo "   cloud upload or journal — ut-docs#5074). Add a method to SalesMirrorRepo instead." >&2
  echo "   A reviewed exception carries a same-line 'sales-mirror:allow <reason>' comment." >&2
  echo "${violations}" >&2
  exit 1
fi

echo "✓ sales-mirror isolation guard: only ${ALLOWED_FILE} touches the sales copy"
