---
id: inventory
title: Bestand & Inventar
section: Ihr Geschäft einrichten
order: 120
summary: Verfolgt die vorrätigen Mengen je Artikel und Variante.
routes: [/inventory, /locations, /ui/inventory/stock-table]
# /locations is never screenshotted (docs-shots only captures routes[0]) —
# accepted gap, see e2e/tests-docs/lib.js's routedTopics() comment (ut-docs#900).
keywords: [bestand, waren, wareneingang, lagerorte, zählung]
---

# Bestand & Inventar

Verfolgt die vorrätigen Mengen je Artikel und Variante. Verkäufe verringern den Bestand automatisch; Wareneingänge und Korrekturen erfassen Lieferungen und Berichtigungen.

## Verwendung

1. Öffnen Sie „Bestand“, um die aktuellen Lagerbestände zu sehen.
2. Erfassen Sie eine Lieferung mit einem Wareneingang; verwenden Sie eine Korrektur für Schwund, Bruch oder Zählkorrekturen — geben Sie eine negative Menge ein, um Bestand zu entfernen (tippen Sie an einer Touch-Kasse zuerst auf die „-“-Taste der Bildschirmtastatur).
3. Die Bestandsseite prognostiziert, wie viele Tage der Bestand noch reicht, und schlägt vor, wie viel nachbestellt werden sollte; die Berichtsseite trägt ebenfalls einen Hinweis auf niedrigen Bestand.
4. Lagerorte (Standorte, nur Manager) sind geschäftsweit und werden immer von der **Hauptkasse** aus verwaltet: An einer beigetretenen Kasse zeigt das Anlegen, Umbenennen oder Deaktivieren eines Lagerorts stattdessen eine Meldung, die Sie zur Hauptkasse zurückverweist.

## Wenn Sie gar keinen Bestand führen

Manche Geschäfte zählen nie Bestand — sie möchten einfach, dass sich jeder Artikel jederzeit verkaufen lässt. Aktivieren Sie dazu **Einstellungen → Bestand → „Artikel ohne Bestandsführung verkaufen“**. Artikel werden dann auch verkauft, wenn die Kasse keinen Bestandssatz für sie hat, und kein Verkauf wird jemals wegen fehlendem Bestand abgelehnt.

Lassen Sie die Option aus, wenn die Kasse den Verkauf stoppen soll, sobald ein Artikel aufgebraucht ist. Das ist die Voreinstellung — und der Grund, warum ein aus einem System ohne Bestandsführung importierter Katalog zunächst gar nichts verkaufen kann, bis Sie diese Option einschalten.

Wenn Sie aus einem System importieren, das für einen Artikel **„Bestandsführung? Nein“** vermerkt, behandelt die Kasse dessen Mengenspalte nicht als echten Lagerbestand — der Import weist Sie darauf hin, dass die Menge nicht übernommen wurde, statt einen Bestand zu erfinden, den Ihr altes System nie behauptet hat.
