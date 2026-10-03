---
id: claim
title: Geschäftsregistrierung & Beanspruchung
section: Verbinden & erweitern
order: 320
summary: Die Registrierung verbindet Ihre Kasse mit dem Universal-Till-Marktplatz; die Beanspruchung verknüpft das Geschäft mit IHREM Konto, sodass Sie das Online-Backoffice (Meine Geschäfte, Flottenübersicht) und kostenpflichtige Funktionen erhalten.
keywords: [registrieren, beanspruchen, cloud, marktplatz, geschäft]
---

# Geschäftsregistrierung & Beanspruchung

Die Registrierung verbindet Ihre Kasse mit dem Universal-Till-Marktplatz; die Beanspruchung verknüpft das Geschäft mit IHREM Konto, sodass Sie das Online-Backoffice (Meine Geschäfte, Flottenübersicht) und kostenpflichtige Funktionen erhalten.

## Verwendung

1. Einstellungen → Kassenregistrierung zeigt die Geschäftsidentität; „Jetzt registrieren“ verbindet sie bei Bedarf. Auf einer verbundenen Kasse registriert die Hauptkasse sie für Sie: Diese Karte meldet dann, dass sie über die Hauptkasse registriert ist, und das Beanspruchen erfolgt an der Hauptkasse.
2. Klicken Sie auf „Dieses Geschäft beanspruchen“, um einen kurzen Code (15 Minuten gültig) und einen QR-Code zu erhalten — scannen Sie ihn mit Ihrem Telefon, um von dort aus zu beanspruchen.
3. Melden Sie sich mit Ihrer Universal-Till-ID im Marktplatz an, öffnen Sie die Beanspruchungsseite und geben Sie den Code ein.

## Automatische Registrierung — Ihre Wahl bei der Einrichtung

Der letzte Bildschirm des Einrichtungsassistenten fragt einmalig, ob die Kasse sofort registriert werden soll — standardmäßig nicht angehakt. Dieselbe Wahl finden Sie unter Einstellungen → Kassenregistrierung als „Diese Kasse automatisch beim Marktplatz registrieren“:

1. Schalten Sie es ein, sendet die Kasse ihre Geräte-ID, den Geschäftsnamen, die Region und die Softwareversion an den Universal-Till-Cloud-Marktplatz (verwendet für Support, Updates und Lizenzierung) und registriert sich sofort. Ihre Adresse wird nicht gesendet. Sind Sie in diesem Moment offline? Es entsteht keine Verzögerung, und die Einrichtung wird trotzdem abgeschlossen — die Kasse bleibt jedoch unregistriert, bis Sie den Plugin-Store öffnen oder „Jetzt registrieren“ drücken. Prüfen Sie daher Einstellungen → Kassenregistrierung, sobald Sie wieder online sind.
2. Schalten Sie es aus, registriert sich die Kasse erst wieder, wenn Sie zum ersten Mal den Plugin-Store nutzen oder „Jetzt registrieren“ drücken. Das Ausschalten entfernt niemals eine bereits erfolgte Registrierung.

## Wenn die Cloud diese Kasse nicht mehr annimmt

Lehnt die Cloud die Zugangsdaten dieser Kasse dreimal hintereinander ab, erscheint ein Hinweis in der Statusleiste. Er blockiert nie einen Verkauf: Die Kasse verkauft offline weiter, versucht es einmal pro Stunde erneut bei der Cloud und behält alles, was sie noch senden muss, bis sie wieder verbunden ist. Der Hinweis lautet:

- **Aus dem Cloud-Konto des Geschäfts entfernt**: Der Inhaber hat diese Kasse online aus dem Geschäft entfernt.
- **Diese Kasse muss neu gekoppelt werden**: Die Cloud-Zugangsdaten der Kasse gelten nicht mehr; die Kasse muss erneut mit dem Geschäft gekoppelt werden.
- **Als neues Geschäft registrieren**: Das Geschäft hat kein Inhaberkonto, daher kann die Kasse nicht gekoppelt werden. Registrieren Sie sie neu als neues Geschäft; nur der alte Kassenverlauf geht verloren.

Ein Manager öffnet mit einem Tipp auf den Hinweis Einstellungen → Kassenregistrierung. Der Hinweis verschwindet nach dem nächsten erfolgreichen Kontakt mit der Cloud.
