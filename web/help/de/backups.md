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

1. Einstellungen → Backups: Erstellen Sie jederzeit eine Sicherung. Wenn dort steht, dass die Fotos nicht hinzugefügt werden konnten, enthält die Sicherung Ihre Daten, aber nicht Ihre Fotos — versuchen Sie es erneut, bevor Sie die Kasse löschen oder ersetzen.
2. „Herunterladen“ speichert eine Kopie in Ihrem Downloads-Ordner — bewahren Sie eine Kopie getrennt von der Kasse auf.

## Eine Sicherung wiederherstellen

Beim Wiederherstellen werden alle aktuellen Daten durch die gewählte Sicherung ersetzt — bestätigen Sie mit der PIN einer Führungskraft, da dies über die Einstellungsseite selbst nicht rückgängig gemacht werden kann (die ersetzten Daten werden als eigene Sicherung aufbewahrt, falls Sie sie wiederhaben möchten). Klicken Sie nach der Wiederherstellung auf **Jetzt neu starten**, und die Kasse startet sich selbst neu — kein Griff zu Tastatur oder Steckdose nötig. Unter Windows kann sich die Kasse noch nicht selbst neu starten — schließen Sie stattdessen das Fenster und öffnen Sie Universal Till erneut.

Eine Wiederherstellung behält die eigene Verbindung dieser Kasse mit dem Cloud-Konto Ihres Geschäfts, auch wenn die Sicherung auf einer anderen Kasse erstellt wurde. Ist die Datei keine Sicherung, die diese Kasse verwenden kann, behält sie Ihre aktuellen Daten, und unter Einstellungen → Backups steht, dass die Wiederherstellung nicht angewendet wurde.

## Aus einer Datei wiederherstellen

Nutzen Sie dies auf einer neuen Kasse oder auf einer, die eine defekte Kasse ersetzt, mit einer Sicherungsdatei, die Sie von der alten Kasse kopiert haben (ein USB-Stick, ein anderer Computer, der Download-Ordner).

1. Einstellungen → Backups → **Aus Datei wiederherstellen**: Wählen Sie die Datei (sie heißt `unitill-pos-….db`) und tippen Sie auf **Datei prüfen**.
2. Die Kasse prüft, ob die Datei eine Universal-Till-Sicherung dieses Geschäfts ist, erstellt mit dieser oder einer älteren Version, und zeigt den Namen des Geschäfts, wie viele Verkäufe sie enthält und den neuesten Verkauf. Verkäufe danach sind nicht in der Sicherung.
3. Tippen Sie auf **Diese Sicherung wiederherstellen** und bestätigen Sie mit der PIN einer Führungskraft. Die Kasse sichert zuerst ihre aktuellen Daten; starten Sie sie dann wie oben beschrieben neu.

Verwenden Sie eine Datei aus dem Ordner `backups` der alten Kasse oder eine mit **In Downloads speichern** gesicherte. Kopieren Sie stattdessen die laufende Datenbankdatei `unitill-pos.db` von der Festplatte einer defekten Kasse, kopieren Sie die Dateien `unitill-pos.db-wal` und `unitill-pos.db-shm` daneben mit, sonst fehlen die letzten Verkäufe.

Am Ende der Einrichtung bietet die Willkommensseite die Schaltfläche **Sicherung wiederherstellen**, die dieselbe Stelle öffnet.

Eine wiederhergestellte Sicherung bringt Produkte, Verkäufe, Belege, Mitarbeitende mit ihren PINs, Kunden, Einstellungen, Bestände und Fotos zurück, so wie sie beim Erstellen der Sicherung waren. Schlüssel für Kartenterminal und Zahlungs-Plugins sind nicht in einer Sicherung, und die Cloud-Verbindung der Kasse ebenso wenig. Stellen Sie eine Telefon- oder iCloud-Sicherung auf einem anderen Gerät wieder her oder kopieren Sie die Datenbank auf ein anderes Gerät, startet dieses Gerät unregistriert und zeigt **Auf einem neuen Gerät wiederhergestellt — erneut verbinden**: Koppeln Sie es unter Einstellungen → Kassenregistrierung erneut mit dem Geschäft oder registrieren Sie es als neues Geschäft. Geben Sie Plugin-Schlüssel und Passwörter auf der Einstellungsseite des jeweiligen Plugins erneut ein; ein Feld, das meldet, dass es nicht gelesen werden kann, braucht einen neuen Wert.

## Eine Kasse ersetzen

Nur mit einer Sicherung kommen Verkäufe, Belege, Kunden und die PINs der Mitarbeitenden auf eine neue Kasse zurück: Die Cloud speichert sie in keinem Tarif. Bewahren Sie eine aktuelle Kopie außerhalb der Kasse auf. Führungskräfte sehen in der Statusleiste **Seit 7 Tagen keine Sicherung von dieser Kasse kopiert**, bis eine Sicherung von ihr gespeichert wurde.

Wo die Kasse ihre Sicherungen ablegt (die neuesten 14):

- macOS: `~/Library/Application Support/UniversalTill/backups`
- Windows: `%LOCALAPPDATA%\UniversalTill\backups`
- Linux: `~/.local/share/universal-till/backups` standardmäßig (aus dem `.deb`-Paket installiert: `/opt/unitill/data/backups`)
- Android und iOS: in der App, nicht erreichbar — nutzen Sie **In Downloads speichern**.

1. Wenn die alte Kasse noch startet: Einstellungen → Backups → **Jetzt sichern**, dann **In Downloads speichern** neben der neuen Sicherung, und kopieren Sie die Datei auf einen USB-Stick oder einen anderen Computer.
2. Startet sie nicht, ist ihre Festplatte aber lesbar, nehmen Sie die neueste Datei aus ihrem Ordner `backups`.
3. Schließen Sie auf der neuen Kasse die Einrichtung ab (für ein Geschäft auf my.universaltill.com: als Hauptkasse beitreten), tippen Sie dann auf der Willkommensseite auf **Sicherung wiederherstellen** oder öffnen Sie Einstellungen → Backups → **Aus Datei wiederherstellen**, und folgen Sie „Aus einer Datei wiederherstellen“ oben.
4. Stellen Sie wieder her, bevor die neue Kasse ihren ersten Verkauf bucht: Eine Wiederherstellung ersetzt die Daten der Kasse, sie führt sie nicht zusammen.

Verkäufe, die nach der Sicherung auf der alten Kasse gemacht wurden, sind nicht darin.

## Automatisches Aufräumen

Einmal am Tag löscht die Kasse Dateien, die sie nicht mehr braucht, damit der Speicher nicht vollläuft:

- Datensicherungen: die neuesten 14 bleiben erhalten. Zusammen belegen sie nie mehr als ein Viertel des freien Speichers: Die ältesten werden zuerst gelöscht, die neuesten 3 bleiben aber immer erhalten.
- Die Kopie Ihrer Daten, die beim Wiederherstellen einer Sicherung beiseitegelegt wird: wird so lange aufbewahrt, wie Sie Verkaufsdaten gesetzlich aufbewahren müssen; danach bleiben nur noch die neuesten 3 erhalten, keine älter als 30 Tage.
- Problemberichte, die nicht gesendet werden konnten: nach 7 Tagen gelöscht.
- Heruntergeladene Updates: nach 7 Tagen gelöscht.
- Hochgeladene Dateien, die liegen geblieben sind, weil die Kasse mitten in einem Upload oder Import ausgeschaltet wurde oder abgestürzt ist: nach 24 Stunden gelöscht.

Wird der Speicher fast voll — weniger als 500 MB frei oder weniger als ein Zehntel des Speichers (höchstens 5 GB), je nachdem, was größer ist —, räumt die Kasse sofort auf, statt bis zum nächsten Tag zu warten, und behält nur das Nötigste: die neuesten 3 Datensicherungen, die neueste beim Wiederherstellen beiseitegelegte Kopie sowie Problemberichte und heruntergeladene Updates vom letzten Tag. Führungskräfte sehen dann in der Statusleiste **Speicher fast voll**, bis wieder Platz ist. Der Verkauf läuft weiter; um mehr Platz zu schaffen, löschen Sie nicht benötigte Dateien vom Gerät oder fragen Sie die Person, die es eingerichtet hat.

Verkäufe, Belege, das Prüfprotokoll, Z-Berichte und alle anderen Aufzeichnungen, die Sie gesetzlich aufbewahren müssen, werden dabei nie gelöscht. Jede Kasse räumt nur ihren eigenen Speicher auf.

## Die Kasse von einem Linux-Rechner entfernen

Wurde die Kasse aus dem `.deb`-Paket installiert, führen Sie `sudo unitill-uninstall` in einem Terminal aus, um sie zu entfernen. Dabei wird zunächst eine verifizierte Sicherung Ihrer Geschäftsdaten erstellt (im Home-Verzeichnis gespeichert), und Sie werden gefragt, ob die Daten behalten werden sollen — wenn Sie sie behalten, kann eine spätere Neuinstallation dort weitermachen, wo Sie aufgehört haben. Zum Löschen der Daten müssen Sie `DELETE` eingeben, damit dies nicht versehentlich geschieht.
