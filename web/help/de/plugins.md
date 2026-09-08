---
id: plugins
title: Plugin-Store
section: Verbinden & erweitern
order: 330
summary: "Fügen Sie Funktionen hinzu, ohne die Kernanwendung zu ändern: Zahlungen, Designs, Sprachpakete, Integrationen, KI-Werkzeuge und mehr."
routes: [/plugins, /plugins/store, /plugins/{id}/settings]
keywords: [plugin, marktplatz, installieren, erweiterung, design]
---

# Plugin-Store

Fügen Sie Funktionen hinzu, ohne die Kernanwendung zu ändern: Zahlungen, Designs, Sprachpakete, Integrationen, KI-Werkzeuge und mehr. Jedes Plugin wird signiert und verifiziert, bevor es läuft.

## Verwendung

1. Öffnen Sie Plugins → Store, um den Katalog zu durchsuchen.
2. Installieren Sie mit einem Klick; Plugins tragen Vertrauensabzeichen (Gold = offiziell Universal Till, Grün = verifizierter Entwickler), und nicht verifizierte Herausgeber bitten zuerst um Ihre Bestätigung.
3. Ein als **Kostenpflichtig** markiertes Plugin benötigt eine Berechtigung, die Ihr Vertriebsmanager im Marktplatz-Portal genehmigt, bevor es installiert werden kann — der Versuch, es ohne Genehmigung herunterzuladen, zeigt statt eines Downloads eine erklärende Meldung.
4. Jedes Plugin hat seine eigene Einstellungsseite; manche Einstellungen gelten geschäftsweit, andere pro Kasse. Ein Steuer-Plugin, das Außer-Haus-Satz-Überschreibungen unterstützt, zeigt hier einen eigenen Editor statt Rohtext: Geben Sie den Außer-Haus-Prozentsatz neben jedem Steuercode ein, und jeder Artikel mit diesem Code wechselt bei einer Außer-Haus-Bestellung zu diesem Satz. Lassen Sie ein Feld leer, um den Vor-Ort-Satz zu berechnen. Die Einstellungsseite des KI-Assistent-Plugins zeigt außerdem über dem API-Schlüssel-Feld einen Hinweis: Die Wahl eines gehosteten Anbieters (Anbieter = Claude oder OpenAI) sendet, womit die KI-Funktionen arbeiten, an die Server dieses Anbieters, während die selbstgehostete Standardoption alles auf Ihrer eigenen Hardware behält — lesen Sie dies, bevor Sie einen Schlüssel eingeben.
5. Ein installiertes Plugin mit eigener Dokumentation zeigt eine Schaltfläche „Dokumentation“ auf seiner Karte, die diese Dokumentation direkt in der Kasse öffnet.
6. Installieren und entfernen Sie Plugins in einem Geschäft mit mehreren Kassen nur auf der **Hauptkasse**: Jede beigetretene Kasse lädt dasselbe Plugin selbst aus dem Store und wendet die Änderung automatisch innerhalb von etwa einer halben Minute an. Die eigenen Aktionen einer beigetretenen Kasse zum Installieren, Deinstallieren, Aktivieren/Deaktivieren und Aktualisieren werden mit einer Meldung abgelehnt, die auf die Hauptkasse verweist. Aus einer Datei importierte Plugins sind die Ausnahme — der Import funktioniert weiterhin auf jeder Kasse, auch beigetretenen, das Plugin bleibt jedoch nur auf der Kasse, auf der es importiert wurde, und wird nie auf die anderen kopiert.
7. Zeigt ein Plugin auf der Plugins-Seite ein rotes Abzeichen **Defekt ⚠**, sind seine Dateien auf dieser Kasse fehlend oder nicht lesbar (dies kann direkt nach dem Beitritt einer Kasse zum Geschäft passieren). Wie es sich erholt, hängt von der Herkunft ab: Ein aus dem Store installiertes Plugin wird automatisch innerhalb von etwa einer halben Minute neu installiert — keine Aktion nötig. Ein aus einer Datei importiertes Plugin hat keinen Store-Eintrag zum erneuten Abrufen, wird also **nicht** automatisch repariert — importieren Sie die Plugin-Datei auf dieser Kasse erneut, um es zu beheben. Bis das Plugin sich erholt, können Artikel, deren Steuersatz es bestimmt, auf dieser Kasse nicht verkauft werden: Ist nur ein Artikel im Warenkorb betroffen, nennt der Verkaufsbildschirm ihn, und das Entfernen lässt den Rest des Verkaufs abschließen; ist jeder Artikel betroffen (zum Beispiel wenn das defekte Plugin das einzige Steuer-Plugin der Kasse ist), ist das Kassieren an dieser Kasse nicht verfügbar, bis sich das Plugin erholt hat — warten Sie einen Moment und versuchen Sie es erneut.
8. Ein Plugin, das Sie aus dem Store herunterladen, aber nicht sofort installieren, bleibt 48 Stunden lang bereitgestellt; verstreicht dieses Zeitfenster, wechselt sein Abzeichen **Heruntergeladen** still zurück zu einer einfachen Schaltfläche **Herunterladen**, und es muss vor der Installation erneut heruntergeladen werden. Dies ist routinemäßige Speicherplatzpflege, kein Fehler — nichts geht verloren, da Sie es einfach erneut herunterladen können.
