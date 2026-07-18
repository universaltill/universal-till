# Code review — preferred payment method (2026-07-18)

Branch `feat/preferred-payment`. Payments manual-mode slice (ADR-0016 §2a:
"merchant default"): the shop's cheaper/house provider should be the
one-tap first choice at checkout.

- Setting `payments.default_method` (Settings → Payments card, manager-only,
  dropdown of active methods incl. plugin-provided; i18n ×4).
- The sale screen reorders both tender UIs (Pay tab rows + split-tender
  select) so the preferred method leads; no behavior change when unset or
  when the method is inactive (it simply isn't in the list).
- Setting rides LAN sync like other shop settings → all tills agree.

Cost HINTS (per-provider fee guidance) remain with B4 cost-rules. Suites +
both guards green.
