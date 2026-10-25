# Review: price and commission sweep (owner decision 2026-10-24)

**Date:** 2026-10-24 · **Built by:** Sonnet 5.5 · **Independent review:** Fable (money/price), findings below.
Decision: Local 0; Local Plus 10; Shop 29; Pro 49; Multi-location 120; extra location 33 (to 10); annual = two months free; Germany +EUR 12 per registered main till (Shop and up). Partner commission 25% growing to 35%, nothing else.

## What changed
- README.md pricing section: ladder now Local 0 / Local Plus 10 / Shop 29 / Pro 49 / Multi-location 120 in GBP and EUR with annual per-month figures; extra location 33; managed TSE EUR 12 per registered main till (Shop and up) replaces 'TSE included'; removed GCC/Turkey/rest-of-world rows and the German founding-customer line (built on the retired ladder).
- No price strings exist in web/locales, web/help or Go code.

## Review findings
- Major (my-shop only), fixed: the German guard regex used an ASCII space but Intl emits U+00A0, so it never matched; now uses \s and the retired numbers (42, 54).
- Minor, fixed: README claimed "prices on request" for markets the decision does not cover; now "not yet published".
- Open (owner / ut-docs): README link to ut-docs reference/plan-prices.md resolves once the sibling ut-docs PR is merged; the README "~44% under incumbents" figure was computed on the old ladder and is unverified.
