# Code review — printer discovery reports HTTP failures (ut-docs#1556 follow-up)

**Change:** both LAN printer-discovery scripts — the original on
`/kitchen-stations` and the copy ut-docs#1556 added to Settings → Printer —
now check `r.ok` before `r.json()`, so a non-2xx reaches the existing
`.catch()` and shows the error message.

## The defect

```js
fetch('/api/kitchen-stations/discover-printers')
  .then(function (r) { return r.json(); })
  .then(function (j) {
    var list = (j.data && j.data.printers) || [];
    if (!list.length) { msg.textContent = i18n.noneFound; return; }
```

A 403 (a cashier reaching the manager-gated endpoint) or a 500 (the scan
itself failing) still parses as JSON and simply has no `data.printers`, so
`list` is `[]` and the operator is told **"No printers found on this
network"** — a sentence that sends them to check cables, switches and the
printer's own network settings for a fault that is on the server. The
`.catch()` that exists precisely to say "could not search this network"
was unreachable for every failure that returned a body.

Not caught by ut-docs#1556's own review because the endpoint always
answered 200 in every path exercised there.

## Verification (TDD, actually run)

- New spec `e2e/tests/printer-discovery-http-error-1556.spec.ts` stubs the
  endpoint with a 500 and a well-formed JSON body, then drives the real
  button on **both** pages.
- **Ran it against the unfixed templates first** (`git stash` of the two
  HTML files): 2 failed, both on
  `expect(msg).toHaveText('Could not search this network for printers.')` —
  the DOM showed the "none found" string, exactly the reported defect.
- Restored the fix: 2 passed.
- The spec asserts the negative too (`not.toHaveText(noneFound)`) and that
  the button is handed back so the operator can retry.
- `watchConsole` is used with `extraExempt: /^Failed to load resource:.*500/`
  (helpers.ts, ut-docs#916) — Chromium logs that line for the stubbed
  response however the page handles it. Every other console error still
  fails the test.
- `guard-i18n.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh`, `go build
  ./...` all pass. No new user-facing strings (both messages already
  existed and were already translated in all four locales), so no
  `ut-plugin-language-*` follow-up is needed for this change.
- Screenshots regenerated: only `manifest.json` changed — the surface hash
  moved because inline JS is hashed, but no rendered pixel did.

## Note

The two scripts are near-identical copies, which is why one bug needed
fixing twice. Extracting a shared partial is a real cleanup but out of
scope here; left unfiled rather than pretending it was addressed.
