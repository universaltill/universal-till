---
id: voucher-import
title: Gutschein-Guthaben importieren
section: Ihr Geschäft einrichten
order: 148
summary: Wechseln Sie von einer anderen Kasse? Übertragen Sie die offenen Guthaben auf physischen Gutscheinkarten, die Ihre Kunden bereits besitzen, als Anfangssaldo — nicht als neuen Verkauf.
keywords: [gutschein, geschenkgutschein, geschenkkarte, import, migration, guthaben, anfangssaldo, csv]
routes: [/settings/vouchers/import]
---

# Gutschein-Guthaben importieren

Wenn Sie von einem anderen System zu dieser Kasse wechseln, besitzen manche Ihrer Kunden möglicherweise bereits physische Gutschein-(Geschenkkarten-)Karten mit echtem Restguthaben. Diese Seite bringt diese Guthaben als **Anfangssaldo** ein — Geld, das diese Kasse diesen Kunden nun schuldet — ohne einen neuen Verkauf zu erfassen. Siehe [Geschenkgutscheine](/help/vouchers) dafür, wie ein Gutschein im Alltag funktioniert, sobald er eingebucht ist.

## Verwendung

1. Öffnen Sie **Einstellungen** und wählen Sie unter **Datenverwaltung** **Gutschein-Guthaben importieren** (nur Manager).
2. Bereiten Sie eine CSV-Datei mit einer Spalte **code** (der auf jeder physischen Karte aufgedruckte Code), einer Spalte **balance** (das verbleibende Guthaben) und optional einer Spalte **label** (Name des Inhabers) vor. Die Spaltennamen sind flexibel — „amount" funktioniert genauso wie „balance", „holder" genauso wie „label" — solange jede Spalte eindeutig benannt ist.
3. Wählen Sie die Datei aus und klicken Sie auf **Vorschau**. Noch wird nichts gespeichert: Sie sehen, wie viele Gutscheine angelegt werden, deren Gesamtwert sowie eine Liste aller Zeilen, die nicht gelesen werden konnten, jeweils mit Begründung.
4. Prüfen Sie die Vorschau und klicken Sie dann auf **Import bestätigen**. Jede gültige Zeile wird zu einem aktiven Gutschein auf dieser Kasse, sofort einlösbar wie jeder andere.

## Gut zu wissen

- Ein Code, der auf dieser Kasse bereits existiert (aus einem früheren Import oder weil die Kasse ihn selbst ausgestellt hat), wird niemals überschrieben — diese Zeile wird übersprungen und gemeldet, sodass ein versehentliches erneutes Hochladen derselben Datei das Guthaben nicht verdoppeln kann.
- Eine Zeile ohne Code, mit einem unlesbaren Guthaben oder mit einem Guthaben von null oder weniger wird übersprungen und gemeldet; jede andere Zeile in der Datei wird trotzdem importiert.
- Ein Code, der mehr als einmal in derselben Datei vorkommt, wird beide Male vollständig übersprungen — korrigieren Sie die Datei und laden Sie sie erneut hoch, statt zu raten, welche Zeile stimmt.
- Importierte Gutscheine funktionieren genau wie an dieser Kasse verkaufte — siehe [Geschenkgutscheine](/help/vouchers) zum Einlösen und zur Zahlung damit.
- Der Import zählt im Tagesabschlussbericht nicht als verkaufter Gutschein, da hier kein tatsächlicher Verkauf stattgefunden hat — siehe [Berichte & Tagesabschluss](/help/reports).
