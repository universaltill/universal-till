#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
canonical="$root/web/public/assets/logo/unitill-logo.svg"

# The artwork the product owner supplied for ut-docs#290, mirrored from
# ut-docs/logo/unitill-logo.svg. Assert the content, not just the filename:
# the first attempt at that card shipped the previous logo renamed to
# unitill-logo.svg, and a filename-only guard accepted it. Keep this hash in
# step with ut-docs/scripts/check-brand-assets.sh.
CANONICAL_SHA256="d4816d6daa622b47d3cb160058ec7368dd6e45800624e0855688d0e50d228221"

test -s "$canonical"

# sha256sum on Linux/CI, shasum on macOS — neither is present on both.
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$canonical" | cut -d' ' -f1)"
else
  actual="$(shasum -a 256 "$canonical" | cut -d' ' -f1)"
fi
if [ "$actual" != "$CANONICAL_SHA256" ]; then
  echo "web/public/assets/logo/unitill-logo.svg does not match the canonical supplied mark." >&2
  echo "  expected $CANONICAL_SHA256" >&2
  echo "  actual   $actual" >&2
  exit 1
fi

for template in \
  "$root/web/ui/partials/nav.html" \
  "$root/web/ui/pages/login.html" \
  "$root/web/ui/pages/setup.html" \
  "$root/web/ui/pages/self_order.html"; do
  grep -Eq 'unitill-logo\.svg' "$template"
done
! grep -REq 'ut-logo-name(-light)?\.svg' "$root/web/ui"

# The mark is a black silhouette on a transparent background, so every surface
# that can render on a dark theme needs a light plate behind it or the logo
# disappears. Keep that coupling visible to anyone editing the CSS.
grep -Eq '^\.login-logo, \.selforder-logo \{' "$root/web/public/app.css"
grep -Eq 'background: #fff' "$root/web/public/app.css"
