---
id: fiscal-device
title: Fiskalgerät (Türkei)
section: Ihr Geschäft einrichten
order: 113
summary: "Sehen Sie, über welche Registrierkasse (YN ÖKC) die Kasse zahlt, ob sie ihre Druckfähigkeit bewiesen hat, und den zuletzt ausgestellten Beleg — das Gerät druckt den gesetzlichen Beleg, die Kasse führt dessen Nummer bei jedem Verkauf mit."
routes: [/fiscal-device]
keywords: [fiskal, türkei, türkiye, ÖKC, yazarkasa, registrierkasse, GİB, beleg, gerät, Z-Bericht]
---

# Fiskalgerät (Türkei)

In der Türkei wird ein Einzelhandelsverkauf durch eine zertifizierte Registrierkasse dokumentiert — ein *Yeni Nesil Ödeme Kaydedici Cihaz* (YN ÖKC), die „Yazarkasa-POS“ auf der Theke. Das Gerät nimmt das Geld entgegen, druckt den gesetzlichen Beleg (*mali fiş*) und meldet seine Tagessummen über seinen Hersteller an die Finanzbehörde. Universal Till ersetzt es nicht: Mit installiertem Türkei-Fiskalgerät-Plugin wird das Gerät zu einer Zahlungsmethode namens **Yazarkasa (ÖKC)**. Bei der Zahlung übergibt die Kasse den Warenkorb an das Gerät, das Gerät nimmt Bargeld oder Karte entgegen und druckt, und die Kasse zeichnet die Belegnummer, Seriennummer und den Z-Zähler des Geräts beim Verkauf auf und zeigt sie auf ihrer eigenen Belegkopie an.

Diese Seite zeigt diese Anordnung aus Sicht der Kasse. Sie kommuniziert nie direkt mit dem Gerät und prüft nichts bei der Finanzbehörde; sie liest lediglich, was die Kasse aus den eigenen Antworten des Geräts aufgezeichnet hat.

## Verwendung

1. Installieren und aktivieren Sie das Plugin **Türkei-Fiskalgerät (YN ÖKC)** unter **Plugins** und erteilen Sie ihm die angefragte Netzwerkberechtigung. **Fiskalgerät** erscheint dann im Menü (nur Manager, sobald das Land Ihres Geschäfts auf Türkei eingestellt ist).
2. Folgen Sie unter **Plugin** dem Link **Plugin-Einstellungen öffnen** und geben Sie ein, wo sich das Gerät im Netzwerk Ihres Geschäfts befindet — Treiber, Adresse und Port. Solange der Treiber eines Herstellers noch nicht fertig ist, spricht der *Bridge*-Treiber mit einem Bridge-Programm oder mit dem für Tests verwendeten Simulator; das Plugin verweigert jede Zahlung, statt vorzugeben, dass es funktioniert, solange sein Treiber nicht bereit ist.
3. Nehmen Sie einen Verkauf mit **Yazarkasa (ÖKC)** als Zahlungsmethode vor. Das Gerät druckt; die Kasse zeichnet den Beleg auf. Der erste Beleg markiert das Gerät automatisch auf dieser Seite als **bestätigt**.
4. Haben Sie bereits beobachtet, wie das Gerät einen Testbeleg druckt, und möchten es vor dem ersten echten Verkauf als bestätigt markieren, drücken Sie **Gerät bestätigen**. Drücken Sie **Gerät entkoppeln**, wenn das Gerät entfernt oder ersetzt wird — Verkäufe als Systemunterlage werden dann wieder verweigert, bis sich ein Gerät erneut bewährt hat.

## Gut zu wissen

- **Bestätigt** ist das, was die Sicherung der Kasse für die Türkei ausliest: Solange das Geschäft als Systemunterlage eingestellt ist und kein Gerät bestätigt ist, verweigert die Kasse einen Verkaufsabschluss, statt einen ohne Beleg zuzulassen. Im Schattenmodus (das vorhandene Gerät des Geschäfts bleibt der gesetzliche Nachweis) wird nichts verweigert.
- Das Gerät druckt einen Fiskalbeleg pro Verkauf, daher muss die ÖKC-Zahlung den gesamten Verkauf abdecken; eine Aufteilung zwischen dem Gerät und einer anderen Methode wird verweigert.
- Lehnt das Gerät ab, läuft ein Zeitlimit ab oder ist es nicht erreichbar, wird die Zahlung verweigert und der Warenkorb bleibt erhalten — beheben Sie das Gerät oder das Netzwerk und versuchen Sie es erneut. Bei einer verweigerten Zahlung wird auf keiner Seite etwas aufgezeichnet.
- Meldet das Gerät Erfolg, liefert aber keine Belegnummer, verweigert die Kasse den Verkauf ebenfalls, statt einen ohne Druckbeweis aufzuzeichnen — die Kasse vertraut niemals einer „genehmigt“-Antwort für sich allein. Prüfen Sie das Gerät, bevor Sie die Zahlung erneut vornehmen: Es könnte das Geld bereits genommen haben, obwohl die Kasse keinen Nachweis davon hat, und ein erneuter Versuch mit einer anderen Methode riskiert eine doppelte Belastung des Kunden. Dieselbe Regel gilt für eine **Rückerstattung**: Eine ÖKC-Rückerstattung, die das Gerät als erfolgreich meldet, aber ohne Beleg, wird aus demselben Grund ebenfalls verweigert.
- Dieselbe Verweigerung gilt, wenn ein Verkauf **überhaupt keine** Fiskalgerät-Zahlung verwendet — Bargeld, Karte oder eine andere Methode allein. Das einmalige Bestätigen des Geräts deckt nicht jeden folgenden Verkauf ab; jeder Verkauf benötigt weiterhin seine eigene **Yazarkasa (ÖKC)**-Zahlung.
- **Belege heute** zählt Gerätebelege seit dem Beginn Ihres Geschäftstages, derselben Grenze, die auch die Berichte verwenden.
- Diese Seite zeichnet Daten auf und zeigt den Status an. Ob das Gerät, die Steuerpflichtigen-Klasse und die Unterlagen Ihres Geschäfts Ihren Pflichten genügen, ist eine Angelegenheit zwischen Ihnen und Ihrem Steuerberater (*mali müşavir*); die Seite bestätigt dies nicht.
