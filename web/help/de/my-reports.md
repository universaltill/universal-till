---
id: my-reports
title: Meine Berichte
section: Verbinden & erweitern
order: 361
summary: "Sehen Sie die von dieser Kasse erfassten Problemberichte — gesendet oder noch ausstehend (die letzten 100) — mit ihrem letzten bekannten Status. Funktioniert auch offline."
routes: [/my-reports]
keywords: [fehler, problem, bericht, status, gesendet, ausstehend, github, verfolgung]
---

# Meine Berichte

Sehen Sie die von dieser Kasse erfassten Problemberichte — gesendet oder noch nicht gesendet (die letzten 100) — mit ihrem letzten bekannten Status. Funktioniert auch offline.

## Was die Seite zeigt

Jede Zeile ist ein von dieser Kasse erfasster Bericht — gesendet oder nicht —, mit dem Zeitpunkt der Erfassung, dem Inhalt (Ihre getippte Notiz sowie Kennzeichnungen für eine Sprachnotiz, eine Bildschirmaufzeichnung oder Screenshots) und seinem aktuellen Status.

Hat diese Kasse mehr als 100 Berichte gesendet, weist eine Zeile unter der Einleitung darauf hin, wie viele nicht angezeigt werden — sie erscheinen wieder, sobald ältere abgelegt oder verworfen werden.

Die Status bedeuten:

- **Hier gespeichert, wartet auf Versand** — auf dieser Kasse erfasst, noch nicht hochgeladen (normal, während das Geschäft offline ist).
- **Konnte nicht gesendet werden** — dieser Bericht scheitert seit einer Weile beim Hochladen; darunter erscheint ein kurzer Grund (zum Beispiel die noch nicht abgeschlossene Aufnahme dieser Kasse). Er ist weiterhin gespeichert, und die Kasse versucht es automatisch weiter — nichts geht verloren.
- **Gesendet, wartet auf Prüfung** — von dieser Kasse hochgeladen; die Cloud hat noch nichts Weiteres gemeldet.
- **Empfangen / Wird transkribiert / Bereit zur Prüfung** — der Bericht wird verarbeitet (Sprachnotizen werden automatisch transkribiert).
- **Auf GitHub abgelegt** — er wurde zu einem verfolgten Vorgang, und wir wissen noch nicht, was daraus geworden ist. Dieser Status aktualisiert sich automatisch zu einem der drei folgenden, sobald wir es wissen.
- **Offen — wird bearbeitet** / **Behoben** / **Geschlossen — nicht geplant** — der eigene Status des verfolgten Vorgangs, automatisch aktualisiert. „Nicht geplant“ bedeutet, dass er geprüft und bewusst nicht aufgegriffen wurde — der Vorgang selbst nennt den Grund.
- **Verworfen** — er wurde geprüft und ohne Ablage eines Vorgangs geschlossen.

## Offline

Die Seite benötigt nie das Netzwerk: Sie zeigt immer die Status vom letzten Mal, als dieses Geschäft online war, und aktualisiert sie automatisch im Hintergrund, sobald Sie wieder verbunden sind. Ein gerade gespeicherter Bericht erscheint hier sofort als ausstehend — er wechselt zu „Gesendet, wartet auf Prüfung“, sobald er tatsächlich hochlädt.

Kann diese Kasse keine eigene Kopie eines Berichts speichern — zum Beispiel weil ihr Speicher voll ist —, versucht sie es weiter, aber nur eine Weile. Nach mehreren fehlgeschlagenen Versuchen gibt sie auf, sich den Bericht lokal zu merken. Der Bericht selbst erreicht den Support trotzdem; er wird hier nur nicht aufgeführt.

Öffnen Sie es über den Link **Meine Berichte ansehen** im Berichtsfenster (🐞-Schaltfläche im linken Menü — siehe das Thema „Ein Problem melden“).
