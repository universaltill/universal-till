---
id: my-reports
title: My reports
section: Connecting & extending
order: 361
summary: "See the problem reports this till has sent (most recent 100), with their last-known status — works offline too."
routes: [/my-reports]
keywords: [bug, issue, report, status, sent, github, tracking]
---

# My reports

See the problem reports this till has sent (the most recent 100), with their last-known status — works offline too.

## What the page shows

Each row is one report this till uploaded: when it was captured, what it contained (your typed note, plus tags for a voice note, screen recording, or screenshots), and its current status. When a report has been turned into a GitHub issue, a **View on GitHub** link appears next to it.

The statuses mean:

- **Sent, awaiting review** — uploaded from this till; the cloud hasn't reported anything further yet.
- **Received / Transcribing / Ready for review** — the report is being processed (voice notes are transcribed automatically).
- **Filed on GitHub** — it became a tracked issue; follow the link to see progress.
- **Discarded** — it was reviewed and closed without filing an issue.

## Offline

The page never needs the network: it always shows the statuses from the last time this shop was online, and refreshes them automatically in the background once you're connected again. A report you've just saved appears here shortly after it uploads.

Open it from the report panel's **View my reports** link (🐞 button in the top bar — see the Reporting a problem topic).
