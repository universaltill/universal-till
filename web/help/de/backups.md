---
id: backups
title: Datensicherungen
section: Den Betrieb führen
order: 250
summary: Momentaufnahmen aller Ihrer Geschäftsdaten (Katalog, Verkäufe, Einstellungen), die Sie herunterladen und sicher aufbewahren können.
keywords: [sicherung, backup, wiederherstellen, herunterladen, kopie, deinstallieren, entfernen]
---

# Datensicherungen

Momentaufnahmen aller Ihrer Geschäftsdaten (Katalog, Verkäufe, Einstellungen), die Sie herunterladen und sicher aufbewahren können.

## Verwendung

1. Einstellungen → Datensicherungen: Erstellen Sie jederzeit eine Sicherung.
2. „Herunterladen“ speichert eine Kopie in Ihrem Downloads-Ordner — bewahren Sie eine Kopie getrennt von der Kasse auf.

## Eine Sicherung wiederherstellen

Beim Wiederherstellen werden alle aktuellen Daten durch die gewählte Sicherung ersetzt — geben Sie zur Bestätigung `RESTORE` ein, da dies über die Einstellungsseite selbst nicht rückgängig gemacht werden kann (die ersetzten Daten werden als eigene Sicherung aufbewahrt, falls Sie sie wiederhaben möchten). Klicken Sie nach der Wiederherstellung auf **Jetzt neu starten**, und die Kasse startet sich selbst neu — kein Griff zu Tastatur oder Steckdose nötig. Unter Windows kann sich die Kasse noch nicht selbst neu starten — schließen Sie stattdessen das Fenster und öffnen Sie Universal Till erneut.

## Die Kasse von einem Linux-Rechner entfernen

Wurde die Kasse aus dem `.deb`-Paket installiert, führen Sie `sudo unitill-uninstall` in einem Terminal aus, um sie zu entfernen. Dabei wird zunächst eine verifizierte Sicherung Ihrer Geschäftsdaten erstellt (im Home-Verzeichnis gespeichert), und Sie werden gefragt, ob die Daten behalten werden sollen — wenn Sie sie behalten, kann eine spätere Neuinstallation dort weitermachen, wo Sie aufgehört haben. Zum Löschen der Daten müssen Sie `DELETE` eingeben, damit dies nicht versehentlich geschieht.
