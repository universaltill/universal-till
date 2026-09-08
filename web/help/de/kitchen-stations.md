---
id: kitchen-stations
title: Küchenstationen
section: Den Betrieb führen
order: 235
summary: Leiten Sie jede Speisekategorie — oder einen einzelnen Artikel — an einen eigenen Küchendrucker oder Küchenbildschirm, sodass die Grillbestellung am Grill und die Getränkebestellung an der Bar gedruckt wird.
keywords: [küche, station, drucker, anzeige, bildschirm, routing, bon, grill, bar]
routes: [/kitchen-stations, /kitchen-display/{station_id}]
---

# Küchenstationen

Leiten Sie jede Speisekategorie — oder einen einzelnen Artikel — an einen eigenen Küchendrucker oder Küchenbildschirm, sodass die Grillbestellung am Grill und die Getränkebestellung an der Bar gedruckt wird.

## Verwendung

1. Öffnen Sie **Küchenstationen** über das Menü (nur Manager) und legen Sie für jeden Ort, an dem Speisen zubereitet werden, eine Station an — zum Beispiel „Grill“ oder „Bar“. Wählen Sie das **Ziel**: **Drucker** (ein Bon), **Anzeige** (ein Küchenbildschirm — siehe unten) oder **Drucker und Anzeige**. Eine druckende Station benötigt die Netzwerkadresse oder den Gerätepfad ihres Druckers; eine reine Anzeigestation nicht. Ist der Drucker bereits in diesem Netzwerk vorhanden, klicken Sie zuerst auf **Drucker in diesem Netzwerk suchen** — es listet alle gefundenen auf, sodass Sie einen auswählen können, statt die Adresse von Hand einzutippen; nichts wird hinzugefügt, bis Sie auch die Station speichern.
2. Aktivieren Sie unter **Kategorie-Routing** die Stationen, an denen die Artikel jeder Kategorie gedruckt werden sollen. Dies ist der Hauptweg der Zuordnung — ein Häkchen deckt jeden Artikel der Kategorie ab.
3. Für den seltenen Artikel, der woanders hingehen soll, suchen Sie ihn unter **Artikel-Ausnahmen** und aktivieren Sie seine Stationen. Eine Artikel-Ausnahme ersetzt die Kategorieregel nur für diesen einen Artikel.
4. Ist ein Verkauf abgeschlossen, druckt jede druckende Station ihren eigenen Bon mit nur ihren Zeilen. Ein an zwei Stationen geleiteter Artikel wird auf beiden gedruckt. Alles, was nirgendwohin geleitet wird — einschließlich eines Artikels, dessen einzige Station reine Anzeige ist —, wird wie bisher auf dem Standard-Küchendrucker aus den Einstellungen gedruckt.

## Küchenanzeige (ein Bildschirm statt, oder zusätzlich zu, einem Bon)

Eine Station mit dem Ziel **Anzeige** oder **Drucker und Anzeige** hat ihren eigenen Live-Bestellbildschirm. Klicken Sie neben der Station auf **Anzeige ansehen**, um sie zu öffnen, und verschieben Sie dieses Fenster dann auf den zweiten, an diese Kasse angeschlossenen Monitor — es ist eine Seite dieser Kasse, es muss also nichts gekoppelt oder vernetzt werden.

- Der Bildschirm listet die Bestellungen, die mindestens einen an diese Station geleiteten Artikel haben, neueste zuerst, mit denselben Ein-Tipp-Schaltflächen **In Zubereitung** / **Fertig** / **Abgeholt** wie die Seite „Bestellungen“. Er aktualisiert sich selbst alle paar Sekunden und sofort, wenn sich der Status einer Bestellung ändert.
- Der Status gehört zur ganzen Bestellung, nicht zu jedem Artikel: Eine Bestellung mit Artikeln für zwei Stationen erscheint auf beiden Bildschirmen, und das Markieren als Fertig oder Abgeholt auf einem der beiden Bildschirme aktualisiert beide.
- Der Bildschirm zeigt nur Bestellungen, die an **dieser** Kasse aufgenommen wurden — anders als die eigene Bestellungsseite der Kasse zeigt er keine Bestellungen einer anderen Kasse an, auch nicht nach dem Synchronisieren. In einem Geschäft mit mehreren verbundenen Kassen öffnen Sie die Küchenanzeige an der Kasse, die die entsprechenden Bestellungen tatsächlich aufnimmt.
- Der Bildschirm einer deaktivierten Station funktioniert nicht mehr, bis Sie sie reaktivieren; eine reine Druckerstation hat keinen Bildschirm.

## Gut zu wissen

- Deaktivieren Sie eine Station, statt sie zu löschen — ihre Artikel fallen auf den Standard-Küchendrucker zurück, bis Sie sie reaktivieren.
- Ein nicht erreichbarer Drucker blockiert nie die anderen Stationen oder den Verkauf selbst.
- Stationen und Routing sind geschäftsweit und werden immer von der **Hauptkasse** aus verwaltet: An einer beigetretenen Kasse zeigt das Hinzufügen einer Station, das Umbenennen, das Ändern ihres Ziels oder das Bearbeiten des Kategorie-/Artikel-Routings eine Meldung, die Sie zur Hauptkasse zurückverweist, statt eine nur lokal gültige Änderung anzunehmen.
- Die **Druckeradresse** einer Station ist die eine Ausnahme, und das ist Absicht — pro Kasse: Öffnen Sie Küchenstationen an einer beigetretenen Kasse, können Sie dort weiterhin die eigene Adresse dieser Station festlegen, da an jeder Kasse für dieselbe gemeinsame Station ein eigener Drucker angeschlossen sein kann. Bis Sie sie festlegen, drucken die Bons dieser Station auf dem eigenen Standard-Küchendrucker der beigetretenen Kasse aus den Einstellungen.
