#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
canonical="$root/web/public/assets/logo/unitill-logo.svg"
light="$root/web/public/assets/logo/unitill-logo-light.svg"

# The artwork the product owner supplied for ut-docs#290, mirrored from
# ut-docs/logo/unitill-logo.svg. Assert the content, not just the filename:
# the first attempt at that card shipped the previous logo renamed to
# unitill-logo.svg, and a filename-only guard accepted it. Keep this hash in
# step with ut-docs/scripts/check-brand-assets.sh. Still the source of truth
# for the favicon/app-icon/installer/Android-launcher rasterizations (ut-docs#298
# left those on the dark mark deliberately — OS-chrome icons carry their own
# background tile by platform convention, so they were never the "white patch
# on dark chrome" defect that card fixed).
CANONICAL_SHA256="d4816d6daa622b47d3cb160058ec7368dd6e45800624e0855688d0e50d228221"

# ut-docs#298: the .nav-only light/white-glyph variant, transparent
# background. NOT used on login/setup/self-order — an independent review
# found that a light glyph there (behind a var(--brand) plate) just
# relocated the reported "patch pasted on" defect from white-on-dark to
# navy-on-white, on surfaces that had no defect to begin with (--surface is
# white in every shipped theme, so the canonical dark mark already reads
# cleanly there with no plate at all). Must stay a pure recolor of the same
# path data: pin its hash too so the two can't silently drift apart (e.g.
# someone touches up one mark's geometry and forgets the other).
LIGHT_SHA256="1a367e13b983d430c57cd4ac83b7092bf64699490a98cf669911721a3552e034"

check_hash() { # check_hash <file> <expected> <label>
  test -s "$1"
  # sha256sum on Linux/CI, shasum on macOS — neither is present on both.
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$1" | cut -d' ' -f1)"
  else
    actual="$(shasum -a 256 "$1" | cut -d' ' -f1)"
  fi
  if [ "$actual" != "$2" ]; then
    echo "$1 does not match the canonical $3 mark." >&2
    echo "  expected $2" >&2
    echo "  actual   $actual" >&2
    exit 1
  fi
}
check_hash "$canonical" "$CANONICAL_SHA256" "dark"
check_hash "$light" "$LIGHT_SHA256" "light"

# .nav is the ONLY surface guaranteed dark in every shipped theme — it alone
# gets the light-glyph mark. login/setup/self-order stay on the canonical
# dark mark (checked further down, alongside the two e2e suites that assert
# the same split — tests/e2e/tests/pos_ui_mvp.spec.ts and
# e2e/tests/{login,sale-screen-213,self-order-brand-mark-298}.spec.ts — so a
# regression here can't silently slip past this guard the way the bare
# filename-only version of this check once did, ut-docs#290).
#
# Match the bare "unitill-logo-light" marker (-F, fixed string), NOT
# "...-light.svg": one of the two e2e-suite checks below searches inside a
# TypeScript regex LITERAL (`/unitill-logo-light\.svg/`), whose source text
# has a real backslash character between "light" and the dot. A pattern
# ending "...-light.svg" (however it's escaped) then requires that exact
# substring, which doesn't exist verbatim in that file — this silently
# failed to find a match that was right there (caught testing this exact
# guard change). The bare marker has no such trap and reads identically in
# either file kind, since only the ".svg"/"\.svg" tail differs between them.
grep -Fq 'unitill-logo-light' "$root/web/ui/partials/nav.html"
for template in \
  "$root/web/ui/pages/login.html" \
  "$root/web/ui/pages/setup.html" \
  "$root/web/ui/pages/self_order.html"; do
  grep -Fq 'unitill-logo.svg' "$template"
  ! grep -Fq 'unitill-logo-light' "$template"
done
grep -Fq 'unitill-logo-light' "$root/tests/e2e/tests/pos_ui_mvp.spec.ts"
! grep -RFq 'ut-logo-name-light.svg' "$root/web/ui"
! grep -RFq 'ut-logo-name.svg' "$root/web/ui"

# Scoped to the actual .login-logo, .selforder-logo rule (not `grep -Eq
# 'background: transparent'` over the whole file, which half a dozen
# unrelated rules would also satisfy) — a plate reappearing here silently
# reintroduces the patch-on-white-card regression this guard exists to
# catch.
awk '/^\.login-logo, \.selforder-logo \{/{f=1} f{print; if (/\}/) exit}' "$root/web/public/app.css" \
  | grep -Fq 'background: transparent'
