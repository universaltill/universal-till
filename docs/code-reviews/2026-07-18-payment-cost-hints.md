# Code review — payment fee rules + checkout cost hints (2026-07-18)

Branch `feat/payment-cost-hints`. Payments B4 + the C2 cost-hint half
(ADR-0016 manual mode: the cashier sees which provider is cheaper for THIS
basket).

- Settings → Payments: per-provider fee rows (percent + fixed per
  transaction, manager-only). Stored as `payments.fee.<method>` JSON
  {bp, fixed} — percent entered in %, stored basis points; fixed entered in
  major units, stored minor (money boundary rule). LAN-synced shop-wide.
- Sale page: fee rules ride as JSON; each Pay-tab button gains a live
  "≈ −£0.45" hint computed client-side from the basket total (re-computed
  on every basket swap via the total's existing data-minor). No fee rule =
  no hint; zero total = no hint.
- i18n ×4 (help text explains the point: pick the cheaper provider).

Estimates only — actual provider fees settle elsewhere; the hint's job is
relative comparison. Suites + both guards green.
