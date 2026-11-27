# Till self-heal chip, messages, Tills page and help (ut-docs#7097, ADR-0217 slice T2)

Status: **stub, the Reviewer completes it** (a different-model pass over the
diff is required by the pipeline; none has run on this code yet).

Scope: `internal/pages/cloud_heal_chip.go` (one view for chip, Tills page,
Settings notice and Backups line), `cloud_auth_chip.go`, `tills_tabs.go`,
`settings_page.go`, `offsite_backup_api.go`, the chip / Tills / Settings
templates, `app.css` (hit area, self-healing chip, phone drawer), the English
keys and metadata, the join-screen wording, help topics `claim`, `multitill`,
`backups`, `plugins`, and `e2e/tests/cloud-heal-states-7097.spec.ts`. Binding
inputs: design.md section 9 as amended by `review-adr.md`; wording and placement
from `ux-notes.md`. Stacked on the engine PR (#2760): `internal/credheal` is not
touched.

## To attack

1. Exactly ONE control per state: the status-bar pill is the link (short words,
   a dot, the button label), never a second link; a cashier gets words and the
   ask-a-manager title, no link.
2. A state that fixes itself (`healing`, `waiting`) shows no banner, no button
   and no amber pill; on a phone it does not bring the status row back above the
   pay bar (`.sb-cloud-auth:not(.is-selfhealing)`).
3. No token, key, hash, device ID or address is rendered anywhere new.
4. Chip, Tills banner, Settings notice and Backups line use the same sentence
   key per reason; the Backups line says "paused, reconnects by itself" versus a
   person step.
5. The code button lands on `/settings?reconnect=1#pair-details` with the field
   open and nothing focused (no autofocus, no second keyboard).
6. Join wording: the two sales warnings were not merged; the refusal still
   carries the (reworded) warning text.
7. Help topics say "reconnects by itself" first, "code" second, and never
   "pair again" as the first answer.

## Findings

(none yet)
