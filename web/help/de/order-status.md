---
id: order-status
title: Bestellstatus (Küchenfortschritt)
section: Täglicher Verkauf
order: 45
summary: "Markieren Sie den Fortschritt jeder Bestellung — in Zubereitung, fertig, abgeholt — mit einem Tipp, damit jeder sehen kann, wo eine Bestellung steht."
routes: [/orders, /orders/{receipt}]
keywords: [bestellungen, küche, in zubereitung, fertig, abgeholt, storniert, status, scannen, abholen]
---

# Bestellstatus (Küchenfortschritt)

Markieren Sie den Fortschritt jeder Bestellung — in Zubereitung, fertig, abgeholt — mit einem Tipp, damit jeder sehen kann, wo eine Bestellung steht.

## Verwendung

1. Öffnen Sie **Bestellstatus** — das 🛎️-Symbol in der Seitenleiste des Verkaufsbildschirms oder über das ☰-Menü. Dies ist die *aktive* Warteschlange: aktuelle, noch laufende Verkäufe werden neueste zuerst mit ihrem jeweiligen Status aufgelistet.
2. Das Scannen des Kassenbons eines Kunden am Verkaufsbildschirm öffnet ebenfalls direkt dessen Bestellung, falls sie noch nicht abgeholt wurde — mit derselben Ein-Tipp-Schaltfläche **Abgeholt** und einem Link zur Rückerstattung, falls das eigentlich gebraucht wird. Ein bereits abgeholter oder nie nachverfolgter Beleg öffnet direkt die Rückerstattung, genau wie beim Scannen jedes anderen abgeschlossenen Belegs; eine stornierte Bestellung oder ein nie abgeschlossener Verkauf werden stattdessen einfach auf dem Verkaufsbildschirm angezeigt.
3. Tippen Sie auf **In Zubereitung**, wenn die Küche mit einer Bestellung beginnt, auf **Fertig**, wenn sie abgeholt werden kann, und auf **Abgeholt**, wenn der Kunde sie hat. Das kann jeder Bediener tun — keine Manager-PIN nötig.
4. Jede Änderung zeichnet auf, wer sie wann vorgenommen hat, neben dem Status. Angezeigt wird die kurze Bestellnummer, die Ihr Kunde erhalten hat (siehe unten) — ein Tipp darauf öffnet den vollständigen Beleg im Kassenjournal, außer bei einer hier angezeigten Bestellung, weil sie an einer *anderen* Kasse aufgenommen wurde (siehe Hinweis unten), wo die Nummer zwar angezeigt, aber kein Link ist, da diese Kasse den Beleg selbst nicht besitzt.
5. **Bestellung stornieren** markiert eine Bestellung als storniert. Dies funktioniert jederzeit, bis die Bestellung abgeholt ist; eine abgeholte Bestellung kann nicht storniert werden.
6. Das Markieren einer Bestellung als **Abgeholt** oder ihre Stornierung entfernt sie sofort aus dieser Liste — sie ist erledigt und nimmt daher keinen Platz mehr in der aktiven Warteschlange ein. Nichts wird gelöscht: Der Beleg, jede Statusänderung und wer was getan hat bleiben genau dort, wo sie immer waren, im Kassenjournal.
7. Der Status einer Bestellung bewegt sich immer nur vorwärts (Abgeholt und Storniert sind das Ende der Kette). Ein versehentliches Antippen eines früheren Schritts (oder eine zweite Kasse, die nach einer Offline-Phase einen alten Status meldet) ändert nichts — die Kasse zeigt einfach weiter den späteren Status. Das ist Absicht, kein Fehler.
8. Bestellungen aus Verkäufen vor Einführung dieser Funktion (oder die noch niemand angetippt hat) zeigen **Nicht begonnen** — für sie ändert sich nichts, bis jemand einen Status antippt.

## Hinweise

- Die Liste aktualisiert sich selbst alle paar Sekunden — neue Bestellungen (einschließlich am Selbstbedienungskiosk aufgegebener) erscheinen ohne Neuladen der Seite.
- Konnte ein Küchenbon oder Beleg nicht gedruckt werden — ein Drucker ohne Papier, nicht angeschlossen oder offline —, zeigt die Bestellung ein ⚠-Warnsymbol neben ihrem Status, damit eine bezahlte Bestellung (zum Beispiel vom Selbstbedienungskiosk) nie stillschweigend verloren geht. Ein ⚠ bei der Küche bedeutet, dass die Küche den Bon nie erhalten hat: Beheben Sie den Drucker und geben Sie die Bestellung selbst an die Küche weiter. Ein ⚠ beim Beleg verschwindet, sobald Sie den Drucker beheben und diesen Beleg von der Bestellseite im Kassenjournal aus erneut drucken.
- Sind mehrere Kassen verbunden, zeigt und aktualisiert jede Kasse die Bestellungen des ganzen Geschäfts über die Hauptkasse, solange diese erreichbar ist — nicht nur die Kasse, die die Bestellung aufgenommen hat. Eine Kasse, die die Hauptkasse nicht erreichen kann, arbeitet weiter mit ihrer eigenen lokalen Liste und zeigt das gemeinsame Board wieder an, sobald die Verbindung zurück ist — eine Statusänderung, die *während der Trennung* vorgenommen wird, wird jedoch nirgendwohin gesendet: Sobald diese Kasse sich wieder verbindet, wechselt ihr Bildschirm zurück zum gemeinsamen Board, und jedes während der Trennung gemachte Tippen kann von diesem Bildschirm sichtbar verschwinden (ein Manager, der auf einer wirklich offline befindlichen Kasse auf „Fertig“ tippt, sollte dies nach der Wiederverbindung der Kasse noch einmal prüfen). Eine brandneue Bestellung erscheint ebenfalls nicht auf dem gemeinsamen Board — auch nicht auf der Kasse, die sie gerade aufgenommen hat —, bis sie mit der Hauptkasse fertig synchronisiert ist, normalerweise wenige Sekunden.
- Alles hier funktioniert vollständig offline, wie der Rest der Kasse.
- Dieser Bildschirm ist die Grundlage für Kommendes: eine Küchenanzeige, Kunden-Pager und Bestellverfolgung werden alle denselben Status-Vorgaben folgen.

## Bestellnummern

Jeder Verkauf behält seine dauerhafte Belegnummer (für Rückerstattungen und Berichte) — aber Kunden, der Küchenbon, der Bestätigungsbildschirm der Selbstbedienung und diese Liste zeigen stattdessen eine **kurze Bestellnummer**, damit sie sich leicht vorlesen oder über die Theke rufen lässt. Wählen Sie unter **Einstellungen → Bestellnummern**, ob sie nach jedem Kassenabschluss auf 1 zurückgesetzt wird (Standard — entspricht dem, was die meisten modernen Kassensysteme tun) oder einfach ohne Zurücksetzen weiterzählt. Die Änderung wirkt sich nur auf neue Verkäufe aus; frühere Belege bleiben unberührt.
