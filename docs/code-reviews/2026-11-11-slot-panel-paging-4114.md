# Review: slot panel paging (ut-docs#4114)

Independent review by an Opus subagent (lane:cloud-54, 2026-11-11), plus the
Tester's mutation run (17 of 20 mutants killed). Record added at merge time;
the original findings were in the card comment only.

| Id | Finding | Outcome |
|----|---------|---------|
| M1 | Two paged tables in one panel shared the indicator id `<panel>-pos`; focus and `aria-describedby` hit the wrong table | Fixed: `ViewPaging.PosID` is `<panel>-pos<n>` per table; app.js focus scoped to the paged table |
| D1/L4 | e2e in-flight test false pass (forced clicks under `pointer-events:none`) | Fixed: taps use `dispatchEvent`, so only `hx-sync` can drop them; "panel B stays pageable" dropped from the title (`paged_b` does not page) |
| L1 | `admin.pages` draws no panels, yet a hand-made panel GET was asked under it | Fixed: `pluginSlotDrawsPanels`; `pluginSlotPanelOf` is false for it |
| L5/D2 | "tabs unchanged" check could pass vacuously; no test that a request `slot` is overwritten | Fixed: tabs asserted visible, no `.catch`; Go test for the entry route |
| L2, L3 | Slow answer of item A can show its notice over B; network failure shows no notice | Accepted / backlog |
| - | POST/poll panel-id check not bound to the slot rule; Next during a panel action POST not synced | Backlog |
