---
id: channel-orders
title: Online-Bestellungen
section: Täglicher Verkauf
order: 16
summary: "Bestellungen aus Ihrem Webshop oder von Marktplätzen, die auf dieser Kasse erfasst wurden: was bestellt wurde, wie weit sie sind und was sie mit Ihrem Bestand gemacht haben."
routes: [/channel-orders, /channel-orders/{id}]
keywords: [online-bestellungen, webshop, marktplatz, kanal, shopify, bestellnummer, erstattung, bestand, kein verkauf, annehmen, kommissionieren, verpacken, versenden, sendungsverfolgung, stornieren]
---

# Online-Bestellungen

Bestellungen aus Ihrem Webshop oder von Marktplätzen, die auf dieser Kasse erfasst wurden: was bestellt wurde, wie weit sie sind und was sie mit Ihrem Bestand gemacht haben. Es sind keine Kassenverkäufe.

## So geht's

1. Öffnen Sie **Online-Bestellungen** im ☰-Menü. Die neueste Bestellung steht oben. Jede Zeile zeigt Bestellnummer, Zeitpunkt, Kanal, Status, Anzahl der Positionen und die Summe.
2. Tippen Sie auf eine Bestellnummer, um die Positionen zu sehen: Artikel, Artikelnummer, Menge, Einzelpreis, wie viele Stück erstattet wurden und wie viele davon zurück in den Bestand gingen. Eine Position mit **Keinem Artikel zugeordnet** hat hier keinen Artikel, daher blieb Ihr Bestand unverändert; die Kasse meldet außerdem ein Problem, das Sie im Back-Office sehen.
3. Der Bestand sinkt beim Erfassen um die bestellte Menge, auch wenn ein Artikel dadurch unter null fällt (die Kasse warnt Sie). Eine Erstattung, die der Kanal als eingelagert meldet, bucht diese Stücke zurück. Kundendaten werden nie gespeichert: angezeigt wird nur das Land des Kunden.
4. **Bestellung annehmen.** Der Bereich **Aktionen** steht oben in der Bestellung. Eine neue Bestellung bietet **Bestellung annehmen** (mit dem Annehmen sagen Sie dem Online-Shop zu) und **Bestellung stornieren…**. Tippen Sie auf **Bestellung annehmen**.
5. **Kommissionieren und verpacken.** **Als kommissioniert markieren** und **Als verpackt markieren** sind optional; damit halten Sie den Shop auf dem Laufenden. Ist die Bestellung angenommen, können Sie auch direkt zum Versand springen.
6. **Versenden.** **Bestellung versenden…** öffnet ein kleines Formular: Tragen Sie den Versanddienstleister ein und, falls vorhanden, die Sendungsnummer und den Sendungslink (eine vollständige Webadresse, die mit https:// beginnt). Tippen Sie dann auf **Als versendet markieren**.
7. **Stornieren.** **Bestellung stornieren…** öffnet ein Formular: Wählen Sie einen Grund, entscheiden Sie, ob die Artikel zurück in den Bestand gehen (standardmäßig angehakt), und tippen Sie auf **Diese Bestellung stornieren**. Das storniert die Bestellung auch im Online-Shop und lässt sich nicht rückgängig machen. Eine versendete Bestellung lässt sich nicht stornieren.
8. **Wartet auf das Senden.** Nach dem Tippen erscheint ein gestrichelter Hinweis wie **Versand noch nicht gesendet**, der Status bleibt unverändert. Die Aktion wird auf dieser Kasse gespeichert und an den Online-Shop gesendet, sobald die Kasse online ist. Der Status ändert sich, wenn der Shop sie bestätigt, und erst dann ändert sich der Bestand.
9. **Wenn der Shop ablehnt.** Ein Hinweis nennt den Grund (zum Beispiel: Die Bestellung war schon storniert), und die Bestellung zeigt in der Liste **Nicht akzeptiert**. Tippen Sie auf **Ausblenden**, wenn Sie ihn gelesen haben. Kann der Online-Shop einen Versand oder eine Stornierung nicht automatisch übernehmen, bittet der Hinweis Sie, dieselbe Änderung in der Verwaltung des Online-Shops vorzunehmen.

## Hinweise

- Diese Bestellungen sind **keine Kassenverkäufe**. Sie stehen weder in der Kassenlade noch im Z-Bericht, im Journal oder im Fiskaljournal; der Shop hat sie online angenommen und online kassiert.
- Nur die Hauptkasse erfasst Online-Bestellungen und führt Aktionen darauf aus. Wer Berichte sehen darf, kann diese Seite öffnen; für Aktionen braucht es zusätzlich die Berechtigung, den Bestand zu verwalten. Auf einer zusätzlichen Kasse ist die Seite nur lesbar.
- Ist die Kasse offline, warten die Bestellungen in der Cloud und werden beim nächsten Verbinden erfasst, und Ihre Aktionen warten auf der Kasse und werden beim erneuten Verbinden gesendet. Das Kassieren wird dadurch nie aufgehalten.
- Wird diese Kasse nicht mehr als Hauptkasse geführt, bleiben noch nicht gesendete Aktionen mit einem Hinweis auf der Seite, bis Sie sie ausblenden. Sie werden nie stillschweigend verworfen.
