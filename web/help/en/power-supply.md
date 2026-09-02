---
id: power-supply
title: Power supply warning
section: Connecting & extending
order: 355
summary: On a Raspberry Pi, the status bar warns when the power supply can't deliver enough current for the touchscreen and other USB peripherals.
keywords: [power, PSU, USB, Raspberry Pi, under-voltage, touchscreen]
---

# Power supply warning

On a Raspberry Pi till, an under-rated power supply restricts how much
current is available to USB peripherals — including the touchscreen, which
can then behave erratically (missed touches, intermittent freezes). The
device itself already detects this, but a shop running in kiosk mode with
no desktop chrome would otherwise never see the warning.

## How to use it

1. If the status bar shows **"Power supply too weak,"** the till has
   detected that its power supply can't reliably deliver enough current.
2. Replace it with the official power supply for this board (a Raspberry Pi
   5 needs the official 27 W USB-C supply specifically — an older, lower-
   wattage USB-C charger is not enough, even though the connector fits).
3. The warning clears the next time the till starts up on a proper supply.

This is a local, offline check — it never depends on the till being online,
and it never blocks a sale.
