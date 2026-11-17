# Review: Turkish help names the Unhide button as the UI shows it (ut-docs#2660)

Date: 2026-10-31. Branch `fix/2660-tr-help-unhide-label`. Lane `lane:cloud-41`.
Built inline by the orchestrator (Opus); reviewed by an independent Sonnet
subagent in a fresh context (complexity:easy, one review pass).

## What shipped

- `web/help/tr/till-designer.md` step 5: the per-item button in the
  "Hidden from sell screen" list was called **Gizliliği kaldır**. The UI
  renders `designer.hidden.unhide` (`web/ui/partials/buttons.html`), which
  is **Göster** in `web/locales/tr.json`. The help now says **Göster**.
- `web/help/img/manifest.json`: the `till-designer`/`tr` topic hash follows
  the edited markdown. The screenshot is of the till route, so a help-prose
  edit can't change its pixels; no PNG regenerated.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1 | info | "bir **Göster** düğmesiyle gösterir" repeats the verb; correct Turkish | Accepted; the UI label stays verbatim |
| R2 | info | The other step-5 labels (title, unhide_all, empty text) match `tr.json` | No change |
| R3 | info | en, ar, fa name the button as their locale renders it; no other mismatch | No change |
| R4 | info | `web/help/de/till-designer.md` can't be checked here: German ships as `ut-plugin-language-de` | Not filed; the de help says "Einblenden", which matches a standard German UI label, and `guard-help-drift` keeps structure aligned |

## Verified

- `guard-docs-shots.sh`, `guard-help-topics.sh`, `guard-help-drift.sh`,
  `guard-help-breadcrumbs.sh`, `guard-compliance-claims.sh` pass (run by the
  reviewer and the orchestrator). The other `ci.yml` build-job guards pass,
  except two that fail only on this container's tooling (deadcode binary
  built with go1.26, no shellcheck binary); CI runs both.
- `go test ./internal/manual/` passes (the in-till manual renders the topic).
- No string of "Gizliliği kaldır" remains under `web/`.

## Verdict

Safe to merge. No deferred items.
