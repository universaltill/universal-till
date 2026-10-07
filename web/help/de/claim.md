---
id: claim
title: Geschäftsregistrierung & Beanspruchung
section: Verbinden & erweitern
order: 320
summary: Die Registrierung verbindet Ihre Kasse mit dem Universal-Till-Marktplatz; die Beanspruchung verknüpft das Geschäft mit IHREM Konto, sodass Sie das Online-Backoffice (Meine Geschäfte, Flottenübersicht) und kostenpflichtige Funktionen erhalten.
keywords: [registrieren, beanspruchen, cloud, marktplatz, geschäft, abonnement, tarif, koppeln, kopplungscode, neu koppeln]
---

# Geschäftsregistrierung & Beanspruchung

Die Registrierung verbindet Ihre Kasse mit dem Universal-Till-Marktplatz; die Beanspruchung verknüpft das Geschäft mit IHREM Konto, sodass Sie das Online-Backoffice (Meine Geschäfte, Flottenübersicht) und kostenpflichtige Funktionen erhalten.

## Verwendung

1. Einstellungen → Kassenregistrierung zeigt die Geschäftsidentität; „Jetzt registrieren“ verbindet sie bei Bedarf. Auf einer verbundenen Kasse registriert die Hauptkasse sie für Sie: Diese Karte meldet dann, dass sie über die Hauptkasse registriert ist, und das Beanspruchen erfolgt an der Hauptkasse.
2. Klicken Sie auf „Dieses Geschäft beanspruchen“, um einen kurzen Code (15 Minuten gültig) und einen QR-Code zu erhalten — scannen Sie ihn mit Ihrem Telefon, um von dort aus zu beanspruchen.
3. Melden Sie sich mit Ihrer Universal-Till-ID im Marktplatz an, öffnen Sie die Beanspruchungsseite und geben Sie den Code ein.

## Automatische Registrierung — Ihre Wahl bei der Einrichtung

Der letzte Bildschirm des Einrichtungsassistenten fragt einmalig, ob die Kasse sofort registriert werden soll — standardmäßig nicht angehakt. Dieselbe Wahl finden Sie unter Einstellungen → Kassenregistrierung als „Diese Kasse automatisch beim Marktplatz registrieren“:

1. Schalten Sie es ein, sendet die Kasse ihre Geräte-ID, den Geschäftsnamen, das Land, die Region und die Softwareversion an den Universal-Till-Cloud-Marktplatz (verwendet für Support, Updates und Lizenzierung) und registriert sich sofort. Ihre Adresse wird nicht gesendet. Sind Sie in diesem Moment offline? Es entsteht keine Verzögerung, und die Einrichtung wird trotzdem abgeschlossen — die Kasse bleibt jedoch unregistriert, bis Sie den Plugin-Store öffnen oder „Jetzt registrieren“ drücken. Prüfen Sie daher Einstellungen → Kassenregistrierung, sobald Sie wieder online sind.
2. Schalten Sie es aus, registriert sich die Kasse erst wieder, wenn Sie zum ersten Mal den Plugin-Store nutzen oder „Jetzt registrieren“ drücken. Das Ausschalten entfernt niemals eine bereits erfolgte Registrierung.

## Abonnement

Einstellungen → Abonnement (nur für Manager) zeigt Ihren Tarif so, wie diese Kasse ihn zuletzt von der Universal-Till-Cloud erhalten hat, das Verlängerungsdatum und wann er zuletzt bestätigt wurde. Verkaufen, Belege und Berichte an der Kasse sind in jedem Tarif kostenlos und hören wegen des Abonnements nie auf.

- Lokal (kostenlos): Nichts zu tun, die Karte zeigt nur, dass Sie den kostenlosen Tarif nutzen.
- Aktiv: Ihr Tarif, das Verlängerungsdatum und die letzte Bestätigung.
- Abonnement nicht bestätigt: Die Kasse konnte Ihren Tarif seit mehr als 7 Tagen nicht bei der Cloud bestätigen, daher sind kostenpflichtige Funktionen (etwa Cloud-Synchronisierung, Artikelverwaltung im Browser und die Einrichtung der verwalteten TSE) pausiert. Prüfen Sie die Internetverbindung der Kasse; der Hinweis verschwindet von selbst, sobald sie verbunden ist.
- Abonnement beendet: Ihr Abonnement ist nicht mehr aktiv, daher sind kostenpflichtige Funktionen pausiert, bis Sie es in Ihrem Universal-Till-Konto verlängern. Tippen Sie danach unter Einstellungen → Kassenregistrierung auf „Nach einem kostenpflichtigen Tarif suchen“: Eine Kasse mit beendetem Abonnement meldet sich von selbst nur noch beim ersten Start jedes Tages bei der Cloud, so erfährt sie am schnellsten von der Verlängerung. Auf einer Kasse, die über die Hauptkasse registriert ist, tippen Sie an der Hauptkasse darauf: Diese Kasse erhält die Verlängerung von dort.

In den beiden letzten Fällen führt ein Hinweis in der Statusleiste zu dieser Karte, und Kassenregistrierung sowie Belegsignierung (TSE) zeigen direkt an, welche ihrer Funktionen pausiert sind. Nichts wird ausgeblendet, und eine bereits eingerichtete TSE signiert weiterhin Belege.

## Die Cloud-Synchronisierung gehört zu den kostenpflichtigen Tarifen

Registrieren und Beanspruchen sind kostenlos, ebenso der Plugin-Store. Die Kasse mit Ihrem Online-Konto synchron zu halten (Umsatzzahlen, Artikelliste, Änderungen auf der Cloud-Verwaltungsseite des Geschäfts) gehört zu den kostenpflichtigen Tarifen. Solange das Geschäft keinen davon hat, synchronisiert die Kasse nicht mit der Cloud, und Einstellungen → Kassenregistrierung zeigt an, dass die Cloud-Synchronisierung aus ist. Sie meldet sich nur kurz bei der Cloud: bei der Installation, nach einem Update und beim ersten Start jedes Tages.

1. Sobald das Geschäft einen kostenpflichtigen Tarif hat, erfährt die Kasse das beim nächsten Start an einem neuen Tag oder schon früher, wenn jemand an ihr registriert, koppelt, ein Plugin installiert oder einen Beanspruchungscode erzeugt. Sie können auch unter Einstellungen → Kassenregistrierung auf **Nach einem kostenpflichtigen Tarif suchen** tippen (ein Manager oder Admin bestätigt das). Die Cloud-Synchronisierung startet dann innerhalb einer Minute und läuft von selbst weiter.
2. Endet der Tarif, stoppt die Cloud-Synchronisierung nach dem nächsten Abgleich der Kasse mit der Cloud. Der Verkauf hängt nie davon ab: Jeder Verkauf funktioniert so oder so offline.

## Wenn die Cloud diese Kasse nicht mehr annimmt

Lehnt die Cloud die Zugangsdaten dieser Kasse dreimal hintereinander ab, erscheint ein Hinweis in der Statusleiste. Er blockiert nie einen Verkauf: Die Kasse verkauft offline weiter, versucht es einmal pro Stunde erneut bei der Cloud und behält alles, was sie noch senden muss, bis sie wieder verbunden ist. Der Hinweis lautet:

- **Aus dem Cloud-Konto des Geschäfts entfernt**: Der Inhaber hat diese Kasse online aus dem Geschäft entfernt.
- **Diese Kasse muss neu gekoppelt werden**: Die Cloud-Zugangsdaten der Kasse gelten nicht mehr; die Kasse muss erneut mit dem Geschäft gekoppelt werden.
- **Als neues Geschäft registrieren**: Das Geschäft hat kein Inhaberkonto, daher kann die Kasse nicht gekoppelt werden. Registrieren Sie sie neu als neues Geschäft; nur der alte Kassenverlauf geht verloren.

Ein Manager öffnet mit einem Tipp auf den Hinweis Einstellungen → Kassenregistrierung. Der Hinweis verschwindet nach dem nächsten erfolgreichen Kontakt mit der Cloud.

## Mit einem Geschäft koppeln (und neu koppeln)

Verwenden Sie dies, wenn das Geschäft bereits in der Universal-Till-Cloud existiert und diese Kasse ihm beitreten soll, oder wenn eine Kasse aus dem Cloud-Konto des Geschäfts entfernt wurde bzw. meldet, dass sie neu gekoppelt werden muss.

1. Wählen Sie im Cloud-Konto des Geschäfts „Kasse hinzufügen oder neu koppeln“, um einen Kopplungscode zu erhalten (8 Zeichen, 15 Minuten gültig, einmal verwendbar).
2. Öffnen Sie an dieser Kasse Einstellungen → Kassenregistrierung → Mit einem Geschäft koppeln, geben Sie den Code ein und tippen Sie auf „Koppeln“. Ein Manager oder Administrator bestätigt den Vorgang.
3. Die Kasse ersetzt nur ihre eigene Cloud-Verbindung: Sie erhält eine neue Geräte-ID und eigene Zugangsdaten. Verkäufe, Artikel und Einstellungen bleiben auf der Kasse, und der Verkauf funktioniert die ganze Zeit offline weiter.
4. Auf einer verbundenen Kasse muss der Code vom selben Geschäft stammen wie ihre Hauptkasse; ein Code für ein anderes Geschäft wird abgelehnt und nichts ändert sich. Nach einem abgelehnten oder abgelaufenen Code bleibt die Kasse unregistriert, bis Sie sie mit einem neuen Code koppeln.
