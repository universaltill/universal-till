---
id: vouchers
title: Gutscheine
section: Täglicher Verkauf
order: 25
summary: Verkaufen Sie Gutscheine über einen Gutschein-Artikel — Karte scannen oder die Kasse eine Nummer erzeugen lassen — und nehmen Sie sie als Zahlung an, per Scan im Verkaufsbildschirm oder im Reiter „Aufteilen“ des Zahlungsfeldes.
keywords: [gutschein, geschenkgutschein, geschenkkarte, voucher, einzweck, bestimmter artikel, einlösen, guthaben, ausgeben, code, kartennummer, artikeltyp, korrigieren, Korrektur, Guthaben korrigieren]
routes: [/vouchers, /vouchers/{id}]
---

# Gutscheine

Ein Gutschein ist Geld, das eine Kundin jetzt bezahlt, um es später auszugeben — für alles im Laden, an jeder Kasse. Die Kasse führt das Guthaben jedes Gutscheins unter seiner **Kartennummer**: der Nummer, die auf einer physischen Karte aufgedruckt ist, die Sie aushändigen, oder einer 13-stelligen Nummer, die die Kasse erzeugt, wenn Sie die Nummer leer lassen. Einen Gutschein verkaufen Sie, indem Sie im Verkaufsbildschirm auf einen **Gutschein**-Artikel tippen; als Zahlung nehmen Sie ihn per Scan oder im Reiter **Aufteilen** des Zahlungsfeldes an (zum Reiter selbst siehe [Zahlungen](/help/payments)).

## Einen Gutschein verkaufen

Zum Verkaufen brauchen Sie einen Artikel, dessen **Typ** im Katalog *Gutschein* ist (siehe [Katalog](/help/catalog)) — legen Sie ihn einmal an. Lassen Sie seinen Preis leer, wird jedes Mal nach dem Wert gefragt; geben Sie ihm einen Preis (z. B. *Gutschein 25 €*), startet das Fenster mit diesem Wert.

1. Tippen Sie im Verkaufsbildschirm auf den Gutschein-Artikel (oder scannen Sie seinen Barcode). Das Fenster **Gutschein** öffnet sich.
2. **Kartennummer**: Scannen Sie die Karte, die Sie aushändigen — das Feld ist für Ihren Scanner bereit, sobald das Fenster offen ist — oder tippen Sie ihre Nummer ein. Lassen Sie es leer, erzeugt die Kasse eine Nummer, wenn Sie auf **Hinzufügen** tippen.
3. **Wert**: der Wert des Gutscheins, ein normaler Währungsbetrag, z. B. `25,00`. Hat der Artikel einen Preis, steht dieser schon im Feld.
4. **Für** ist optional — ein Name, um festzuhalten, für wen der Gutschein gekauft wurde.
5. **Einlösbar für**: **Jeden Einkauf** ist der gewöhnliche Gutschein; **Bestimmter Artikel (jetzt versteuert)** ist unten beschrieben. Tippen Sie auf **Hinzufügen**.
6. Der Gutschein kommt als eigene Position in den Warenkorb und zeigt seine Kartennummer. Nehmen Sie die Zahlung wie gewohnt entgegen (**Zahlung**, Bargeld, Karte, …). Der Beleg listet jeden Gutschein unter **Ausgestellte Gutscheine** mit seiner Nummer, für wen er ist, und der Nummer als Barcode auf dem gedruckten Beleg auf (am Bildschirm bei Nummern, die die Kasse erzeugt), sodass ein späteres Scannen des Belegs ihn wie eine Karte einlöst. Bei einer von der Kasse erzeugten Nummer findet die Kundin sie auf diesem Beleg — händigen Sie ihn also aus oder schreiben Sie die Nummer auf die Karte.

Ein Gutschein kann allein verkauft werden, ohne irgendetwas anderes im Warenkorb, oder zusammen mit Waren in einem Verkauf. Seine Warenkorbposition ist immer genau ein Gutschein zu seinem Wert: ohne Mengentasten und ohne Positionsrabatt, und eine Aktion oder ein Warenkorbrabatt verringert ihn nie. Tippen Sie auf ✕ an der Position, um den Gutschein zu verwerfen — ohne Grundabfrage, denn nichts hat den Laden verlassen. Der Verkauf eines Gutscheins ist kein Warenumsatz und bewegt keinen Bestand: Er wird als Geld erfasst, das demjenigen geschuldet wird, der ihn später ausgibt, und die Ware wird erst versteuert, wenn der Gutschein eingelöst wird — siehe die Hinweise zum Tagesabschluss unten. Ein geparkter Verkauf behält seine Gutscheinpositionen, und der Gutschein wird ausgegeben, wenn der Verkauf bezahlt ist.

Was schiefgehen kann:

- **Hinzufügen** bleibt ausgegraut, bis der Wert über null liegt.
- Eine Kartennummer, die schon vergeben ist — früher verkauft oder bereits auf einer anderen Position dieses Verkaufs — wird im Fenster mit einer entsprechenden Meldung abgelehnt, bevor etwas hinzugefügt wird. Scannen Sie eine andere Karte oder lassen Sie die Nummer leer.
- Ein Verkauf kann bis zu 50 Gutscheine verkaufen.

## Einen Gutschein als Zahlung annehmen

Am schnellsten: Scannen Sie den Barcode des Gutscheins — die Karte oder die auf dem Beleg gedruckte Nummer — im Verkaufsbildschirm. Die Kasse schlägt ihn nach und bietet im Zahlungsfeld eine Schaltfläche **Gutschein** für sein Guthaben an, höchstens so viel, wie der Verkauf noch braucht. Um die Nummer stattdessen einzutippen oder die Zahlung aufzuteilen:

1. Wählen Sie im Reiter **Aufteilen** die Methode **Gutschein**. Ein Feld **Gutscheincode** erscheint, und das Feld „Wechselgeld“ verschwindet — auf einen Gutschein wird nie Wechselgeld herausgegeben.
2. Tippen Sie den Code von der Karte der Kundin ein.
3. **Guthaben prüfen** (optional) schlägt den Gutschein nach und zeigt sein Restguthaben, bevor Sie abschließen. Ist das Betragsfeld noch leer, wird auch gleich der naheliegende Betrag eingetragen — das Guthaben, oder der noch offene Betrag, falls dieser kleiner ist. Kann die Kasse die Hauptkasse gerade nicht erreichen, sagt die Prüfung das einfach; Sie können die Zahlung trotzdem hinzufügen, und die eigentliche Prüfung erfolgt beim Abschluss des Verkaufs.
4. Geben Sie den **Betrag** ein, der vom Gutschein abgebucht werden soll (höchstens sein Guthaben, und nicht mehr, als der Verkauf noch benötigt), und dann **Zahlung hinzufügen**. Zahlen Sie einen Rest mit einer anderen Methode und dann **Verkauf abschließen**. Der Beleg zeigt den Gutscheincode bei dieser Zahlung an.

Was schiefgehen kann:

- **Zahlung hinzufügen** mit leerem Codefeld wird abgelehnt.
- Ein Code, den die Kasse nicht kennt, oder ein Gutschein, der storniert oder vollständig ausgegeben wurde, wird abgelehnt — bei **Guthaben prüfen**, falls Sie es verwenden, sonst beim Abschluss des Verkaufs.
- Ein Betrag über dem Guthaben des Gutscheins wird abgelehnt („Gutscheinguthaben deckt diese Zahlung nicht“) — nehmen Sie das verbleibende Guthaben und den Rest auf anderem Weg.
- Ein Betrag über dem, was der Verkauf noch benötigt, wird ebenfalls abgelehnt, da ein Gutschein kein Wechselgeld geben kann — geben Sie stattdessen den Restbetrag ein.
- Ein Gutschein kann pro Verkauf einmal verwendet werden, und ein Gutschein, der im selben Verkauf verkauft wird, kann diesen nicht bezahlen.

Die ältere Methode **Geschenkkarte** in derselben Liste ist eine einfache Zahlungsmethode, die kein Guthaben führt — verwenden Sie **Gutschein** für Gutscheine, die diese Kasse ausgegeben hat.

## Gutscheine für einen bestimmten Artikel

Die meisten Gutscheine gelten für **jede Verwendung** — die Kundin entscheidet später, wofür sie ihn ausgibt, und die Ware wird versteuert, wenn der Gutschein eingelöst wird. Ein Gutschein für einen **bestimmten Artikel** (ein konkretes Produkt oder eine konkrete Leistung, deren Umsatzsteuersatz damit schon feststeht — ein *Einzweck-Gutschein*) wird stattdessen beim Verkauf versteuert, und das spätere Einlösen ist einfach die Übergabe des Artikels. Die Kasse hält die beiden Arten auseinander.

Verkauf:

1. Wählen Sie im Fenster **Gutschein** unter **Einlösbar für** die Option **Bestimmter Artikel (jetzt versteuert)** (die Vorgabe **Jeden Einkauf** ist der oben beschriebene gewöhnliche Gutschein).
2. Der Gutschein wird mit dem Steuercode des Gutschein-Artikels selbst versteuert — geben Sie diesem Artikel im Katalog den Satz des Artikels (z. B. einen Artikel *Kaffee-Gutschein* mit dem 7-%-Code).
3. Füllen Sie Kartennummer, Wert und Namen wie gewohnt aus und tippen Sie auf **Hinzufügen**. Die Warenkorbposition zeigt *Bestimmter Artikel (jetzt versteuert)*. Der Verkauf erfasst die Umsatzsteuer zu diesem Satz sofort, der Gutschein erscheint also wie eine normale Verkaufsposition in den Steuersätzen des Tages — nicht im Abschnitt **GUTSCHEINE** für Verbindlichkeiten.

Einlösen:

1. Wählen Sie im Reiter **Aufteilen** die Methode **Gutschein**, tippen Sie den Code ein und dann **Guthaben prüfen**.
2. Bei einem Gutschein für einen bestimmten Artikel trägt die Kasse keinen Betrag ein — sie zeigt stattdessen Wert und Namen des Gutscheins neben einer Schaltfläche **Einlösen**. Ein solcher Gutschein kann nie als Zahlung verwendet werden: Er ist bereits versteuert, ihn als Zahlung anzunehmen würde dasselbe Geld zweimal versteuern.
3. Händigen Sie den Artikel aus und tippen Sie auf **Einlösen**. Das ist endgültig: Der Gutschein wird sofort als eingelöst markiert, ohne dass danach etwas abzuschließen oder zu entfernen wäre. Es wird keine weitere Zahlung und keine weitere Steuer erfasst — das hat der Verkauf des Gutscheins bereits getan.

Was schiefgehen kann:

- Ein Gutschein-Artikel ohne Steuercode wird mit dem Standardsatz des Ladens versteuert — setzen Sie zuerst den richtigen Steuercode am Artikel.
- Das Tippen auf **Einlösen** bei einem bereits eingelösten Gutschein, oder das Scannen eines Gutscheins für einen bestimmten Artikel im Verkaufsbildschirm, wird mit einer entsprechenden Meldung abgelehnt.
- Ein an einer anderen Kasse verkaufter Gutschein für einen bestimmten Artikel lässt sich hier prüfen, aber noch nicht einlösen — lösen Sie ihn an der Hauptkasse oder an der Kasse ein, die ihn verkauft hat.

## Die Seite Geschenkgutscheine

**Menü → Geschenkgutscheine** (nur Manager) listet jeden Gutschein, den diese Kasse kennt, den neuesten zuerst: Kartennummer, für wen er ist, ursprünglicher Wert, Restguthaben, Status — **Aktiv**, **Aufgebraucht** oder **Storniert** — und wofür er eingelöst werden kann (ein Gutschein für einen bestimmten Artikel ist markiert). **Verkauft in** verlinkt den Bon des Verkaufs, in dem er verkauft wurde; bei einem importierten Guthaben steht das stattdessen dort.

1. Tippen Sie in das Feld **Suchen**, um einen Gutschein nach Kartennummer oder Inhaber zu finden — die Liste aktualisiert sich beim Tippen. Mit **Status** zeigen Sie nur aktive, aufgebrauchte oder stornierte Gutscheine.
2. Tippen Sie auf eine Kartennummer, um den **Verlauf** des Gutscheins zu sehen: wann er ausgestellt wurde, wann er jeweils eingelöst wurde und jede Korrektur, mit Betrag und einem Link zum Bon jedes Verkaufs.
3. **CSV exportieren** lädt jeden Gutschein herunter, der zur aktuellen Suche und zum Status passt — alle, nicht nur die auf dem Bildschirm — mit den Spalten `code`, `balance`, `label`, `original_amount`, `status`, `purpose` und `created_at`. Die Datei lässt sich an einer anderen Kasse mit **Gutschein-Guthaben importieren** unten auf derselben Seite einlesen (siehe [Gutschein-Guthaben importieren](/help/voucher-import)).

Gut zu wissen:

- Die Seite zeigt die neuesten 200 Gutscheine. Gibt es mehr, steht das dort — suchen oder filtern Sie, um ältere zu finden.
- Gibt es noch keine Gutscheine, sagt die Seite, wie Sie beginnen: einen mit einem Gutschein-Artikel verkaufen oder Guthaben aus Ihrem bisherigen System importieren.
- An einer zusätzlichen Kasse zeigt die Liste nur die Gutscheine, die diese Kasse kennt — hier verkauft, importiert oder eingelöst. Die vollständige Liste ist an der Hauptkasse.

## Ein Guthaben korrigieren

Stimmt das Guthaben eines Gutscheins nicht — der Wert wurde beim Verkauf falsch eingetippt, oder eine beschädigte Karte wird ersetzt —, kann ein Manager auf der Seite des Gutscheins das richtige Guthaben eintragen. Dabei fließt kein Geld: Die Kasse erfasst die Korrektur, wer sie vorgenommen hat und warum.

1. Öffnen Sie **Menü → Geschenkgutscheine** und tippen Sie auf die Kartennummer des Gutscheins.
2. Prüfen Sie unter **Guthaben korrigieren** das **Aktuelle Guthaben** und tippen Sie dann das **Neue Guthaben** ein — den vollen Betrag, den der Gutschein ab jetzt haben soll, nicht die Differenz.
3. Geben Sie einen **Grund** ein (Pflicht, bis zu 200 Zeichen), z. B. *Betrag beim Verkauf falsch eingetippt*.
4. Tippen Sie auf **Korrektur speichern**. Die Seite lädt neu mit **Guthaben korrigiert.**, dem neuen Guthaben und einer Zeile **Korrektur** im **Verlauf**, die die Differenz (+ oder −), den Grund und den Namen zeigt.

Eine Korrektur auf 0 markiert den Gutschein als **Aufgebraucht**; wird das Guthaben eines aufgebrauchten Gutscheins erhöht, ist er wieder **Aktiv**. Der ursprüngliche Wert des Gutscheins ändert sich nie. Der Tagesabschlussbericht (Z-Bericht) führt die Korrekturen des Tages in einer eigenen Zeile **Adjusted** im Abschnitt **GUTSCHEINE** — nie als Verkauf, Zahlung oder Umsatzsteuer.

Was schiefgehen kann:

- Nur ein Manager oder Administrator kann ein Guthaben korrigieren.
- Ein stornierter Gutschein oder ein Gutschein für einen bestimmten Artikel lässt sich nicht korrigieren — die Seite sagt stattdessen, warum.
- „Das neue Guthaben entspricht dem aktuellen Guthaben“ — es gibt nichts zu korrigieren.
- „Das Guthaben dieses Gutscheins hat sich geändert, während Sie ihn angesehen haben“ — jemand hat ihn inzwischen eingelöst oder korrigiert. Laden Sie die Seite neu, prüfen Sie das neue Guthaben und korrigieren Sie erneut, falls nötig.
- An einer zusätzlichen Kasse wird die Korrektur an der Hauptkasse vorgenommen. Ist die Hauptkasse nicht erreichbar, wird nichts geändert und die Seite sagt das — versuchen Sie es erneut, wenn sie wieder erreichbar ist. Meldet die Seite, dass die Hauptkasse die Korrektur nicht bestätigt hat, wurde sie vielleicht gespeichert, vielleicht auch nicht: Laden Sie die Seite neu und prüfen Sie den Verlauf des Gutscheins, bevor Sie es erneut versuchen.

## Tagesabschluss und andere Kassen

Der Tagesabschlussbericht (Z-Bericht) hat einen eigenen Abschnitt **GUTSCHEINE** für die an diesem Tag verkauften, eingelösten und korrigierten Gutscheine, und das Stornieren eines Verkaufs, mit dem ein Gutschein verkauft wurde, storniert den Gutschein mit — allerdings nur, solange er unbenutzt ist; siehe [Berichte & Tagesabschluss](/help/reports). Ein an einer Kasse verkaufter Gutschein kann an jeder anderen Kasse im Laden eingelöst werden; wie das funktioniert, während eine Kasse offline ist, steht unter [Mehrere Kassen (ein Laden)](/help/multitill).
