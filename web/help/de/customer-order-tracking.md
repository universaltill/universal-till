---
id: customer-order-tracking
title: Kundenbestellverfolgung (QR)
section: Täglicher Verkauf
order: 46
summary: "Nach der Bestellung am Selbstbedienungskiosk scannen Kunden einen QR-Code, um den Status ihrer Bestellung auf ihrem eigenen Telefon zu verfolgen. Teil der kostenpflichtigen Tarife."
routes: [/o/{token}, /o/{token}/status]
keywords: [verfolgung, QR, kunde, bestellstatus, selbstbedienung, telefon]
---

# Kundenbestellverfolgung (QR)

Nach der Bestellung am Selbstbedienungskiosk scannen Kunden einen QR-Code, um den Status ihrer Bestellung auf ihrem eigenen Telefon zu verfolgen, egal ob sie dort bezahlt haben oder an der Kasse bezahlen. Wie der Selbstbedienungsbildschirm selbst richtet sich die vom Kunden gesehene Seite an den Kunden und trägt daher keinen „?“-Link zurück zu diesem Handbuch — finden Sie dieses Thema über die Suche oder die Themenliste des Handbuchs.

## Funktionsweise

1. Schließt ein Kunde eine Bestellung am Selbstbedienungskiosk ab, zeigt der Bestätigungsbildschirm einen QR-Code neben der Bestellnummer (die Bestätigung bleibt etwa 20 Sekunden stehen — lang genug zum Scannen).
2. Das Scannen öffnet auf dem Telefon des Kunden eine kleine Seite mit der Bestellnummer und ihrem aktuellen Status — dieselben Status, die Ihr Personal auf dem Bildschirm **Bestellungen** setzt: in Zubereitung, fertig, abgeholt.
3. Die Seite aktualisiert sich selbst alle paar Sekunden, sodass der Kunde „Fertig“ sieht, sobald Ihr Personal darauf tippt — kein Neuladen, kein Nachfragen am Tresen.
4. Der Link öffnet sich in der Sprache, in der der Kiosk gerade verwendet wurde.
5. Hat der Kunde „An der Theke zahlen“ gewählt, zeigt die Seite **Wartet auf Zahlung an der Theke**, bis Ihr Personal die Bestellung kassiert hat. Danach zeigt sie den Status der Bestellung wie bei jeder anderen Bestellung. Eine Bestellung, die länger als etwa 2 Stunden unbezahlt bleibt, erscheint erst wieder auf der Seite, wenn sie bezahlt ist.

## Was der Kunde sehen kann und was nicht

- Die Seite zeigt **nur die Bestellnummer und ihren Status** — keine Namen, keine Artikel, keine Preise, keine Zahlungsdetails. Der Link ist ein langer, zufälliger Code, der nicht erraten werden kann, und jeder gehört zu genau einer Bestellung.
- Sobald die Bestellung abgeholt (oder storniert) wurde, funktioniert der Link noch etwa 2 Stunden lang und antwortet danach nicht mehr — der QR-Code eines alten Belegs bleibt nicht ewig aktiv.

## Hinweise

- Die Bestellverfolgung gehört zu den kostenpflichtigen Tarifen. Im kostenlosen Tarif Lokal oder solange kostenpflichtige Funktionen pausiert sind (Abonnement beendet, oder die Kasse konnte es seit mehr als 7 Tagen nicht bei der Cloud bestätigen), zeigt die Bestätigung keinen QR-Code; die Bestellung selbst ist davon nie betroffen.
- Das Telefon des Kunden muss sich im selben Netzwerk wie die Kasse befinden — dem WLAN des Geschäfts. Hat die Kasse keine andere Netzwerkadresse als sich selbst, zeigt die Bestätigung einfach ohne QR-Code an; die Bestellung selbst ist davon nie betroffen.
- Hat Ihr Geschäft mehrere Kassen und wird die Bestellung an einer anderen Kasse als dem Kiosk bezahlt, sieht der Link des Kiosks die Zahlung nicht: Er zeigt bis zu etwa 2 Stunden lang **Wartet auf Zahlung an der Theke** und antwortet danach nicht mehr. Bis die Bestellverfolgung über unsere Cloud läuft, kassieren Sie Kiosk-Bestellungen deshalb an der Kasse des Kiosks selbst.
- Der QR-Code erscheint nur bei Bestellungen am Selbstbedienungskiosk. Verkäufe, die Ihr Personal an der Kasse bonieren, drucken noch keinen Verfolgungs-QR-Code.
