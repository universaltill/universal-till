---
id: bluetooth-devices
title: Bluetooth-Geräte
section: Verbinden & erweitern
order: 236
summary: Koppeln Sie einen Bluetooth-Barcode-Scanner oder eine Waage direkt aus der Kasse heraus mit der Kasse — scannen, auf Koppeln tippen, fertig — ohne jemals die Einstellungen des Betriebssystems zu öffnen.
keywords: [bluetooth, scanner, barcode, waage, koppeln, pairing, HID, kabellos, vergessen]
routes: [/bluetooth-devices]
---

# Bluetooth-Geräte

Koppeln Sie einen Bluetooth-Barcode-Scanner oder eine Waage direkt aus der Kasse heraus mit der Kasse — scannen, auf Koppeln tippen, fertig — ohne jemals die Einstellungen des Betriebssystems zu öffnen.

## Verwendung

1. Öffnen Sie **Bluetooth-Geräte** über das Menü (nur Manager). Die Liste **Gekoppelte Geräte** zeigt alles, was bereits mit dieser Kasse gekoppelt ist, mit Adresse und aktuellem Verbindungsstatus.
2. Versetzen Sie das neue Gerät in den Kopplungsmodus (bei den meisten Scannern: Auslöser gedrückt halten oder den „Pairing“-Barcode im Handbuch scannen) und tippen Sie dann auf **Nach Geräten suchen**. Der Suchvorgang dauert etwa zehn Sekunden und listet die gefundenen Geräte auf; Geräte, die wie ein Scanner oder eine Tastatur aussehen, werden als **Scanner/Tastatur** markiert.
3. Tippen Sie neben dem Gerät auf **Koppeln**. Die Kasse koppelt, vertraut und verbindet das Gerät in einem Schritt; die Seite aktualisiert sich, und das Gerät erscheint in der Liste der gekoppelten Geräte. Von nun an verbindet es sich von selbst wieder, sobald es in der Nähe eingeschaltet wird — ein Scanner funktioniert ab diesem Moment wie ein fest angeschlossener.
4. Um ein Gerät zu entfernen, tippen Sie daneben auf **Vergessen**. Es wird nicht mehr vertraut oder verbunden und verbindet sich erst wieder, wenn Sie es erneut koppeln.

## Gut zu wissen

- Durch die Suche allein wird nie etwas gekoppelt — ein Gerät wird der Kasse erst hinzugefügt, wenn Sie darauf **Koppeln** tippen.
- Fragt das Gerät nach einer PIN, kann es von hier aus nicht gekoppelt werden. Fast jeder Bluetooth-Scanner und jede Waage koppelt ohne PIN; für die seltenen Ausnahmen wenden Sie sich an Ihren Installateur.
- Ein Gerät außer Reichweite oder ausgeschaltet zeigt einfach *nicht verbunden* an; das hält niemals einen Verkauf oder irgendetwas anderes an der Kasse auf.
- Auf einer Kasse ohne Bluetooth (kein Adapter oder der Bluetooth-Dienst läuft nicht) sagt die Seite dies, und die Schaltfläche „Suchen“ ist deaktiviert — sonst ändert sich nichts.
- Auf einer Android-Kasse (einem Tablet) funktioniert die Seite genauso, mit drei Besonderheiten, die Android selbst vorgibt:
    - **Ist Bluetooth ausgeschaltet**, sagt die Seite dies und bietet **Bluetooth einschalten** an. Android bittet dann um Bestätigung — keine App darf den Funk von sich aus einschalten, daher ist diese Bestätigung nicht überspringbar.
    - **Beim ersten Mal** teilt die Seite mit, dass dieser Kasse die Nutzung von Bluetooth noch nicht erlaubt wurde, und bietet **Bluetooth-Zugriff erlauben** an; Android fragt erst, wenn Sie darauf drücken. Lehnen Sie ab, zeigt die Seite dies an, und Sie können es erneut versuchen; haben Sie „nicht mehr fragen“ gewählt, führt es Sie zum eigenen Berechtigungsbildschirm der App.
    - **Vergessen** funktioniert unter Umständen nicht aus der App heraus. Android bietet Apps keinen unterstützten Weg, eine Kopplung zu entfernen; auf manchen Android-Versionen gelingt es, auf anderen teilt die Seite dies offen mit und bietet **Bluetooth-Einstellungen öffnen** an, um es dort zu erledigen. So oder so wird nie behauptet, ein Gerät entkoppelt zu haben, das noch gekoppelt ist.
- Diese drei Schaltflächen erscheinen nur an der Kasse selbst. Öffnen Sie diese Seite von einem anderen Computer im Netzwerk aus, sehen Sie zwar, was nicht stimmt, die Änderung muss aber an der Kasse selbst vorgenommen werden.
- Koppeln und Vergessen werden im Prüfprotokoll mit dem Namen der ausführenden Person festgehalten.
