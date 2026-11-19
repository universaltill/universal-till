# Review: payment plugin contract tests, store payment statements, card-plugin help (ut-docs#6565, #6440, #6561)

Date 2026-11-17. Lane sdk. Tester: /tmp/tabletfix/tester-storepay.md (driven desktop run, tablet v0.36.1 sideload, mutation checks). Reviewer: Opus 5.5 subagent, independent of the Sonnet author.

Pre-flight: walked; no UI code, no migration, no new en.json key, no money-path change except the one noted below; docs/tests/manifests only.

## What shipped (this repo's part)
web/help/en/payments.md: section 'Install, set up and update a card plugin'.

## Findings and triage
- Tester G1 weak 404 test: fixed (TestContractLookup404IsNotYetNotAnError; mutation 404->error now fails it).
- Tester G2 refund body: the sandbox proved `{}` only; comment added that `{"amount": major}` is documented, not sandbox-verified.
- Tester G4/G5, reviewer 4: scope of the fakes stated in headers and CLAUDE.md (paths/bodies/idempotency, not headers/auth; Stripe fake is from docs, not a recording; client_transaction_id lookup and reply id field assumed).
- Tester D1 raw key names as labels: not fixed here, card ut-docs#6570; help names fields by key meanwhile (D2 fixed).
- Tester D3-D6 and reviewer 2, 3, 9 (store statements): Play REQUEST_INSTALL_PACKAGES claim corrected (every v0.36.1 build still declares it; the play flavor #6388/#2583 is unmerged), Solo-only and Stripe reader models sourced from the manifests/READMEs, affiliate key is global, card-data claim flagged as a placeholder, language packs auto-update wording.
- Reviewer 1 (medium): help said Stripe without a reader id works with a live key; it only works with sk_test_ (pm_card_visa) and declines with sk_live_. Fixed.
- Reviewer 5-8: subtest renamed, comment moved, missing assertions added, stray call removed, Install step added.
- Real bug found by the new suite (SumUp reader.go): the checkout reply was only read as `data.client_transaction_id`; the sandbox note records it flat. A flat reply would be treated as a lost reply and every reader sale declined. Fix reads both; only on 2xx; 4xx/5xx/id-less paths unchanged, so no path turns a decline or unknown into an approval (reviewer confirmed; the test fails on the old code).

## Verification
go vet, GOOS=wasip1 vet, `go test -race -count=1`, build.sh, validate.sh in both plugins; mutation checks; help guards (topics, terms, drift, breadcrumbs). Not verified: any real SumUp/Stripe exchange from a plugin, Android settings/payment screens (tablet holds real shop data, nothing installed), the Play build, package.sh output.

## Verdict
Approve. Deferred: ut-docs#6570 (labels, distinct bad-key reason); Stripe simulated-reader run waits for Key Vault stripe-test-terminal-key; Play-build verification waits for #2583.
