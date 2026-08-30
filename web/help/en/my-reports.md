---
id: my-reports
title: My reports
section: Connecting & extending
order: 361
summary: "See the problem reports this till has captured — sent or still pending (most recent 100) — with their last-known status. Works offline too."
routes: [/my-reports]
keywords: [bug, issue, report, status, sent, pending, github, tracking]
---

# My reports

See the problem reports this till has captured — sent, or still waiting to send (most recent 100) — with their last-known status. Works offline too.

## What the page shows

Each row is one report this till has captured — sent or not — with when it was captured, what it contained (your typed note, plus tags for a voice note, screen recording, or screenshots), and its current status. When a report has been turned into a GitHub issue, a **View on GitHub** link appears next to it.

If this till has sent more than 100 reports, a line under the intro tells you how many aren't shown — they come back into view as older ones get filed or discarded.

The statuses mean:

- **Saved here, waiting to send** — captured on this till, not uploaded yet (normal while the shop is offline).
- **Couldn't send** — this report has been failing to upload for a while; a short reason appears underneath it (for example, finishing this till's enrolment). It's still saved and the till keeps retrying automatically — nothing is lost.
- **Sent, awaiting review** — uploaded from this till; the cloud hasn't reported anything further yet.
- **Received / Transcribing / Ready for review** — the report is being processed (voice notes are transcribed automatically).
- **Filed on GitHub** — it became a tracked issue; follow the link to see progress.
- **Discarded** — it was reviewed and closed without filing an issue.

## Offline

The page never needs the network: it always shows the statuses from the last time this shop was online, and refreshes them automatically in the background once you're connected again. A report you've just saved appears here immediately, as pending — it moves to "Sent, awaiting review" once it actually uploads.

If this till can't save its own copy of a report — for example, its storage is full — it keeps retrying, but only for a while. After several failed attempts it gives up trying to remember the report locally. The report itself still reaches support either way; it just won't be listed here.

Open it from the report panel's **View my reports** link (🐞 button in the left-hand menu — see the Reporting a problem topic).
