---
id: country-settings
title: Ländereinstellungen
section: Einrichtung
order: 145
summary: Die Standardwährung, der Steuersatz und die Archivaufbewahrung für jedes Land — und wie Sie diese für Ihr eigenes Geschäft anpassen.
keywords: [land, währung, steuer, mwst, aufbewahrung, archiv, region, standardwerte]
routes: [/country-settings]
---

# Ländereinstellungen

Jedes Land, das die Kasse kennt, bringt sinnvolle Standardwerte mit: welche Währung es verwendet, den üblichen Steuersatz, ob diese Steuer bereits im Preis enthalten ist, und eine Mindestanzahl an Tagen, die archivierte Transaktionsstapel aufbewahrt werden sollen. Jedes Land hat außerdem seine eigene Standardsprache (Deutschland etwa verwendet standardmäßig Deutsch) — dieser Teil ist auf dieser Seite noch nicht bearbeitbar, nur Währung, Steuer und Aufbewahrung sind es.

Hier liegen diese länderspezifischen Standardwerte. **Der Erstinbetriebnahme-Assistent liest sie jetzt aus**, sodass eine Änderung hier beim nächsten Einrichten einer Kasse von Grund auf im Länderschritt ankommt. Sie ändert kein Geschäft, das *bereits* eingerichtet wurde — dessen Währung/Steuer bleibt, was zum jeweiligen Zeitpunkt gewählt wurde. Die Archivaufbewahrung ist anders: Der hier angezeigte Wert ist der Maßstab, an dem ein endgültiges Löschen eines zurückgesetzten Archivstapels (Einstellungen → Daten) für jedes Geschäft gemessen wird, sobald er gespeichert wird — siehe den Abschnitt „Berichtsaufbewahrung“ im Hilfethema „Berichte“ für die genauen Auswirkungen.

## Verwendung

1. Öffnen Sie **Ländereinstellungen** über das Menü (nur Manager). Standardmäßig sehen Sie nur das Land Ihres eigenen Geschäfts mit Währung, Steuersatz und Mindestarchivaufbewahrung.
2. Bearbeiten Sie die Werte in der Zeile und drücken Sie **Speichern**.
3. Um jedes Land zu sehen, das die Kasse kennt — nützlich, wenn Sie Werte für eine Kasse einrichten, die in einem anderen Land laufen wird —, folgen Sie **Alle Länder anzeigen**. **Nur mein Land anzeigen** bringt Sie zurück zu nur Ihrem eigenen.
4. Um einen Ort hinzuzufügen, der nicht aufgeführt ist, füllen Sie **Land hinzufügen** mit einem eigenen kurzen Code (nur Buchstaben und Zahlen, bis zu 8 Zeichen), seiner Währung und seinem Steuersatz aus — dieses Formular ist in beiden Ansichten verfügbar.
5. **Standardwerte wiederherstellen** setzt ein eingebautes Land auf die Werte zurück, mit denen es ausgeliefert wurde. Ein selbst hinzugefügtes Land wird über **Löschen** vollständig entfernt.

## Gut zu wissen

- Die Steuer wird als Prozentsatz eingegeben — geben Sie `19` für 19 % ein. Halbe Prozentsätze wie `8.5` sind zulässig und werden exakt gespeichert — der Einrichtungsassistent füllt eine neue Kasse allerdings mit dem nächsten vollen Prozentsatz vor, sodass `8.5` dort als `9` ankommt (danach in den Einstellungen wie jeder andere Satz bearbeitbar).
- **Steuer im Preis enthalten** bedeutet, dass der Regalpreis die Steuer bereits enthält, was in den meisten europäischen Ländern üblich ist. Lassen Sie es deaktiviert, wenn die Steuer stattdessen an der Kasse aufgeschlagen wird.
- **Archivaufbewahrung** ist hier eine Untergrenze, die Sie erhöhen, aber nicht unter das angezeigte Minimum senken können. Sie bestimmt, wann ein zurückgesetzter Archivstapel (Einstellungen → Daten → Archive zurücksetzen) für die endgültige Löschung infrage kommt: Ein Stapel mit echten Verkäufen kann erst gelöscht werden, wenn seit seiner Archivierung so viele Tage vergangen sind. Eine Erhöhung des Werts schützt bestehende Stapel sofort länger; sie verkürzt nie einen bereits geltenden Schutz. Die Liste „Archive zurücksetzen“ selbst zeigt für jeden geschützten Stapel direkt das Datum an, bis zu dem er aufbewahrt wird, und verbirgt die Schaltfläche „Endgültig löschen“ bis dahin, sodass Sie nie die Bestätigung eingeben, nur um abgelehnt zu werden.
- Das Bearbeiten eines Landes hier ändert kein bereits eingerichtetes Geschäft und schreibt keine bereits getätigten Verkäufe um.
- Die Wahl eines Landes bei der Ersteinrichtung legt auch die Standardsprache der Kasse auf die des jeweiligen Landes fest (z. B. Deutschland → Deutsch) — dafür muss das passende Sprachpaket nicht bereits installiert sein. Ändern Sie das Land Ihres Geschäfts später über den rohen Schlüssel-/Wert-Editor unter Einstellungen → Alle Einstellungen (`store.country`), geschieht dasselbe automatisch **nur, wenn diese Sprache bereits installiert ist** oder genau wie Englisch von links nach rechts gelesen wird (z. B. Französisch, Spanisch); eine Sprache von rechts nach links wie Arabisch bleibt unverändert, bis ihr Paket installiert ist, damit Sie niemals eine Sprache verlieren, die Sie bereits lesen können, zugunsten einer, deren Text und Layout noch nicht bereit sind.
- Sind Sie unsicher, was Ihr eigenes Land zur Aufbewahrung verlangt, fragen Sie Ihren Steuerberater, bevor Sie annehmen, eine hier angezeigte Zahl sei eine Garantie für die Einhaltung von Vorschriften — diese Seite bestätigt nicht die Einhaltung der Aufbewahrungspflichten irgendeines bestimmten Landes.
- Stimmt die Ländereinstellung Ihres eigenen Geschäfts mit nichts auf dieser Seite überein (selten — das kann normalerweise nicht vorkommen), zeigt die Seite jedes Land mit einem erklärenden Hinweis an, statt der üblichen Einzelland-Ansicht.
