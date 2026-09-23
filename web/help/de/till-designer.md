---
id: till-designer
title: Schnellwahltasten
section: Ihr Geschäft einrichten
order: 115
summary: Ordnen Sie die Schnellwahltasten, das Produktraster und die Kategorien des Verkaufsbildschirms an — auf einer Live-Kopie des Verkaufsbildschirms selbst.
routes: [/designer]
keywords: [schnellwahltasten, designer, schaltflächen, layout, produktraster, schnellverkauf, verkaufsbildschirm, kategorien]
---

# Schnellwahltasten

Ordnen Sie die Schnellwahltasten, das Produktraster und die Kategorien an, die auf dem Verkaufsbildschirm erscheinen. Die Seite zeigt das Produktfeld des Verkaufsbildschirms selbst — dieselbe Kategorieleiste und dieselben Kacheln, die Kassierer sehen, live — und jede Änderung darauf wird sofort gespeichert und erscheint sofort auf dem Verkaufsbildschirm. Jeder Artikel, der bereits in Ihrem [Katalog](/help/catalog) ist, kann eine Schnellwahltaste werden; Schnellwahltasten legt keine Artikel an, sondern bestimmt nur, welche eine Kachel bekommen, in welcher Reihenfolge sie erscheinen und aus welchen Kategorien die Leiste besteht. Nur für Manager und Admins: Ein Kassierer sieht Schnellwahltasten im Menü gar nicht, und die Seite selbst weist einen Kassierer ab, der ihre Adresse eintippt.

## Verwendung

1. **Schaltfläche hinzufügen:** Tippen Sie in das Suchfeld oben — mindestens 3 Zeichen (Name, SKU oder Barcode); bei weniger erscheint statt einer Suche der Hinweis „Mindestens 3 Zeichen eingeben“. Passende Artikel erscheinen in einer Liste unter dem Feld; tippen Sie einen an, und seine Kachel erscheint sofort in der Kopie des Verkaufsbildschirms darunter, ohne separaten Speicherschritt. Passt nichts, zeigt die Liste „Keine Treffer.“ — versuchen Sie eine kürzere oder andere Suche oder prüfen Sie zuerst, ob der Artikel im Katalog existiert und aktiv ist.
2. **Kacheln neu anordnen:** Halten Sie eine Kachel in der Kopie des Verkaufsbildschirms gedrückt (oder klicken Sie sie mit der rechten Maustaste an), und das Raster wechselt in denselben Bearbeitungsmodus wie auf dem Verkaufsbildschirm: Die Kacheln wackeln, und Sie ziehen eine Kachel innerhalb ihrer Kategorie dorthin, wo Sie sie haben möchten, oder verschieben eine fokussierte Kachel mit den Pfeiltasten Links/Rechts um jeweils einen Platz. Tippen Sie auf **Fertig**, drücken Sie Escape oder tippen Sie irgendwo außerhalb des Rasters, um die neue Reihenfolge zu speichern. Eine Kachel lässt sich nur innerhalb ihrer eigenen Kategorie verschieben — einen Artikel in eine andere Kategorie zu legen, geschieht im Katalog.
3. **Schaltfläche entfernen:** Tippen Sie, während die Kacheln wackeln, auf das Papierkorb-Symbol an der Ecke einer Kachel und bestätigen Sie. Das nimmt sie nur vom Verkaufsbildschirm — der Artikel selbst wird nie aus Ihrem Katalog gelöscht, Sie können ihn also jederzeit wieder suchen und hinzufügen. Das Stift-Symbol an der anderen Ecke öffnet den Artikel im Katalog und bringt Sie danach hierher zurück.
4. **Kategorien verwalten:** Die Liste **Kategorien** unter der Kopie des Verkaufsbildschirms zeigt jede Kategorie — auch solche ohne Schnellwahltasten und deaktivierte, die die Leiste selbst nie zeigt. Tippen Sie auf **+ Neue Kategorie**, um eine anzulegen (Name und optional eine Farbe aus der Palette), auf den Stift einer Zeile, um sie umzubenennen oder umzufärben, und auf die Pfeile nach oben/unten, um die Reihenfolge zu ändern, in der die Leiste die Kategorien zeigt. Jede Zeile nennt, wie viele Schnellwahltasten und wie viele Katalogartikel die Kategorie hat.
5. **Kategorie deaktivieren:** Tippen Sie auf den Papierkorb in ihrer Zeile. Das geht nur, solange kein aktiver Katalogartikel sie verwendet — eine Zeile, die noch Artikel hat, sagt das vorab mit der Anzahl, und die Kasse lehnt das Antippen mit derselben Meldung ab, statt stillschweigend nichts zu tun; verschieben oder deaktivieren Sie diese Artikel zuerst im Katalog. Eine deaktivierte Kategorie verschwindet aus der Leiste, bis Sie in ihrer Zeile auf **Aktivieren** tippen; ihre Artikel behalten die Kategorie durchgehend.

## Gut zu wissen

- Schnellwahltasten und Kategorien gelten geschäftsweit und werden immer von der **Hauptkasse** aus verwaltet: An einer beigetretenen Kasse zeigt das Hinzufügen, Entfernen oder Neuanordnen einer Schaltfläche oder das Ändern einer Kategorie eine Meldung, die Sie zur Hauptkasse zurückverweist, statt eine nur lokal gültige Änderung anzunehmen.
- Die Kopie des Verkaufsbildschirms zeigt eine Kategorie erst, wenn einer ihrer Artikel eine Schnellwahltaste hat — genau wie der Verkaufsbildschirm —, eine ganz neue Kategorie erscheint also erst in der Leiste, wenn Sie eine Schaltfläche für einen ihrer Artikel hinzufügen. Die Kategorienliste darunter zeigt sie immer.
- Kacheln auf dieser Seite verkaufen nie etwas: Sie hier anzutippen legt nichts in einen Warenkorb. Alles andere an ihnen — Farbe, Preis, Bild, Reihenfolge — ist das, was Kassierer sehen.
- Noch keine Schaltflächen eingerichtet? Das Raster zeigt „Noch keine Produkte.“ — suchen Sie und fügen Sie Ihre erste hinzu.
- Lehnt die Kasse eine Neuanordnung ab (zum Beispiel wegen eines Serverfehlers), lädt das Raster die tatsächlich gespeicherte Reihenfolge des Verkaufsbildschirms neu, statt eine Kachel an einer Position zu lassen, die nie übernommen wurde. Bricht stattdessen die Verbindung ganz ab, sagt eine Meldung das, und das Raster lädt genauso neu — wiederholen Sie die Verschiebung, sobald Sie wieder im Ladennetz sind.
- Dies ist nicht dieselbe Seite wie der **Belegdesigner** (Einstellungen → Belegdrucker → Belegdesigner), der anpasst, was auf Belegen gedruckt wird — siehe [Belegdesigner](/help/designer).
- Kassierer können Kacheln auf dem Verkaufsbildschirm selbst auf dieselbe Weise neu anordnen, ohne diese Seite zu öffnen (siehe **Verkaufen & Kassieren → Schnellwahltasten neu anordnen**) — Ziehen zum Neuanordnen oder Antippen des Papierkorbs fragt vor dem Speichern an Ort und Stelle nach der PIN eines Managers, dieselbe [Manager-Freigabe](/help/elevation) wie anderswo. Kategorien lassen sich nur hier (oder unter Artikel → Kategorien) verwalten.
