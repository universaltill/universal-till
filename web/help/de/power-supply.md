---
id: power-supply
title: Netzteil-Warnung
section: Verbinden & erweitern
order: 355
summary: Auf einem Raspberry Pi warnt die Statusleiste, wenn das Netzteil nicht genug Strom für den Touchscreen und andere USB-Geräte liefern kann.
keywords: [strom, netzteil, USB, Raspberry Pi, unterspannung, touchscreen]
---

# Netzteil-Warnung

An einer Raspberry-Pi-Kasse schränkt ein unterdimensioniertes Netzteil ein, wie
viel Strom für USB-Geräte zur Verfügung steht — einschließlich des
Touchscreens, der sich dann unzuverlässig verhalten kann (verpasste
Berührungen, gelegentliches Einfrieren). Das Gerät selbst erkennt dies
bereits, doch ein Geschäft im Kioskmodus ohne Desktop-Oberfläche würde die
Warnung sonst nie sehen.

## Verwendung

1. Zeigt die Statusleiste **„Netzteil zu schwach“** an, hat die Kasse
   erkannt, dass ihr Netzteil nicht zuverlässig genug Strom liefern kann.
2. Ersetzen Sie es durch das offizielle Netzteil für diese Platine (ein
   Raspberry Pi 5 benötigt speziell das offizielle 27-W-USB-C-Netzteil — ein
   älteres USB-C-Ladegerät mit geringerer Leistung reicht nicht aus, auch
   wenn der Stecker passt).
3. Die Warnung verschwindet beim nächsten Start der Kasse mit einem
   geeigneten Netzteil.

Dies ist eine lokale, offline durchgeführte Prüfung — sie hängt nie davon ab,
ob die Kasse online ist, und blockiert nie einen Verkauf.
