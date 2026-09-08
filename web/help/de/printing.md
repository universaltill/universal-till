---
id: printing
title: Belege & Drucken
section: Den Betrieb führen
order: 230
summary: Druckt Belege, Rechnungen und Tagesabschlussberichte auf einem Thermobelegdrucker oder einem gewöhnlichen Bürodrucker; Küchenbestellungen können auf einem separaten Drucker ausgegeben werden.
keywords: [drucker, beleg, küche, etiketten, papier]
---

# Belege & Drucken

Druckt Belege, Rechnungen und Tagesabschlussberichte auf einem Thermobelegdrucker oder einem gewöhnlichen Bürodrucker; Küchenbestellungen können auf einem separaten Drucker ausgegeben werden.

## Verwendung

1. Wählen Sie in den Einstellungen Ihren Drucker und dessen Typ — Thermo oder gewöhnlich.
2. Kennen Sie die Adresse Ihres Netzwerkdruckers nicht? Verwenden Sie **Drucker in diesem Netzwerk suchen**, um danach zu scannen, statt sie von Hand einzutippen — eine nur für Manager sichtbare Schaltfläche neben den Druckeradressfeldern. Sie findet nur Drucker, die sich selbst als AppSocket/JetDirect ankündigen (der übliche Typ für Netzwerk-Belegdrucker); ein reiner IPP-Drucker oder einer, der per USB angeschlossen ist, erscheint nicht und benötigt weiterhin die manuelle Eingabe von Adresse oder Gerätepfad.
3. Verwenden Sie die Testdruck-Schaltfläche, um die Verbindung zu prüfen.
4. Ein Küchendrucker kann separat eingestellt werden, damit Essensbestellungen dort gedruckt werden, wo sie zubereitet werden — dieselbe Schaltfläche **Drucker in diesem Netzwerk suchen** bietet für beide Felder einen Kandidaten an.
5. Benötigen Sie mehr als einen Küchendrucker — zum Beispiel einen Grilldrucker und einen Bardrucker? Siehe **Küchenstationen**, um Kategorien oder einzelne Artikel an ihre eigene Station zu leiten.
6. Küchenbons drucken den Bestelltyp und die Stationskopfzeile in der eingestellten Sprache der Kasse, nicht immer auf Englisch. Es gibt keine separate reine Küchensprache-Einstellung — sie folgt der einen konfigurierten Sprache der Kasse.
7. Öffnet sich die Kassenschublade nach einem Barverkauf nicht? Die meisten Schubladen sind an Pin 2 (Standard) angeschlossen — braucht Ihre Pin 5, stellen Sie dies unter **Pin der Kassenschublade** in den Druckereinstellungen ein.
8. Druckt die Kasse Währungssymbole verstümmelt — kommt `€` oder `£` als seltsame Zeichen heraus? Viele Thermodrucker verstehen kein UTF-8. Seit v0.12.12 übernimmt die Kasse dies für Sie: Ein in Euro oder Pfund eingerichtetes Geschäft mit einer westeuropäischen Sprache sendet automatisch **Westeuropa (CP858 — €/£)**, und eine bereits eingerichtete Kasse wird beim ersten Start mit dieser Version einmalig umgestellt. Sie benötigen **Zeichensatz** unter Einstellungen → Drucker nur, um dies zu überschreiben — zum Beispiel bei einem Drucker, der tatsächlich UTF-8 beherrscht. Dies ändert nur, was an den Belegdrucker gesendet wird — die Sprachunterstützung der Kasse auf dem Bildschirm ist davon nicht betroffen. CP858 deckt nur westeuropäische Buchstaben ab, daher bleiben arabische, farsi-, türkisch- und griechischsprachige Kassen bei **UTF-8** und werden nie automatisch umgestellt.
