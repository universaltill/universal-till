# README plan matrix and iOS (ut-docs#5222, part of #5223)

**What shipped:** the README's "What's in each plan" matrix now shows all five
plans (Local, Local Plus, Shop, Pro, Multi-location) and every row agrees with
ut-docs `reference/subscription-plans.md` and `plan-prices.md` (archive: Local
Plus 1 year, Shop 2, Pro and up legal retention, UK 7 / Germany 10 / elsewhere
10). iOS (iPhone, iPad; internal testing through TestFlight, no App Store claim)
is added to the highlights, the supported-platforms list and the roadmap lines.
The "Optional Cloud Services" list and the German TSE notes now name Local Plus.

**Review:** independent Opus pass over every matrix cell against the canonical
doc; no blocking findings.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | medium | Cloud-services list called LAN multi-till sync and DATEV paid | fixed |
| 2 | medium | TSE notes said only "free plan" brings its own (Local Plus does too) | fixed |
| 3 | low | Archive row omitted "elsewhere 10 years" | fixed |
| 4 | low | TestFlight wording narrower than #3052 | fixed |
| 5 | low | Order-tracking QR line (README ~93) says "over the shop's network"; plans doc says through our cloud, Shop and up | deferred, covered by the matrix-vs-README check in #5222's CI follow-up |

**Verified beyond tests:** matrix cells, prices and archive years compared line
by line with the canonical docs; website pages checked in the sibling PR.
**Safe to merge:** yes (docs only). **Deferred:** CI check that fails when the
README matrix drifts from the canonical doc (#5222 stays open for it).
