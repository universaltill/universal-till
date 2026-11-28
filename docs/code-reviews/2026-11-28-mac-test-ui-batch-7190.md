# Mac-instance test UI batch (ut-docs#7190 #7191 #7192 #7193 #7194 #7195 #7196 #7197 #7199 #7201)

Part of ut-docs#7175. Source: reviewer verdict of the Mac-instance UI test (ids A1, A2, A5 wording, A6, B2, B3, B4, C4, E2, E4).

## Shipped
- Preview: taller capped body with scroll-shadow cue, focusable region, safety line outside the scroller (#7190).
- Title uses the "set up" wording when no type is set; unit test seen failing first (#7191).
- `.pairing-notice` on the AA-checked `--notice-info-*` tokens (#7192).
- en.json wording only (ADR-0160): undo banner/dismiss (#7193), preview hidden group (#7194), Hidden menu tiles page (#7195); help topic and meta descriptions follow. Keeping Undo on dismiss is a separate card.
- `.menulayout-goto` 44px (#7196); `.picker-tile` hover token in `@media (hover: hover)` (#7197).
- Backup cloud card: blocked status no longer claims a protected copy (#7199); notice next to the key status (#7201, focus work skipped).

## Review (Opus 5.5 subagent, read-only)
Pre-flight: gofmt, vet, golangci-lint, i18n/rtl/help/compliance guards, go test pages/shoptemplate/httpx/web all green.
- Important: e2e `template-matrix-6332.spec.ts:145` asserted the old wording. Fixed.
- Nit: help topic display.md stale (Dismiss, More settings). Fixed.
- Nit: en.meta.json descriptions for group_hidden and undo.dismiss stale. Fixed.
- Nit: `backup.cloud.key.blocked` reads as a fragment alone; it is `review: legal`, so a wording change is deferred to the legal route (with E3/E3b).
Visual check at 1280x800 and 390x844, light and dark: OK.

Verdict: approve.
