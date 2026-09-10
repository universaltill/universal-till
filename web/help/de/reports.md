---
id: reports
title: Berichte & Tagesabschluss
section: Den Betrieb führen
order: 210
summary: Umsatzsummen nach Tag, Abteilung und Zahlungsart; Bestseller und Ladenhüter; tote Ware; umsatzstärkste Tage und Stunden; Margen; Steuerübersicht; Jahresvergleich — plus der Tagesabschlussbericht (Z) zum Kassieren.
routes: [/reports, /journal, /journal/{receipt}, /shifts, /audit]
keywords: [z-bericht, tagesabschluss, tageseinnahmen, journal, kassenjournal, schicht, prüfprotokoll, trinkgeld, tronc, servicegebühr, mitarbeiterzuweisung]
---

# Berichte & Tagesabschluss

Umsatzsummen nach Tag, Abteilung und Zahlungsart; Bestseller und Ladenhüter; tote Ware; umsatzstärkste Tage und Stunden; Margen; Steuerübersicht; Jahresvergleich — plus der Tagesabschlussbericht (Z) zum Kassieren.

## Verwendung

1. Öffnen Sie „Berichte“: Die Zeile oben zeigt immer Ihre Kennzahlen für den gewählten Zeitraum (Umsatz, Verkäufe, Durchschnittsverkauf, Steuer, Rückerstattungen, Netto, Vorjahr) und eine Warnung bei niedrigem Bestand.
2. Wählen Sie darunter einen Reiter — Umsatztrend, Artikel, Steuer, Prognose, Zahlungen & Kanäle, Trinkgeld oder Tagesabschluss (EOD) — und dieser Bericht lädt beim Öffnen.
3. Führen Sie Tagesabschluss (im Reiter „Tagesabschluss“) beim Schließen aus: Er summiert alles **seit Ihrem letzten Abschluss** — nicht den Kalendertag — und kann für Ihre Unterlagen gedruckt werden. Ein Café, das gestern um 19:19 Uhr geschlossen hat und heute um 19:19 Uhr schließt, erhält jeden Verkauf dazwischen im heutigen Bericht, einschließlich der Verkäufe von gestern Nacht nach 19:19 Uhr, die ein Kalendertag-Bericht sonst gestrandet hätte; alles, was nach dem heutigen Abschluss gebucht wird, gehört zum nächsten. Jeder Abschluss zeichnet den genauen Zeitpunkt auf, von dem bis zu dem er reicht, sodass Sie immer genau sehen können, was ein Bericht abdeckt: Der gedruckte Bericht trägt eine **Zeitraum**-Zeile, und die Archivliste zeigt dieselben beiden Zeitstempel nebeneinander (`2026-08-23T19:10:00+02:00 – 2026-08-24T19:19:00+02:00`), in Ihrer eigenen Ortszeit. Ihr allererster Abschluss hat keinen früheren Abschluss, von dem aus er startet, und deckt daher alles bis zum Moment des Abschlusses ab und zeigt nur diesen einen Endzeitstempel.

## Berichtszeiträume

Neben der oberen Zeile wählen Sie, wie der Zeitraum berechnet wird:

- **Benutzerdefiniert** — das ursprüngliche rollierende Fenster (heute, 7/14/30/90 Tage zurück ab jetzt).
- **Tag / Woche / Monat / Jahr** — ein echter Kalenderzeitraum statt eines
  rollierenden Zählers: Tag ist ein Handelstag, Woche ist Montag bis
  Sonntag, Monat ist ein Kalendermonat, Jahr ist ein Kalenderjahr. Neben
  der Auswahl erscheint eine Datumsauswahl, damit Sie einen vergangenen
  Zeitraum betrachten können — wählen Sie z. B. Monat und ein Datum im
  Juli, um die Zahlen für Juli zu sehen, auch wenn heute im August ist.
- Umsatztrend, Artikel, Steuer und Zahlungen & Kanäle verwenden jeweils den
  ausgewählten Zeitraum, sodass sie immer mit den Zahlen oben übereinstimmen.
  Prognose und die Archivliste des Tagesabschlusses tun dies nicht — sie
  zeigen unabhängig vom Zeitraum-Umschalter ihre eigenen festen Fenster.

## Geschäftstagsbeginn

Standardmäßig läuft ein Berichts-„Tag“ von Mitternacht bis Mitternacht. Handeln
Sie über Mitternacht hinaus — eine Bar, eine späte Küche —, teilt dies die
Einnahmen einer Nacht auf zwei Berichtstage auf. Stellen Sie **Geschäftstag
beginnt um** (im Reiter Tagesabschluss, neben der automatischen
Tagesabschlusszeit) auf den tatsächlichen Beginn Ihres Handelstages ein, z. B.
06:00, und Tag/Woche/Monat/Jahr richten sich nach Ihrem echten Handelstag statt
nach der Uhr.

Diese Verschiebung gilt auch für das Diagramm der umsatzstärksten Stunde im
Umsatztrend — ein kurz nach Mitternacht getätigter Verkauf kann unter einer
Stundenbeschriftung vor Mitternacht (z. B. 22:00) erscheinen, konsistent damit,
dass Tag/Woche/Monat/Jahr diesen Verkauf bereits als Teil des vorherigen
Geschäftstages zählen.

## Geschenkgutscheine im Tagesabschlussbericht (Z)

Verkauft oder akzeptiert Ihr Geschäft Mehrzweck-Geschenkgutscheine, zeigt der
gedruckte Tagesabschlussbericht einen separaten Abschnitt **GUTSCHEINE**: wie
viele Gutscheine an diesem Tag ausgestellt und eingelöst wurden und in welcher
Höhe. Der Verkauf eines Gutscheins wird als dem künftigen Inhaber geschuldetes
Geld erfasst, nicht als Produktumsatz — der Betrag erscheint also in den
Gesamteinnahmen des Tages, aber nie in den Zahlen je Abteilung oder Steuersatz.
Die Steuer auf die Waren wird erst erfasst, wenn der Gutschein später
ausgegeben wird, zu den jeweils eigenen Sätzen dieser Waren, genau wie bei
Barzahlung. Der Abschnitt wird nur an Tagen mit Gutscheinaktivität gedruckt.

Das Stornieren des Verkaufs, der einen Gutschein ausgestellt hat, storniert den
Gutschein mit — solange er noch nicht eingelöst wurde — er verschwindet aus dem
Bericht und kann nicht mehr ausgegeben werden. Wurde bereits ein Teil des
Gutscheins ausgegeben, verweigert die Kasse die Stornierung dieses Verkaufs:
Klären Sie den ausstehenden Gutschein zuerst mit dem Kunden.

## Stornierungen und wer den Abschluss durchgeführt hat

Der gedruckte Tagesabschlussbericht (Z) zeigt einen Abschnitt **STORNOS**,
getrennt von Rückerstattungen, an jedem Tag mit mindestens einer stornierten
Buchung: Eine Stornierung bedeutet hier einen abgeschlossenen Verkauf, der
danach storniert/rückgängig gemacht wurde (z. B. eine Korrektur am selben Tag),
während eine Rückerstattung eine förmliche, nachträglich bearbeitete Rückgabe
ist. Für einen Prüfer bedeuten die beiden Dinge Unterschiedliches, daher werden
sie nie in einer Zahl vermischt — das Stornieren eines Verkaufs trägt von
vornherein keinen Umsatz und ändert nie den Netto-Betrag des Tages. Der
Abschnitt entfällt vollständig an einem Tag ohne Stornierungen.

Der Bericht druckt außerdem immer **Erstellt von** (wer den Abschluss
durchgeführt hat) — den Anzeigenamen der Person, oder „System“ für den
automatischen geplanten Abschluss —, sodass es einen Nachweis gibt, wer den Tag
abgeschlossen hat, auch ohne die Prüfprotokoll-Seite zu öffnen. Ein optionaler
Vermerk kann derzeit an einen Abschluss angehängt werden, wenn die auslösende
Anfrage einen Wert `annotation` mitsendet; dafür gibt es noch kein Feld auf dem
Bildschirm, dies ist also vor allem für eine Integration oder ein künftiges
Kassen-Update nützlich — er wird als **Anmerkung** unmittelbar unter „Erstellt
von“ gedruckt, wenn vorhanden.

## Aufschlüsselung nach Artikelgruppe, Artikel und Bediener

Das Ausführen von Tagesabschluss für einen einzelnen Tag (nicht einen
Datumsbereich) fügt drei weitere Aufschlüsselungen hinzu, im gedruckten
Bericht und auf dem Bildschirm in der Liste der archivierten Berichte:
**BY ARTICLE GROUP** (Umsatz nach der eigenen Kategorie jedes Artikels — eine
Unterkategorie wie „Handys“ erscheint hier in einer eigenen Zeile, statt wie
bei der Abteilungsaufschlüsselung in ihre übergeordnete Abteilung
zusammengefasst zu werden), **BY ARTICLE** (jeder an diesem Tag verkaufte
Artikel, nicht nur die Best-/Schlechtverkäufer — auf dem Bildschirm eingeklappt
angezeigt, da ein geschäftiger Tag Dutzende auflisten kann) und **BY OPERATOR**
(Umsatz und Verkaufsanzahl je Kassierer). Der Umsatz bleibt immer dem
Kassierer zugeordnet, der den Verkauf tatsächlich gebucht hat, auch wenn ein
Manager eine Ausnahme dafür genehmigt hat, sodass die Bediener-Aufschlüsselung
immer widerspiegelt, wer wirklich an der Kasse stand.

Die Artikelliste auf dem Bildschirm zeigt immer jeden Artikel, egal wie viele
es sind. Der **gedruckte** Abschnitt BY ARTICLE ist separat konfigurierbar (im
Reiter Tagesabschluss, neben dem Zeitplan für den automatischen Abschluss), da
ein Geschäft mit vielen Artikeln sonst bei jedem Abschluss Hunderte
zusätzlicher Zeilen drucken könnte: **Top N nach Umsatz drucken** (Standard —
druckt die Top 30, oder so viele wie Sie festlegen, gefolgt von einer
Zeile „+N weitere“, sodass ein typisches Geschäft davon unberührt bleibt,
während ein geschäftiges begrenzt wird), **Jeden Artikel drucken** (das
bisherige Verhalten, unbegrenzt) oder **Nicht drucken** (lässt den Abschnitt
nur im gedruckten Bericht weg).

## Aufschlüsselung nach Vor-Ort/Außer-Haus

Das Ausführen von Tagesabschluss für einen einzelnen Tag fügt außerdem eine
Aufschlüsselung **BY ORDER TYPE** hinzu, im gedruckten Bericht und auf dem
Bildschirm in der Liste der archivierten Berichte: Umsatz und Menge aufgeteilt
zwischen **Vor Ort** und **Außer Haus**, passend zu den Beschriftungen des
eigenen Vor-Ort-/Außer-Haus-Schalters auf dem Verkaufsbildschirm. Ein Verkauf,
der beides mischte (manche Artikel vor Ort, manche außer Haus), teilt sich
korrekt nach seinen eigenen Artikeln auf die beiden Zeilen auf, statt eine
dritte „gemischt“-Zeile zu benötigen. Wie bei den Aufschlüsselungen nach
Artikelgruppe/Artikel/Bediener oben erscheint eine Zeile nur, wenn dieser
Modus mindestens einen Verkauf hatte — ein Tag, an dem ausschließlich vor Ort
verzehrt wurde, zeigt nur die Zeile „Vor Ort“, keine leere Zeile „Außer Haus“.

## Zahlungsart und Mehrwertsteuersatz zusammen im Tagesabschlussbericht (Z)

Der gedruckte Tagesabschlussbericht enthält eine Tabelle **BY METHOD & VAT
RATE**: die Tageseinnahmen aufgeschlüsselt nach Zahlungsart und Mehrwertsteuersatz
gleichzeitig — eine Zeile pro Kombination (z. B. Bargeld bei 7 %, Karte bei
19 %), jeweils mit Netto-, Steuer- und Bruttobetrag. Das ist die Tabelle, die
ein Steuerberater in die Buchhaltungssoftware einträgt: über welche Zahlungsart
das Geld einging, gegen welchen Mehrwertsteuersatz. Die Zeilen addieren sich
immer exakt zu den Tages-Gesamtsummen je Mehrwertsteuersatz. Wurde ein Verkauf
mit mehr als einer Methode bezahlt, werden seine Beträge anteilig auf diese
Methoden aufgeteilt, entsprechend dem, was jede Methode gezahlt hat. Trinkgelder
sind hier nicht enthalten — ein Trinkgeld trägt keine Mehrwertsteuer —, daher
kann eine Kartenzeilengruppe insgesamt weniger ergeben als die
Karteneinnahmenzeile, um genau die Kartentrinkgelder des Tages, und noch
weniger an einem Tag mit einer Kartenrückerstattung (eine Rückerstattung
verringert auch die Karteneinnahmenzeile, sodass die beiden im Gleichschritt
bleiben).

## Berichtsaufbewahrung

Jeder archivierte Tagesabschlussbericht wird **10 Jahre** lang aufbewahrt —
dies ist ein gesetzlicher Nachweis, und die Reset-Schaltfläche „Transaktionshistorie
löschen“ in der Datenverwaltung berührt ihn nie. (Diese Schaltfläche zerstört
inzwischen überhaupt nichts mehr: Sie verschiebt Ihre Transaktionshistorie in
ein Reset-Archiv, und ein archivierter Stapel kann unter Einstellungen → Daten
wiederhergestellt werden, solange die Kasse seit dem Reset nicht gehandelt
hat.) Sobald ein Bericht seinen 10-jährigen Jahrestag überschreitet, wird er
automatisch und dauerhaft im Hintergrund gelöscht — es gibt keinen manuellen
Schritt und keine Bestätigungsabfrage, exportieren Sie also alles, was Sie
länger aufbewahren möchten, vorher.

Ein Reset-Archivstapel kann außerdem **endgültig gelöscht** werden, aus
derselben Liste unter Einstellungen → Daten, jedoch erst, sobald er alt genug
ist: Ein Stapel mit echten Verkäufen ist geschützt, bis das Aufbewahrungsfenster
Ihres Landes (auf der Seite Ländereinstellungen festgelegt) seit seiner
Archivierung verstrichen ist — ein früheres Löschen wird abgelehnt, und die
Meldung nennt Ihnen das Datum, ab dem er löschbar wird. Ein Stapel ganz ohne
Verkäufe (es wurde noch nichts verkauft, als er zurückgesetzt wurde) lässt
sich sofort löschen, auch wenn er noch andere Testdaten wie Kassenschichten
enthält — der Schutz betrifft ausdrücklich Verkaufsdatensätze. Dies ist
getrennt von der obigen 10-jährigen Berichtsaufbewahrung und ändert sie nicht.

Wählen Sie unter Einstellungen → Berichtsaufbewahrung, wo Berichte
aufbewahrt werden:

- **Nur diese Kasse** — funktioniert schon heute, keine zusätzliche
  Einrichtung nötig. Berichtsarchive sind klein (wenige KB pro
  abgeschlossenem Tag), sodass 10 Jahre davon die Festplatte einer modernen
  Kasse nicht füllen.
- **Nur Cloud** / **Kasse + Cloud** — angezeigt für eine künftige Version,
  sobald Cloud-Speicher und ein Geschäftsabonnement verfügbar sind; noch
  nicht auswählbar.

Dieselbe Seite zeigt **wie weit Ihre Aufzeichnungen zurückreichen** (frühester
bis letzter archivierter Bericht und wie viele) und eine **Export**-Schaltfläche
— wählen Sie einen Datumsbereich und laden Sie die passenden Berichte als CSV
oder JSON herunter, um sie z. B. einem Prüfer zu übergeben.

## Bargeldanpassungen & Auszahlungen (Schichten)

Das Formular „Bargeldanpassung/Auszahlung“ auf der Seite Schichten erfasst
alles, was den erwarteten Bargeldbestand der Kasse außerhalb eines Verkaufs
ändert — eine Wechselgeld-Auffüllung, eine Zählkorrektur oder eine Auszahlung
aus der Schublade. Jede Anpassung, die Bargeld **entfernt**, benötigt eine
Manager-PIN, unabhängig vom gewählten Typ — dieselbe Freigabe wie bei einer
Rückerstattung oder einer Pfandrückgabe-Auszahlung, da es dasselbe Risiko ist
(Bargeld verlässt die Schublade ungenehmigt). Das Hinzufügen von Bargeld (ein
positiver Betrag, z. B. eine Wechselgeld-Auffüllung) benötigt keine. Geben Sie
den entnommenen Betrag als negative Zahl ein (z. B. „-50“ für eine Auszahlung
von 50 Einheiten) — tippen Sie an einer Touch-Kasse ohne physische Tastatur
zuerst auf die „-“-Taste der Bildschirmtastatur.

Bei einem deutschen Geschäft im Systemunterlage-Modus durchläuft eine
Anpassung, die Bargeld entfernt — und eine Pfandrückgabe-Auszahlung — dieselbe
TSE-Prüfung wie ein Verkauf oder eine Rückerstattung, wird also verweigert,
solange keine TSE eingerichtet ist oder die TSE ausfällt. Siehe „Verkaufen &
Kassieren“ → „Deutsche Geschäfte: TSE und echte Verkäufe“ für die Bedeutung
dieser Meldung und wie ein Inhaber eine vorübergehende Ausnahme gewähren kann.
Das Hinzufügen von Bargeld ist davon nie betroffen.

Eine Pfandrückgabe-Auszahlung wird gegen die offene Schicht des **eigenen
Registers dieser Kasse** gebucht — auch wenn gleichzeitig die Schicht eines
anderen Registers offen ist, landet sie nie auf der anderen Schublade. In
einem Geschäft mit mehr als einem Register muss die Kasse zunächst wissen,
welches Register sie ist: Legen Sie „Register dieser Kasse“ unter Einstellungen
→ Kassen fest, sonst wird die Auszahlung mit einer entsprechenden Meldung
abgelehnt.

Auch beim Eröffnen einer neuen Schicht erscheint eine Registerauswahl. In
einem Geschäft mit mehr als einem Register ist inzwischen standardmäßig das
eigene Register dieser Kasse voreingestellt (unter Einstellungen → Kassen
festgelegt), sodass das Eröffnen einer Schicht an der Kasse, an der Sie
stehen, normalerweise keine Auswahl mehr erfordert — Sie können für den
seltenen Fall, dass eine Schicht auf einem anderen Register eröffnet werden
muss, weiterhin ein anderes Register aus der Liste wählen.

## Die Schublade beim Abschluss zählen: Abschöpfen & neue Wechselgeldkasse

Das Startgeld für eine neue Schicht wird **automatisch übernommen** vom
letzten Abschluss des Registers — was der vorherige Abschluss in der
Schublade hinterlassen hat, ist bereits vorausgefüllt, sodass Sie es
bestätigen, statt es erneut einzutippen. Sie können die Zahl weiterhin
bearbeiten, falls die Schublade zwischenzeitlich korrigiert wurde; was Sie
absenden, ist das, was aufgezeichnet wird.

Beim Schließen einer Schicht zählen Sie die Schublade und geben das gezählte
Bargeld wie bisher ein. Zwei optionale Zusätze kommen hinzu:

- **Abschöpfen in den Safe** — der Betrag, den Sie als Teil des Abschlusses
  von der Schublade in den Safe verlegen. Das gezählte Bargeld minus das
  Abgeschöpfte wird zur **neuen Wechselgeldkasse** der Schublade, mit der die
  nächste Schicht auf diesem Register eröffnet wird. Ein Abschöpfen kann das
  gezählte Bargeld nie übersteigen, und es ändert nie die erwartete Zahl —
  die Abweichung vergleicht immer Ihre Zählung mit den Einnahmen *vor* dem
  Abschöpfen, sodass das Verlegen von Geld in den Safe keinen Fehlbetrag
  verbergen kann. Ein optionaler Grund kann dazu erfasst werden.
- **Stückelungszählung** — eine optionale Zählung je Stückelung (wie viele
  Münzen und Scheine jeder Art) beim Abschluss gespeichert als Zählprotokoll,
  für Geschäfte, die die Kassenzählung Stück für Stück dokumentiert haben
  möchten. Leer lassen, um sie ganz zu überspringen.

## Bargeldabgleich im Tagesabschlussbericht

Der gedruckte Tagesabschlussbericht (Z) erhält einen Abschnitt **CASH
RECONCILIATION** an jedem Tag, an dem mindestens eine Schicht geschlossen
wurde: Startgeld, Barverkäufe, einbehaltene Trinkgelder (nur an einem Tag mit
tatsächlichem Bar-Trinkgeld gedruckt), Einzahlungen, Auszahlungen, Berechnet
(was in den Schubladen sein sollte), Gezählt (was tatsächlich darin war),
Abweichung, Abschöpfen in den Safe und die auf den nächsten Tag übertragene
neue Wechselgeldkasse. Barverkäufe schließen jedes Bar-Trinkgeld aus, genauso
wie Trinkgelder bereits an anderer Stelle des Berichts aus dem Umsatz
herausgehalten werden — deshalb steht die Zeile „Einbehaltene Trinkgelder“
zwischen Barverkäufen und Einzahlungen: Startgeld + Barverkäufe +
einbehaltene Trinkgelder + Einzahlungen + Auszahlungen ergeben zusammen
Berechnet, sodass die eigenen Zahlen des Abschnitts auch bei genutztem
Bar-Trinkgeld noch aufgehen, nicht nur an einem gewöhnlichen Tag ohne
Trinkgeld. Abschöpfen in den Safe wird als Teil des Schichtabschlusses
eingegeben, nachdem Berechnet für den Tag bereits feststeht — deshalb steht
es unter der Abweichung, statt in diese Summe eingerechnet zu werden. Eine
Abweichung ungleich null wird auf dem Ausdruck mit `!!` markiert, und der
Reiter Tagesabschluss kennzeichnet die Zeile dieses Tages mit einer
Warnmarkierung, sodass eine Unstimmigkeit auf dem Bildschirm sichtbar ist,
ohne jeden Zeitraum erneut zu drucken. Ein Tag ohne geschlossene Schicht
erzeugt weiterhin einen vollständigen Bericht — der Abschnitt fehlt einfach,
und das Ausführen von Tagesabschluss wird nie durch eine offene Schicht
blockiert.

Der Reiter „Zahlungen & Kanäle“ in den Berichten zeigt eine Aufschlüsselung
**Bargeldanpassungen nach Grund** für den ausgewählten Zeitraum — z. B. eine
Summe für „Pfandrückgabe“ über alle Auszahlungen in diesem Fenster —, sodass
Sie eine Zahl wie „insgesamt ausgezahlte Pfandbeträge diese Woche“ sehen
können, ohne die Prüfprotokoll-Seite zu öffnen. Sie erscheint erst, sobald es
mindestens eine Anpassung im Zeitraum gibt.

## Verkäufe aller Kassen sehen (Kassenjournal)

Die Kassenjournal-Seite (die Beleg-/Sync-Liste außerhalb des Verkaufsbildschirms)
zeigt standardmäßig die Verkäufe aller Kassen, neueste zuerst, mit der Kasse,
die jede Bestellung aufgenommen hat, in ihrer eigenen Spalte — sodass eine
Maschine die gesamten Einnahmen des Geschäfts überblicken kann, ohne zu jedem
Register zu gehen. Wechseln Sie zu „Diese Kasse“, um nur die eigenen Verkäufe
dieser Maschine zu sehen.

Verwenden Sie die Filterzeile über der Liste:

- **Kasse** — „Alle Kassen“ (Standard) zeigt die Verkäufe jeder Kasse,
  neueste zuerst; „Diese Kasse“ schränkt die Liste auf nur die eigenen
  lokalen Verkäufe dieser Maschine ein; oder wählen Sie eine bestimmte Kasse
  nach Namen, um nur deren Verkäufe zu sehen.
- **Tag** — wählen Sie ein Datum, um die Liste auf diesen Kalendertag
  einzuschränken; leer lassen, um die neuesten Verkäufe unabhängig vom Tag zu
  sehen.

Ist eine andere Kasse als „Diese Kasse“ registriert, zeigt eine Zeile unter
den Filtern den Namen jeder registrierten Kasse und wann sie zuletzt mit
dieser Maschine in Kontakt stand („letzter Kontakt von *Kasse*: *Zeit*“) —
eine Kasse, die zuverlässig anpingt, kann darunter dennoch eine fehlgeschlagene
Verkaufssynchronisierung haben, dies ist also ein Netzwerkkontakt-Signal, kein
Beweis dafür, dass ihre Verkäufe tatsächlich angekommen sind; nützlich, um eine
zurückgefallene Kasse zu erkennen, kein Ersatz für den Abgleich der Summen.
Eine Kasse zeigt hier „—“, wenn sie nie in Kontakt stand, oder (bei einer
Replika) weil ihre Kontaktzeit nicht von der Primärkasse heruntergegeben wird.

Nur die Primärkasse eines Geschäfts sammelt die Verkäufe anderer Kassen — eine
Replika-Kasse hat für das Kassenjournal immer nur ihre eigenen lokalen Verkäufe,
unabhängig vom gewählten Kassenfilter, da eine Replika ihre eigenen Verkäufe
immer nur einseitig nach oben zur Primärkasse überträgt und niemals die
Verkäufe der Geschwister zurückerhält. Die Wahl von „Alle Kassen“ oder einer
bestimmten anderen Kasse auf einer Replika zeigt eine erklärende Meldung, dass
kassenübergreifende Verkäufe nur auf der Primärkasse des Geschäfts verfügbar
sind, statt einer Tabelle, die still leer ist, ohne zu erklären, warum.

## Trinkgeld- und Servicegebühr-Auszahlungen an Mitarbeiter (Reiter Trinkgeld)

Der Reiter **Trinkgeld** erfasst, wie Trinkgelder und Servicegebühren an
Mitarbeiter ausgezahlt werden, und berichtet darüber — ein Teil der
Aufzeichnungspflicht, die der britische Employment (Allocation of Tips) Act
2023 von Arbeitgebern verlangt. Er erfasst, was ein Manager ihm mitteilt: Die
Software erkennt oder bewegt kein Geld von sich aus.

- **Erhalten vs. zugewiesen** — zwei Summen für den gewählten Zeitraum: was
  einging (Trinkgelder auf abgeschlossenen Verkäufen, oder Servicegebühr,
  sobald sie eingenommen wird) und was als an einen Mitarbeiter ausgezahlt
  erfasst wurde. Sie laufen auf unterschiedlichen Uhren — heute eingenommenes
  Geld wird möglicherweise erst in einer späteren Schicht ausgezahlt —, daher
  ist es normal, keine Fehlermeldung, dass die beiden Zahlen in einem kurzen
  Fenster nicht übereinstimmen; prüfen Sie über ein Fenster, das breit genug
  ist, um beides abzudecken.
- **Eine Auszahlung erfassen** — ein Manager (Berechtigung Mitarbeiterauszahlungen)
  wählt den Mitarbeiter, das Datum, an dem das Geld tatsächlich gezahlt
  wurde, Trinkgeld oder Servicegebühr, den Betrag und eine optionale Notiz,
  und sendet ab. Das Datum darf nicht in der Zukunft liegen — dies erfasst
  eine bereits erfolgte Zahlung.
- **Die eigenen Aufzeichnungen eines Mitarbeiters** — verwenden Sie den
  Mitarbeiterfilter, um sowohl die Summen als auch die Auszahlungsliste auf
  eine Person einzugrenzen, z. B. um einem Mitarbeiter (oder ihm auf Anfrage
  zu zeigen), was ihm als gezahlt erfasst wurde.
- **Export** — laden Sie die Auszahlungsdatensätze für einen Zeitraum
  (optional einen Mitarbeiter) als CSV-Datei herunter, um sie einem
  Mitarbeiter, einem Steuerberater oder jedem anderen zu übergeben, der die
  zugrunde liegenden Datensätze statt nur der Summen benötigt.
- Auszahlungsdatensätze werden zusammen mit den übrigen Finanzunterlagen des
  Geschäfts aufbewahrt und nicht vorzeitig gelöscht — dieselbe Aufbewahrung
  wie alles andere auf dieser Seite (siehe Berichtsaufbewahrung oben).
- **Auf dem gedruckten Tagesabschlussbericht (Z)** — der Bericht druckt
  außerdem eine kurze Trinkgeld-nach-Zahlungsart-Zeile (z. B. „4x Karte
  3,20 £“) für jeden Tag mit mindestens einer Zahlung, bei der ein Trinkgeld
  erfasst wurde — meistens eine Kartenzahlung, bei der die eigene
  Trinkgeldabfrage des Terminals verwendet wurde. Dies wird getrennt von den
  Tagesumsätzen gehalten, nicht als Umsatz gezählt. Dieselbe Summe erscheint
  auch in einer Spalte **Trinkgeld** auf der Bildschirmliste des Reiters
  Tagesabschluss, neben Umsatz und Netto, sodass ein Manager sie sehen kann,
  ohne einen Bericht zu drucken oder herunterzuladen. Sie kann sich von
  „Erhalten“ oben unterscheiden: Die Z-Bericht-Zeile zählt jede Zahlung mit
  Trinkgeld, unabhängig davon, wem das Trinkgeld gehört, während „Erhalten“
  nur für den Mitarbeiter erfasste Trinkgelder zählt (der Standardfall) —
  die beiden weichen erwartungsgemäß voneinander ab, sobald ein Trinkgeld
  stattdessen für das Geschäft erfasst wird.

## Eine Kartenzahlung abgleichen (Belegdetail)

Das Öffnen eines Belegs aus dem Kassenjournal zeigt sein vollständiges
Zahlungsdetail — hier wird eine Kartenzahlung im Nachhinein abgeglichen,
Tage nach dem Verkauf. Wurde eine Zahlung an einem Präsenz-Kartenterminal
entgegengenommen, zeigt ihre Zahlungszeile die maskierte Kartennummer und den
Genehmigungscode (dieselbe Abgleichzeile, die der gedruckte Beleg bereits im
Moment der Zahlung zeigte), sowie Terminal- und Trace-ID zum Abgleich mit dem
eigenen Abrechnungsbericht des Terminals. Diese Felder erscheinen nur, sobald
eine Zahlungsart sie tatsächlich erfasst — die heutigen eingebauten
Zahlungsarten (Bargeld, Stripe, SumUp, QR-Zahlung) tun dies nicht, bestehende
Belege sind davon also nicht betroffen.
