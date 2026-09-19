---
id: vouchers
title: Gutscheine
section: Täglicher Verkauf
order: 25
summary: Verkaufen Sie Mehrzweck-Gutscheine über den Reiter „Aufteilen“ des Zahlungsfeldes und nehmen Sie sie als Zahlung an — prüfen Sie das Guthaben vor dem Abschluss, und der Beleg druckt jeden ausgegebenen Code.
keywords: [gutschein, geschenkgutschein, geschenkkarte, voucher, einlösen, guthaben, ausgeben, code]
---

# Gutscheine

Ein Gutschein ist Geld, das eine Kundin jetzt bezahlt, um es später auszugeben — für alles im Laden, an jeder Kasse. Die Kasse führt das Guthaben jedes Gutscheins unter seinem **Code**: dem Code, der auf einer physischen Karte aufgedruckt ist, die Sie aushändigen, oder einem, den die Kasse selbst erzeugt, wenn Sie das Codefeld leer lassen. Sowohl der Verkauf eines Gutscheins als auch die Annahme eines Gutscheins als Zahlung geschehen im Reiter **Aufteilen** des Zahlungsfeldes (zum Reiter selbst siehe [Zahlungen](/help/payments)).

## Einen Gutschein verkaufen

1. Tippen Sie auf **Zahlung**, wechseln Sie zum Reiter **Aufteilen** und öffnen Sie **Gutschein verkaufen** am unteren Rand des Feldes.
2. Geben Sie den Wert des Gutscheins unter **Gutscheinwert** ein — ein normaler Währungsbetrag, z. B. `25.00`.
3. **Code**: Tippen Sie den Code ein, der auf der Karte steht, die Sie aushändigen. Lassen Sie das Feld leer, erzeugt die Kasse den Code beim Abschluss selbst.
4. **Für** ist optional — ein Name, um festzuhalten, für wen der Gutschein gekauft wurde.
5. **Gutschein hinzufügen**. Er erscheint in einer Liste ausstehender Gutscheine mit seinem Code (oder *Code wird beim Abschluss erzeugt*), seinem Wert und dem Namen, jeweils mit einem ✕ zum Entfernen. Der Wert wird zu dem addiert, was die Kundin schuldet — **Rest auffüllen** rechnet ihn mit ein, die Rechnung geht also auf wie bei jedem anderen Verkauf.
6. Nehmen Sie die Zahlung wie gewohnt entgegen (Bargeld, Karte, …) und **Verkauf abschließen**. Der Beleg listet jeden Gutschein unter **Ausgegebene Gutscheine** mit seinem endgültigen Code auf. Bei einem erzeugten Code ist dieser Beleg die einzige Stelle, an der der Code jemals angezeigt wird — händigen Sie ihn also aus oder schreiben Sie den Code auf die Karte.

Ein Gutschein kann allein verkauft werden, ohne irgendetwas anderes im Warenkorb, oder zusammen mit Waren in einem Verkauf. Der Verkauf eines Gutscheins ist kein Warenumsatz: Er wird als Geld erfasst, das demjenigen geschuldet wird, der ihn später ausgibt, und die Ware wird erst versteuert, wenn der Gutschein eingelöst wird — siehe die Hinweise zum Tagesabschluss unten.

Was schiefgehen kann:

- Ein Wert von null wird abgelehnt, bevor er hinzugefügt wird.
- Ein Code, der bereits vergeben ist, wird beim Drücken von **Verkauf abschließen** abgelehnt, mit einer entsprechenden Meldung — die Liste der ausstehenden Gutscheine bleibt erhalten, korrigieren Sie also den Code und versuchen Sie es erneut.
- Ein Verkauf kann bis zu 50 Gutscheine ausgeben.

## Einen Gutschein als Zahlung annehmen

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

1. Wählen Sie unter **Gutschein verkaufen** bei **Gutscheinart** die Option **Bestimmter Artikel (jetzt versteuert)** (die Vorgabe **Jede Verwendung** ist der oben beschriebene gewöhnliche Gutschein).
2. Ein Feld **MwSt.-Satz** erscheint — geben Sie den Satz des Artikels in Prozent ein, z. B. `19` oder `7`.
3. Füllen Sie Wert, Code und Namen wie gewohnt aus und **Gutschein hinzufügen**. Die Liste der ausstehenden Gutscheine kennzeichnet ihn als *Bestimmter Artikel* mit seinem Satz. Der Verkauf erfasst die Umsatzsteuer zu diesem Satz sofort, der Gutschein erscheint also wie eine normale Verkaufsposition in den Steuersätzen des Tages — nicht im Abschnitt **GUTSCHEINE** für Verbindlichkeiten.

Einlösen:

1. Wählen Sie im Reiter **Aufteilen** die Methode **Gutschein**, tippen Sie den Code ein und dann **Guthaben prüfen**.
2. Bei einem Gutschein für einen bestimmten Artikel trägt die Kasse keinen Betrag ein — sie zeigt stattdessen Wert und Namen des Gutscheins neben einer Schaltfläche **Einlösen**. Ein solcher Gutschein kann nie als Zahlung verwendet werden: Er ist bereits versteuert, ihn als Zahlung anzunehmen würde dasselbe Geld zweimal versteuern.
3. Händigen Sie den Artikel aus und tippen Sie auf **Einlösen**. Das ist endgültig: Der Gutschein wird sofort als eingelöst markiert, ohne dass danach etwas abzuschließen oder zu entfernen wäre. Es wird keine weitere Zahlung und keine weitere Steuer erfasst — das hat der Verkauf des Gutscheins bereits getan.

Was schiefgehen kann:

- Ein leerer, negativer oder über 100 liegender MwSt.-Satz wird abgelehnt, bevor der Gutschein hinzugefügt wird.
- Das Tippen auf **Einlösen** bei einem bereits eingelösten Gutschein, oder das Scannen eines Gutscheins für einen bestimmten Artikel im Verkaufsbildschirm, wird mit einer entsprechenden Meldung abgelehnt.
- Ein an einer anderen Kasse verkaufter Gutschein für einen bestimmten Artikel lässt sich hier prüfen, aber noch nicht einlösen — lösen Sie ihn an der Hauptkasse oder an der Kasse ein, die ihn verkauft hat.

## Tagesabschluss und andere Kassen

Der Tagesabschlussbericht (Z-Bericht) hat einen eigenen Abschnitt **GUTSCHEINE** für die an diesem Tag verkauften und eingelösten Gutscheine, und das Stornieren eines Verkaufs, mit dem ein Gutschein verkauft wurde, storniert den Gutschein mit — allerdings nur, solange er unbenutzt ist; siehe [Berichte & Tagesabschluss](/help/reports). Ein an einer Kasse verkaufter Gutschein kann an jeder anderen Kasse im Laden eingelöst werden; wie das funktioniert, während eine Kasse offline ist, steht unter [Mehrere Kassen (ein Laden)](/help/multitill).
