---
id: customer-order-tracking
title: Kundenbestellverfolgung (QR)
section: Täglicher Verkauf
order: 46
summary: "Kunden scannen einen QR-Code, auf der Bestätigung des Selbstbedienungskiosks oder auf dem Beleg eines an der Kasse bonierten Verkaufs, um den Status ihrer Bestellung auf ihrem eigenen Telefon zu verfolgen. Teil der kostenpflichtigen Tarife."
routes: [/o/{token}, /o/{token}/status]
keywords: [verfolgung, QR, kunde, bestellstatus, selbstbedienung, telefon, beleg, kasse]
---

# Kundenbestellverfolgung (QR)

Kunden scannen einen QR-Code, um den Status ihrer Bestellung auf ihrem eigenen Telefon zu verfolgen: nach der Bestellung am Selbstbedienungskiosk (egal ob sie dort bezahlt haben oder an der Kasse bezahlen) oder vom Beleg eines Verkaufs, den Ihr Personal an der Kasse boniert. Wie der Selbstbedienungsbildschirm selbst richtet sich die vom Kunden gesehene Seite an den Kunden und trägt daher keinen „?“-Link zurück zu diesem Handbuch — finden Sie dieses Thema über die Suche oder die Themenliste des Handbuchs.

**Noch nicht verfügbar:** Die Bestellverfolgung zieht in unsere Cloud um. Bis dahin zeigt die Kasse nirgends einen Verfolgungs-QR-Code — weder auf der Kiosk-Bestätigung noch auf Belegen —, denn ein Link auf die Kasse selbst funktioniert nur im WLAN des Geschäfts und würde jedem Kunden die Adresse der Kasse verraten. Der Rest dieser Seite beschreibt, wie es funktionieren wird, sobald die Cloud-Seite online ist.

## Funktionsweise

1. Schließt ein Kunde eine Bestellung am Selbstbedienungskiosk ab, zeigt der Bestätigungsbildschirm einen QR-Code neben der Bestellnummer (die Bestätigung bleibt etwa 20 Sekunden stehen — lang genug zum Scannen).
2. Boniert Ihr Personal einen Verkauf an der Kasse, trägt der Beleg einen QR-Code mit der Zeile **Scannen, um Ihre Bestellung zu verfolgen**: auf dem gedruckten Beleg und auf dem Beleg, der nach dem Bezahlen auf dem Bildschirm erscheint. Ein Nachdruck des Belegs druckt denselben QR-Code. Das hängt von der Beleg-Einstellung ab, die in den Hinweisen unten beschrieben ist.
3. Das Scannen öffnet auf dem Telefon des Kunden eine kleine Seite mit der Bestellnummer und ihrem aktuellen Status — dieselben Status, die Ihr Personal auf dem Bildschirm **Bestellungen** setzt: in Zubereitung, fertig, abgeholt.
4. Die Seite aktualisiert sich selbst alle paar Sekunden, sodass der Kunde „Fertig“ sieht, sobald Ihr Personal darauf tippt — kein Neuladen, kein Nachfragen am Tresen.
5. Der Link öffnet sich in der Sprache, in der der Kiosk gerade verwendet wurde. Der Link eines gedruckten Belegs öffnet sich in der Sprache des Geschäfts, der Beleg auf dem Bildschirm nach der Zahlung in der Sprache der Kassenoberfläche.
6. Hat der Kunde „An der Theke zahlen“ gewählt, zeigt die Seite **Wartet auf Zahlung an der Theke**, bis Ihr Personal die Bestellung kassiert hat. Danach zeigt sie den Status der Bestellung wie bei jeder anderen Bestellung. Eine Bestellung, die länger als etwa 2 Stunden unbezahlt bleibt, erscheint erst wieder auf der Seite, wenn sie bezahlt ist.

## Was der Kunde sehen kann und was nicht

- Die Seite zeigt **nur die Bestellnummer und ihren Status** — keine Namen, keine Artikel, keine Preise, keine Zahlungsdetails. Der Link ist ein langer, zufälliger Code, der nicht erraten werden kann, und jeder gehört zu genau einer Bestellung.
- Sobald die Bestellung abgeholt (oder storniert) wurde, funktioniert der Link noch etwa 2 Stunden lang und antwortet danach nicht mehr — der QR-Code eines alten Belegs bleibt nicht ewig aktiv.

## Hinweise

- Die Bestellverfolgung gehört zu den kostenpflichtigen Tarifen. Im kostenlosen Tarif Lokal oder solange kostenpflichtige Funktionen pausiert sind (Abonnement beendet, oder die Kasse konnte es seit mehr als 7 Tagen nicht bei der Cloud bestätigen), zeigen Bestätigung und Belege keinen QR-Code; die Bestellung oder der Verkauf selbst ist davon nie betroffen.
- Die Verfolgungsseite wird in unserer Cloud bereitgestellt: Das Telefon des Kunden braucht das WLAN Ihres Geschäfts nicht, und die eigene Netzwerkadresse der Kasse erscheint nie auf einem QR-Code oder Beleg.
- Hat Ihr Geschäft mehrere Kassen und wird die Bestellung an einer anderen Kasse als dem Kiosk bezahlt, sieht der Link des Kiosks die Zahlung nicht: Er zeigt bis zu etwa 2 Stunden lang **Wartet auf Zahlung an der Theke** und antwortet danach nicht mehr. Bis die Bestellverfolgung über unsere Cloud läuft, kassieren Sie Kiosk-Bestellungen deshalb an der Kasse des Kiosks selbst.
- Das Kästchen **Bestellverfolgungs-QR-Code (kostenpflichtige Tarife)** im [Belegdesigner](/help/designer) schaltet den QR-Code auf Kassenbelegen ein oder aus. Bis Sie den Belegdesigner zum ersten Mal speichern, ist es eingeschaltet, außer Ihr Geschäft fragt nicht nach „Vor Ort oder zum Mitnehmen“ (Einzelhandels- und Dienstleistungsgeschäfte starten mit ausgeschalteter Frage); dann ist es ausgeschaltet. Beim Speichern des Designers bleibt das Kästchen so, wie es angezeigt wird. Belege von Rückgaben und stornierten Verkäufen tragen nie einen QR-Code, ebenso wenig Belege einer zusätzlichen Kasse in einem Geschäft mit mehreren Kassen: Der Bildschirm **Bestellungen** der Hauptkasse sieht deren Verfolgungslink erst, wenn die Bestellverfolgung über unsere Cloud läuft.
- Eine Bestellung, die niemand auf dem Bildschirm **Bestellungen** verfolgt (der Normalfall bei einem alltäglichen Verkauf an der Kasse), antwortet 12 Stunden nach dem Verkauf nicht mehr. Sobald Ihr Personal ihren Status setzt, gelten stattdessen die Regeln oben.
