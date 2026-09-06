---
id: bluetooth-devices
title: Bluetooth devices
section: Connecting & extending
order: 236
summary: Pair a Bluetooth barcode scanner or scale with the till from inside the POS — scan, tap Pair, done — without ever opening the operating system's settings.
keywords: [bluetooth, scanner, barcode, scale, pair, pairing, HID, wireless, forget]
routes: [/bluetooth-devices]
---

# Bluetooth devices

Pair a Bluetooth barcode scanner or scale with the till from inside the POS — scan, tap Pair, done — without ever opening the operating system's settings.

## How to use it

1. Open **Bluetooth devices** from the menu (manager only). The **Paired devices** list shows everything already paired with this till, with its address and whether it is connected right now.
2. Put the new device in pairing mode (most scanners: hold the trigger or scan the "pairing" barcode in their manual), then tap **Scan for devices**. The scan takes about ten seconds and lists what it finds; devices that look like a scanner or keyboard are marked **Scanner / keyboard**.
3. Tap **Pair** next to the device. The till pairs it, trusts it and connects it in one go; the page refreshes and the device appears in the paired list. From now on it reconnects on its own whenever it is switched on nearby — a scanner works like a plugged-in one from that moment.
4. To remove a device, tap **Forget** next to it. It is no longer trusted or connected and will not reconnect until you pair it again.

## Good to know

- Nothing is ever paired by the scan alone — a device joins the till only when you tap **Pair** on it.
- If the device asks for a PIN, it cannot be paired from here. Almost every Bluetooth scanner and scale pairs without one; for the rare one that insists, ask your installer.
- A device that is out of range or switched off simply shows as *not connected*; that never stops a sale or anything else on the till.
- On a till with no Bluetooth (no adapter, or the Bluetooth service not running) the page says so and the scan button is off — nothing else changes.
- On an Android till, Bluetooth pairing isn't available yet — the page says so plainly, without suggesting a hardware or settings problem that isn't there.
- Pairing and forgetting are recorded in the audit trail with who did it.
