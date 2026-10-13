---
id: customer-order-tracking
title: Müşteri sipariş takibi (QR)
section: Günlük satış
order: 46
summary: "Self servis kioskta sipariş veren müşteri, bir QR kodu okutarak siparişinin durumunu kendi telefonundan izler. Ücretli planlara dahildir."
routes: [/o/{token}, /o/{token}/status]
---

# Müşteri sipariş takibi (QR)

Self servis kioskta sipariş veren müşteri, ister orada ödemiş ister kasada ödeyecek olsun, bir QR kodu okutarak siparişinin durumunu kendi telefonundan izler. Self servis ekranının kendisi gibi, müşterinin gördüğü sayfa da müşteriye yöneliktir; bu yüzden bu kılavuza dönen bir "?" bağlantısı taşımaz — bu konuyu kılavuzun aramasından veya konu listesinden bulun.

## Nasıl çalışır

1. Müşteri siparişini self servis kioskta tamamladığında, onay ekranı sipariş numarasının yanında bir QR kodu gösterir (onay ekranı yaklaşık 20 saniye açık kalır; okutmak için yeterli).
2. Kod okutulunca müşterinin telefonunda sipariş numarasını ve güncel durumunu gösteren küçük bir sayfa açılır — personelinizin **Siparişler** ekranında işaretlediği durumların aynısı: hazırlanıyor, hazır, teslim edildi.
3. Sayfa birkaç saniyede bir kendini günceller; personel "Hazır"a dokunduğu anda müşteri bunu görür — sayfa yenilemeden, tezgâha sormadan.
4. Bağlantı, kioskun kullanıldığı dilde açılır.
5. Müşteri kasada ödemeyi seçtiyse, personeliniz siparişin ödemesini alana kadar sayfa **Kasada ödeme bekleniyor** gösterir. Ondan sonra siparişin durumunu diğer siparişlerdeki gibi gösterir. Yaklaşık 2 saatten uzun süre ödenmeyen bir sipariş, ödenene kadar sayfada görünmez.

## Müşteri neyi görür, neyi görmez

- Sayfa **yalnızca sipariş numarasını ve durumunu** gösterir — isim, ürün, fiyat veya ödeme bilgisi yoktur. Bağlantı, tahmin edilemeyen uzun ve rastgele bir koddur; her kod yalnızca tek bir siparişe aittir.
- Sipariş teslim edildikten (veya iptal edildikten) sonra bağlantı yaklaşık 2 saat daha çalışır, sonra yanıt vermez — eski bir fişin QR kodu sonsuza dek açık kalmaz.

## Notlar

- Sipariş takibi ücretli planlara dahildir. Ücretsiz Yerel planda ya da ücretli özellikler duraklatılmışken (abonelik sona erdi veya kasa aboneliği 7 günden uzun süredir bulutla doğrulayamadı) onay ekranı QR olmadan gösterilir; siparişin kendisi bundan asla etkilenmez.
- Müşterinin telefonu cihazla aynı ağda olmalıdır — mağazanın Wi-Fi ağı. Cihazın kendisinden başka bir ağ adresi yoksa onay ekranı QR olmadan gösterilir; siparişin kendisi bundan asla etkilenmez.
- Mağazanızda birden fazla kasa varsa ve sipariş kiosktan farklı bir kasada ödenirse, kioskun bağlantısı ödemeyi göremez: yaklaşık 2 saate kadar **Kasada ödeme bekleniyor** göstermeye devam eder, sonra yanıt vermez. Sipariş takibi bulutumuza taşınana kadar bunu önlemek için kiosk siparişlerinin ödemesini kioskun kendi kasasında alın.
- QR kodu yalnızca self servis kioskta verilen siparişlerde görünür. Personelinizin kasada girdiği satışlar henüz takip QR kodu yazdırmaz.
