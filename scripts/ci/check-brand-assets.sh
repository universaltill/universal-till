#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
canonical="$root/web/public/assets/logo/unitill-logo.svg"

test -s "$canonical"
for template in \
  "$root/web/ui/partials/nav.html" \
  "$root/web/ui/pages/login.html" \
  "$root/web/ui/pages/setup.html" \
  "$root/web/ui/pages/self_order.html"; do
  rg -q 'unitill-logo\.svg' "$template"
done
! rg -q 'ut-logo-name(?:-light)?\.svg' "$root/web/ui"
