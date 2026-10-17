#!/usr/bin/env bash
# Guard: every @universaltill.com mail address in a tracked file must be on the
# allow-list below -- i.e. a real alias on the support mailbox, a send-only
# sender, or test/example data.
#
# WHY (ut-docs#4227, ut-docs#4229): for months no @universaltill.com address
# received mail (the domain had no MX) and nothing noticed, so docs, OpenAPI
# specs and UI copy told users to write to mailboxes that did not exist. The
# M365 tenant now has real aliases; this guard stops a made-up one creeping
# back in (api-support, marketplace-support, ai-plugins ...).
#
# THE LIST: the canonical copy of this script is ut-docs
# `scripts/guard-mail-addresses.sh`. The list below changes ONLY together with
# the matching M365 alias (add the alias first), and ALL FOUR copies must stay
# byte-identical (`cmp` them):
#   ut-docs/scripts/guard-mail-addresses.sh
#   universal-till/scripts/ci/guard-mail-addresses.sh
#   ut-cloud/scripts/ci/guard-mail-addresses.sh
#   ut-infra/scripts/guard-mail-addresses.sh
# The same goes for guard-mail-addresses_test.sh. Reference page:
# ut-docs reference/mail-addresses.md.
#
# WHAT IT SCANS: tracked files only (`git grep`), text files only. An address
# is `<local>@universaltill.com` NOT followed by a letter, digit, hyphen or a
# further `.label`, so `@universaltill.community`, `@universaltill.com.au` and
# subdomain addresses (`x@cloud.universaltill.com`, Zitadel login names like
# `admin@universaltill.id.universaltill.com`) are not matched -- they are not
# our mailboxes. A sentence-ending period is fine. The whole local part is
# compared lowercased, so a `+tag` is not allowed. URL userinfo on the bare
# domain (`https://<user>@universaltill.com/`) is matched too; none exists.
#
# EXCEPTION: a line containing `mail-address:allow <reason>` is exempt. The
# reason is mandatory in review -- a bare marker makes this guard a formality.
#
# Usage: guard-mail-addresses.sh [ROOT]   (ROOT defaults to the git toplevel
# of this script's own directory)
set -euo pipefail

# Real aliases on the support mailbox (M365, 2026-10-13).
INBOUND=(support hello security partners plugins docs licensing notify homelab)
# Send-only senders: nothing is delivered to them.
SEND_ONLY=(noreply no-reply)
# Test/example data only: ut-cloud Go tests (admin*), the example plugin
# manifest in docs (developer).
TEST_EXAMPLE=(admin admin1 admin2 developer)

ALLOWED=" ${INBOUND[*]} ${SEND_ONLY[*]} ${TEST_EXAMPLE[*]} "
DOMAIN="universaltill.com"
MARKER="mail-address:allow"
RE="[A-Za-z0-9._%+-]+@universaltill\\.com([^A-Za-z0-9.-]|\\.([^A-Za-z0-9-]|\$)|\$)"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="${1:-$(git -C "${here}" rev-parse --show-toplevel)}"

checked=0
fail=0

# git grep exits 1 for "no match"; anything above that (ROOT missing, not a
# git repo) is an error, never a silent pass.
grep_rc=0
hits="$(git -C "${ROOT}" grep -nIiE "${RE}")" || grep_rc=$?
if [[ "${grep_rc}" -gt 1 ]]; then
  echo "guard-mail-addresses: git grep failed in '${ROOT}' (exit ${grep_rc})" >&2
  exit 2
fi

# git grep -n prints "path:line:content"; read splits on the first two colons.
while IFS=: read -r file lineno content; do
  case "${content}" in
    *"${MARKER}"*) continue ;;
  esac
  while IFS= read -r match; do
    local_part="${match%%@*}"
    # Drop whatever delimiter the regex consumed after the domain.
    addr="${match:0:$((${#local_part} + 1 + ${#DOMAIN}))}"
    checked=$((checked + 1))
    case "${ALLOWED}" in
      *" ${local_part,,} "*) ;;
      *)
        echo "${file}:${lineno}: ${addr}" >&2
        fail=1
        ;;
    esac
  done < <(printf '%s\n' "${content}" | grep -oiE "${RE}")
done < <(if [[ -n "${hits}" ]]; then printf '%s\n' "${hits}"; fi)

if [[ "${fail}" -ne 0 ]]; then
  cat >&2 <<EOT

guard-mail-addresses: the address(es) above are not on the allow-list
(ut-docs#4229). Nothing is delivered to an address that is not a real alias.
Use one of the existing local parts: ${INBOUND[*]}
(send-only: ${SEND_ONLY[*]}). To introduce a new one, add the M365 alias first,
then add it to the list in all four byte-identical copies of this script in the
same change -- see ut-docs reference/mail-addresses.md. A reviewed exception
takes a same-line '${MARKER} <reason>'.
EOT
  exit 1
fi

echo "✓ guard-mail-addresses: ${checked} @${DOMAIN} address(es) checked, all on the allow-list"
