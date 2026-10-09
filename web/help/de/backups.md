---
id: backups
title: Datensicherungen
section: Den Betrieb führen
order: 250
summary: Momentaufnahmen aller Ihrer Geschäftsdaten (Katalog, Verkäufe, Einstellungen), die Sie herunterladen und sicher aufbewahren können.
keywords: [sicherung, backup, wiederherstellen, herunterladen, kopie, deinstallieren, entfernen]
---

# Datensicherungen

Momentaufnahmen aller Ihrer Geschäftsdaten (Katalog, Verkäufe, Einstellungen), die Sie herunterladen und sicher aufbewahren können. Jede Sicherung enthält auch die Fotos, die Sie für Artikel und Kategorien hochgeladen haben, und Ihr Belegslogo — die eine heruntergeladene Datei stellt also auch diese wieder her.

## Verwendung

1. Einstellungen → Datensicherungen: Erstellen Sie jederzeit eine Sicherung. Wenn dort steht, dass die Fotos nicht hinzugefügt werden konnten, enthält die Sicherung Ihre Daten, aber nicht Ihre Fotos — versuchen Sie es erneut, bevor Sie die Kasse löschen oder ersetzen.
2. „Herunterladen“ speichert eine Kopie in Ihrem Downloads-Ordner — bewahren Sie eine Kopie getrennt von der Kasse auf.

## Eine Sicherung wiederherstellen

Beim Wiederherstellen werden alle aktuellen Daten durch die gewählte Sicherung ersetzt — bestätigen Sie mit der PIN einer Führungskraft, da dies über die Einstellungsseite selbst nicht rückgängig gemacht werden kann (die ersetzten Daten werden als eigene Sicherung aufbewahrt, falls Sie sie wiederhaben möchten). Klicken Sie nach der Wiederherstellung auf **Jetzt neu starten**, und die Kasse startet sich selbst neu — kein Griff zu Tastatur oder Steckdose nötig. Unter Windows kann sich die Kasse noch nicht selbst neu starten — schließen Sie stattdessen das Fenster und öffnen Sie Universal Till erneut.

## Automatisches Aufräumen

Einmal am Tag löscht die Kasse Dateien, die sie nicht mehr braucht, damit der Speicher nicht vollläuft:

- Datensicherungen: die neuesten 14 bleiben erhalten. Zusammen belegen sie nie mehr als ein Viertel des freien Speichers: Die ältesten werden zuerst gelöscht, die neuesten 3 bleiben aber immer erhalten.
- Die Kopie Ihrer Daten, die beim Wiederherstellen einer Sicherung beiseitegelegt wird: wird so lange aufbewahrt, wie Sie Verkaufsdaten gesetzlich aufbewahren müssen; danach bleiben nur noch die neuesten 3 erhalten, keine älter als 30 Tage.
- Problemberichte, die nicht gesendet werden konnten: nach 7 Tagen gelöscht.
- Heruntergeladene Updates: nach 7 Tagen gelöscht.
- Hochgeladene Dateien, die liegen geblieben sind, weil die Kasse mitten in einem Upload oder Import ausgeschaltet wurde oder abgestürzt ist: nach 24 Stunden gelöscht.

Wird der Speicher fast voll — weniger als 500 MB frei oder weniger als ein Zehntel des Speichers, je nachdem, was größer ist —, räumt die Kasse sofort auf, statt bis zum nächsten Tag zu warten, und behält nur das Nötigste: die neuesten 3 Datensicherungen, die neueste beim Wiederherstellen beiseitegelegte Kopie sowie Problemberichte und heruntergeladene Updates vom letzten Tag. Führungskräfte sehen dann in der Statusleiste **Speicher fast voll**, bis wieder Platz ist. Der Verkauf läuft weiter; um mehr Platz zu schaffen, löschen Sie nicht benötigte Dateien vom Gerät oder fragen Sie die Person, die es eingerichtet hat.

Verkäufe, Belege, das Prüfprotokoll, Z-Berichte und alle anderen Aufzeichnungen, die Sie gesetzlich aufbewahren müssen, werden dabei nie gelöscht. Jede Kasse räumt nur ihren eigenen Speicher auf.

## Die Kasse von einem Linux-Rechner entfernen

Wurde die Kasse aus dem `.deb`-Paket installiert, führen Sie `sudo unitill-uninstall` in einem Terminal aus, um sie zu entfernen. Dabei wird zunächst eine verifizierte Sicherung Ihrer Geschäftsdaten erstellt (im Home-Verzeichnis gespeichert), und Sie werden gefragt, ob die Daten behalten werden sollen — wenn Sie sie behalten, kann eine spätere Neuinstallation dort weitermachen, wo Sie aufgehört haben. Zum Löschen der Daten müssen Sie `DELETE` eingeben, damit dies nicht versehentlich geschieht.
