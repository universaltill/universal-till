---
id: updates
title: Software-Updates
section: Verbinden & erweitern
order: 350
summary: Die Kasse prüft auf neue Versionen und teilt mit, wenn eine verfügbar ist; auf den meisten Plattformen aktualisiert sie sich mit einem Klick selbst.
keywords: [update, version, aktualisierung, zeitplan]
---

# Software-Updates

Die Kasse prüft auf neue Versionen und teilt mit, wenn eine verfügbar ist; auf den meisten Plattformen aktualisiert sie sich mit einem Klick selbst.

## Verwendung

1. Einstellungen → Software-Update → Jetzt prüfen zeigt, ob Sie auf dem neuesten Stand sind.
2. Wird ein Update angeboten, klicken Sie auf „Jetzt aktualisieren“ — die App startet mit der neuen Version neu. Auf einem Linux-Rechner startet sich die Desktop-App außerdem innerhalb etwa einer Minute selbst neu, wenn ein System-Upgrade (apt) eine neue Version installiert, und wird die Seite der Kasse in ihrem Fenster nicht mehr angezeigt, lädt die App sie von selbst neu, bis die Kasse wieder antwortet.
3. Der Update-Chip in der Statusleiste spiegelt dies wider. Wo die Kasse ein Update nicht selbst installieren kann (ein portables Windows-ZIP oder ein Intel-Mac), führt er stattdessen zur Download-Seite. Auf einem Kiosk ohne App-internes Update ist er einfach reiner Text ohne etwas zum Antippen. Auf einer Linux-Kasse mit automatischen Updates, die ihren eigenen Installationsordner nicht beschreiben kann, sagt der Chip, dass die Kasse das Update nicht selbst installieren kann, und führt zu dieser Seite: Installieren Sie das neueste .deb-Paket erneut — damit ist der Ordner wieder beschreibbar (bei jeder anderen Installationsart geben Sie dem Benutzer der Kasse Schreibrechte für ihren Ordner) — und das Update in der nächsten Nacht läuft wie gewohnt.

## Automatische Updates

Die Kasse aktualisiert sich nachts selbst, solange Sie das nicht ausschalten.

1. Einstellungen → Software-Update → „Automatisch aktualisieren“ ist von Anfang an eingeschaltet, um 03:00 Uhr. Jede Kasse wählt ihren eigenen Zeitpunkt innerhalb der 30 Minuten danach, damit nicht mehrere Kassen gleichzeitig neu starten.
2. Sie startet nie mitten in einem Verkauf neu. Liegen noch Artikel in einem Warenkorb (auch in einer Selbstbedienungs- oder Tischbestellung), wartet sie, bis der Warenkorb leer ist. Geschieht das nicht innerhalb einer halben Stunde oder war die Kasse zu dieser Zeit ausgeschaltet, versucht sie es in der nächsten Nacht erneut.
3. Um automatische Updates zu beenden, entfernen Sie das Häkchen bei „Automatisch aktualisieren“ und klicken Sie auf Speichern. Die Kasse behält diese Einstellung. Auf der Hauptkasse beendet das auch das Nachziehen ihrer zusätzlichen Kassen; eine zusätzliche Kasse übernimmt diese Einstellung immer von ihrer Hauptkasse. Deshalb zeigt sie unter Einstellungen → Software-Update statt des Kästchens die Version der Hauptkasse, der sie folgt — automatische Updates ändern Sie an der Hauptkasse.
4. Eine zusätzliche Kasse folgt der Hauptkasse: Läuft auf der Hauptkasse eine neuere Version, installiert die zusätzliche Kasse genau diese Version im nächsten Moment, in dem kein Verkauf offen ist. Zusätzliche Kassen unter Android und aus dem portablen Windows-ZIP installierte zeigen stattdessen einen Hinweis „erforderlich“, bis sie selbst installieren können.
5. Unter Windows lädt die Kasse das signierte Installationsprogramm herunter und prüft es. Dann schließt sich die Kasse, installiert das Update und öffnet sich von selbst wieder. Das dauert etwa eine Minute. Kassen unter Android können Updates noch nicht selbst installieren. Aktualisieren Sie sie von Hand wie oben beschrieben. Wenn Sie das Windows-Installationsprogramm selbst ausführen, schließt es Universal Till zuerst, falls es noch geöffnet ist. Schließen Sie also einen laufenden Verkauf ab, bevor Sie es starten.

## Was ist neu in jeder Version

Nach einem Update zeigt Ihnen die Kasse, was sich geändert hat – in einfachen Worten und in der Sprache der Kasse.

1. Einstellungen → Über zeigt, welche Version diese Kasse verwendet, wann sie zum ersten Mal auf dieser Kasse gestartet wurde, und „Was ist neu“: die Hinweise zu dieser Version und den wenigen davor, die neueste zuerst, gegliedert in Neu, Verbessert und Behoben.
2. Wenn ein Manager oder Admin die Kasse nach einem Update zum ersten Mal öffnet, erscheint in der Statusleiste ein kleiner Hinweis „Aktualisiert auf … – sehen Sie, was neu ist“. Tippen Sie darauf, um die Hinweise zu lesen, oder tippen Sie auf ×, um ihn auszublenden. Er stört nie beim Verkaufen und erscheint nur einmal.
3. Kassierer sehen diesen Hinweis nie, Kunden an einem Selbstbedienungs-Kiosk ebenfalls nicht. Auch eine neu installierte Kasse zeigt ihn nicht.
4. Die Hinweise sind in die Kasse eingebaut und funktionieren daher ohne Internetverbindung. Ist ein Hinweis noch nicht in Ihre Sprache übersetzt, wird er auf Englisch angezeigt.
5. Bevor Sie ein Update installieren, zeigt Einstellungen → Software-Update einem Manager oder Admin, was die neue Version bringt: die Hinweise zu jeder Version, die das Update enthält, die neueste zuerst – in der Sprache der Kasse, sofern übersetzt, sonst auf Englisch. Diese Hinweise werden erst heruntergeladen, wenn Sie diese Seite öffnen. Können sie nicht geladen werden, zeigt die Kasse „Versionshinweise nicht verfügbar“ – unter Windows und auf dem Mac mit einem Link zur Release-Seite – und Sie können das Update trotzdem wie gewohnt installieren.

## Auf einer Android-Kasse

Die Android-App kann sich nicht wie die Desktop-Versionen selbst ersetzen, sie übergibt die neue Version daher an Androids eigenen Installer. Die Schritte sind etwas anders:

1. Tippen Sie unten auf dem Bildschirm auf den grünen Update-Chip, dann erneut zur Bestätigung. Das ist alles — als Manager angemeldet werden Sie nicht nach einer PIN gefragt, genau wie unter Windows und Mac.
2. Android lädt die neue App herunter — etwa 140 MB, geben Sie ihr also bei einer langsamen Verbindung einen Moment; der Chip zeigt währenddessen „Wird heruntergeladen“ — und zeigt dann seinen eigenen Bildschirm „Möchten Sie dieses Update installieren?“ an. Bestätigen Sie dort.
3. Beim ersten Mal fragt Android möglicherweise, ob Universal Till Apps installieren darf. Erlauben Sie es, und tippen Sie dann erneut auf den Chip.
4. Einstellungen → Software-Update macht dasselbe und zeigt zusätzlich, welche Version Sie ausführen und welche verfügbar ist.
5. Das PIN-Feld für Manager und die Download-Schaltfläche erscheinen nur, wenn tatsächlich eine neue Version verfügbar ist — an einer aktuellen Kasse gibt es dort nichts anzutippen. Prüft die Kasse erneut, während Sie tippen, und stellt fest, dass Sie bereits auf dem neuesten Stand sind, teilt sie dies mit, statt etwas herunterzuladen.

Sie werden in zwei Fällen nach einer Manager-PIN gefragt: Sie sind als Kassierer angemeldet, oder die Kasse befindet sich im Selbstbedienungsmodus. Im Selbstbedienungsmodus würde die Installation auch den Kiosk entsperren, daher ist die PIN dort auch für einen Manager erforderlich.

Während der Ersteinrichtung teilt die Kasse mit, dass ein Update existiert, bietet aber nicht an, es zu installieren — schließen Sie zuerst die Einrichtung der Kasse ab und aktualisieren Sie dann über die Einstellungen.
