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

# Every embed point uses the shared theme-aware brand-mark element
# (ut-docs#298), not a raw <img src="unitill-logo.svg"> — the CSS mask is
# what points at the canonical file now, so assert that instead of an <img>
# per-template. See app.css: .brand-mark is filled with currentColor, which
# is what makes a single asset correct on both light and dark surfaces.
for template in \
  "$root/web/ui/partials/nav.html" \
  "$root/web/ui/pages/login.html" \
  "$root/web/ui/pages/setup.html" \
  "$root/web/ui/pages/self_order.html"; do
  grep -Eq 'class="brand-mark' "$template"
done
! grep -REq 'ut-logo-name(-light)?\.svg' "$root/web/ui"
! grep -REq '<img[^>]+unitill-logo\.svg' "$root/web/ui"

# The mask must still point at the real canonical file (a swapped path would
# silently break every surface at once, same failure class the sha256 check
# above guards against for the file itself).
grep -Eq 'mask:.*unitill-logo\.svg' "$root/web/public/app.css"
grep -Eq 'background-color: currentColor' "$root/web/public/app.css"

# ut-docs#298: the white plate behind the mark WAS the bug (a light tile
# pasted onto the till's dark header) — assert it stays gone, not present.
# A hardcoded background on any of these selectors is a regression back to
# the pre-#298 behaviour.
! grep -REq '^\.(logo|login-logo|selforder-logo)(,|\s*\{)[^}]*background: #fff' "$root/web/public/app.css"
