---
id: telemetry
title: Plugin telemetry
section: Connecting & extending
order: 333
public: true
summary: An optional setting that tells the marketplace which plugins run on this till and their versions. Off by default; this page lists exactly what is sent and how to turn it off.
keywords: [telemetry, privacy, marketplace, plugin status, data sharing, opt out]
---

# Plugin telemetry

![Plugin telemetry card](img/telemetry-card.png)

Plugin telemetry is an optional setting that helps the marketplace spot problems with a plugin early. It is **off by default**. Nothing is sent unless you turn it on.

## What is sent, and when

When it is on, the till sends one short report about every five minutes. It only does so if all of these are true:

- the till has been registered with your shop's cloud account,
- the marketplace can be reached, and
- at least one plugin is switched on.

The report contains:

- your shop's ID and this store's ID, and
- for each plugin that is switched on: the plugin's ID, its installed version, that it is enabled, a source field (always "cloud", whatever way the plugin was installed), and this till's device ID.

It is not anonymous: the IDs tie the report to your shop and till. Sales, customers, receipts, prices and error details are not included. If a report can't be sent, it is simply dropped and the next one goes out later. The till carries on working either way, and a sale never waits for it.

## Turn it on or off

1. Settings → **Plugin telemetry**.
2. Tick or untick **Send plugin telemetry to the marketplace**.
3. Press **Save**.

It takes effect at the next report. Changing it needs a manager: if you are signed in as a cashier, the till asks for a manager PIN. Each change is recorded in the audit log.

The setting applies to every till in the shop. On an additional till it is saved through the main till, so it can't be changed while the main till is unreachable (see [Multiple tills](/help/multitill)).

## What it does not cover

This setting only controls the plugin report above. The till's other contact with the cloud, such as checking in, looking for software updates or sending an issue report you chose to send, is not switched by it. See [Software updates](/help/updates) and [Reporting a problem](/help/bug-reporting).
