---
id: payments
title: Zahlungen
section: Täglicher Verkauf
order: 20
summary: Bargeld ist eingebaut; Karte und andere Zahlungsmethoden kommen als Plugins aus dem Store — zum Beispiel Stripe mit einem Kartenlesegerät (Terminal) oder QR-Code-Zahlung.
keywords: [bargeld, karte, zahlung, wechselgeld, aufteilung]
---

# Zahlungen

Bargeld ist eingebaut; Karte und andere Zahlungsmethoden kommen als Plugins aus dem Store — zum Beispiel Stripe mit einem Kartenlesegerät (Terminal) oder QR-Code-Zahlung.

## Verwendung

1. Installieren Sie ein Zahlungs-Plugin aus dem Plugin-Store und geben Sie Ihre Kontoschlüssel in dessen Einstellungen ein.
2. Verwenden Sie ein Kartenlesegerät, legen Sie dessen ID in den Plugin-Einstellungen dieser Kasse fest.
3. Tippen Sie an der Kasse auf **Zahlung**, um das Zahlungsfeld zu öffnen (der Warenkorb bleibt sichtbar, während es offen ist) — die bevorzugte Methode steht vorn, und jede Schaltfläche kann eine geschätzte Gebühr anzeigen (Anbietergebühren unter Einstellungen → Zahlungen festlegen) — wählen Sie die günstigere; eine abgelehnte Karte lässt den Warenkorb unverändert. Die bevorzugte Methode erhält zusätzlich eine ganzbreite ⚡-Schnellzahlungsschaltfläche direkt auf dem Verkaufsbildschirm, unterhalb der Zahlungsschaltfläche — ein Tipp belastet den genauen geschuldeten Betrag in dieser Methode, ohne das Feld überhaupt zu öffnen.
4. Diese Schnellschaltflächen unter dem Reiter **Zahlen** des Feldes belasten immer den *genauen* geschuldeten Betrag in dieser einen Methode — es gibt dort keine Möglichkeit, Wechselgeld zu erfassen oder den Gesamtbetrag aufzuteilen. Für alles andere (fälliges Wechselgeld, mehr als eine Methode) verwenden Sie **Aufteilen** — siehe unten.

## Wechselgeld bei einem Barverkauf geben

Die Schnellschaltfläche „Bargeld“ kann kein Wechselgeld erfassen, da sie immer den genauen Gesamtbetrag bucht. Um Wechselgeld zurückzugeben, wechseln Sie stattdessen zum Reiter **Aufteilen**, auch bei einem ansonsten gewöhnlichen Barverkauf mit nur einer Methode:

1. Wählen Sie **Bargeld** (oder welche Methode der Kunde auch übergeben hat) und geben Sie den tatsächlich erhaltenen Betrag ein — in normalen Währungsbeträgen, z. B. `5.00`, nicht in der kleinsten Einheit.
2. Geben Sie im Feld **Wechselgeld** ein, wie viel Wechselgeld zurückzugeben ist, und dann **Zahlung hinzufügen**. Eine Karte für die ausstehende Zahlung erscheint und zeigt, was der Verkauf tatsächlich einnimmt (Betrag minus Wechselgeld), zusammen mit einem Hinweis auf das gegebene Wechselgeld direkt daneben.
3. **Verkauf abschließen**. Der Beleg erfasst sowohl den übergebenen Betrag als auch das für diese Zahlung gegebene Wechselgeld.

Was schiefgehen kann: Ein Wechselgeldbetrag, der größer ist als der übergebene Betrag, wird abgelehnt, bevor er hinzugefügt wird — korrigieren Sie den Betrag oder das Wechselgeld und versuchen Sie es erneut. Wechselgeld auf `0` (oder leer) zu lassen ist einfach eine gewöhnliche exakte Barzahlung.

## Eine Zahlung auf mehrere Methoden aufteilen

Verwenden Sie dies, wenn ein Verkauf mit mehr als einer Methode bezahlt wird — teils Karte, teils Bargeld, ein Geschenkkarten-Plugin, das den Rest auffüllt, und so weiter.

1. Öffnen Sie den Reiter **Aufteilen** unter dem Warenkorb.
2. Wählen Sie für den ersten Teilbetrag dessen Methode, geben Sie den Betrag ein (und Wechselgeld sowie optional eine Referenz — zum Beispiel eine Transaktions-ID eines Kartenlesegeräts) und dann **Zahlung hinzufügen**. Er wird einer laufenden Liste ausstehender Zahlungen hinzugefügt, jede mit ihrem Nettobetrag und einem ✕ zum Entfernen, falls Sie ihn versehentlich hinzugefügt haben.
3. Wiederholen Sie dies für jeden weiteren Teilbetrag. **Rest auffüllen** übernimmt die Rechenarbeit für Sie — es füllt das Betragsfeld mit dem, was die ausstehenden Zahlungen noch nicht abdecken, damit Sie den letzten Teilbetrag nicht selbst ausrechnen müssen.
4. Sobald die ausstehenden Zahlungen den Gesamtbetrag decken, **Verkauf abschließen**. (Benötigen Sie nur eine Zahlung, funktioniert auch das direkte Eintippen in das Formular und sofortiges Drücken von „Verkauf abschließen“ — sie wird dabei zuerst automatisch hinzugefügt.)
5. **Leeren** leert die gesamte ausstehende Liste und beginnt von vorn.

Was schiefgehen kann:

- **Verkauf abschließen** ohne eingegebenen oder ausstehenden Betrag wird mit der Aufforderung abgelehnt, zuerst eine Zahlung hinzuzufügen.
- **Verkauf abschließen**, während die ausstehenden Zahlungen den Gesamtbetrag noch nicht decken, wird mit einer Meldung abgelehnt, dass der erhaltene Betrag den Verkaufsgesamtbetrag nicht deckt — fügen Sie den Rest hinzu (oder verwenden Sie **Rest auffüllen**), bevor Sie es erneut versuchen.
- **Rest auffüllen** selbst wird abgelehnt, wenn die ausstehenden Zahlungen den Gesamtbetrag bereits decken (es gibt nichts mehr aufzufüllen) oder wenn der Warenkorb gerade nicht in einem Zustand ist, eine Zahlung anzunehmen.
- Die Felder „Betrag“ und „Wechselgeld“ hier nehmen einen normalen Währungsbetrag an (z. B. `2.50`) — anders als das kleine Rabattfeld pro Zeile im Warenkorb selbst, das die kleinste Währungseinheit erwartet (siehe den Abschnitt „Rabatte“ unter „Verkaufen & Kassieren“) — übertragen Sie diese Gewohnheit also nicht zwischen den beiden.
