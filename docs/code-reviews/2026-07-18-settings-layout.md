# Code review — settings page layout (field: "unaligned and messy") (2026-07-18)

Branch `fix/settings-layout`. The settings page had grown to 12 stacked
cards with 51 ad-hoc inline styles — one endless uneven column.

- **Responsive card grid**: `.settings-grid` (auto-fit minmax 24rem) — cards
  sit side by side on wide screens, single column on narrow/touch; uniform
  gaps replace per-card margin hacks.
- **Aligned form rows**: repeated inline flex patterns normalized to
  `.set-row` (+ tight/below/gap/divider utilities); consistent control
  heights across selects/inputs. Inline styles 51 → 26 (the rest are small
  width hints inside rows — harmless).
- No ids/htmx attributes touched — pure structure/classes; all pages tests
  green and a live render smoke confirms the grid + rows.

Follow-up candidates (queued under polish): the same treatment for the
plugins and users pages if Farshid flags them.
