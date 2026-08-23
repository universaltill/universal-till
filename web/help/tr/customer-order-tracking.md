---
id: customer-order-tracking
title: Müşteri sipariş takibi (QR)
section: Günlük satış
order: 46
summary: "Self servis kioskta ödeme yapan müşteri, bir QR kodu okutarak siparişinin durumunu kendi telefonundan izler."
routes: [/o/{token}, /o/{token}/status]
---

# Müşteri sipariş takibi (QR)

Self servis kioskta ödeme yapan müşteri, bir QR kodu okutarak siparişinin durumunu kendi telefonundan izler. Self servis ekranının kendisi gibi, müşterinin gördüğü sayfa da müşteriye yöneliktir; bu yüzden bu kılavuza dönen bir "?" bağlantısı taşımaz — bu konuyu kılavuzun aramasından veya konu listesinden bulun.

## Nasıl çalışır

1. Müşteri siparişini self servis kioskta tamamladığında, onay ekranı sipariş numarasının yanında bir QR kodu gösterir (onay ekranı yaklaşık 20 saniye açık kalır; okutmak için yeterli).
2. Kod okutulunca müşterinin telefonunda sipariş numarasını ve güncel durumunu gösteren küçük bir sayfa açılır — personelinizin **Siparişler** ekranında işaretlediği durumların aynısı: hazırlanıyor, hazır, teslim edildi.
3. Sayfa birkaç saniyede bir kendini günceller; personel "Hazır"a dokunduğu anda müşteri bunu görür — sayfa yenilemeden, tezgâha sormadan.
4. Bağlantı, kioskun kullanıldığı dilde açılır.

## Müşteri neyi görür, neyi görmez

- Sayfa **yalnızca sipariş numarasını ve durumunu** gösterir — isim, ürün, fiyat veya ödeme bilgisi yoktur. Bağlantı, tahmin edilemeyen uzun ve rastgele bir koddur; her kod yalnızca tek bir siparişe aittir.
- Sipariş teslim edildikten (veya iptal edildikten) sonra bağlantı yaklaşık 2 saat daha çalışır, sonra yanıt vermez — eski bir fişin QR kodu sonsuza dek açık kalmaz.

## Notlar

- Müşterinin telefonu cihazla aynı ağda olmalıdır — mağazanın Wi-Fi ağı. Cihazın kendisinden başka bir ağ adresi yoksa onay ekranı QR olmadan gösterilir; siparişin kendisi bundan asla etkilenmez.
- QR kodu yalnızca self servis kiosk satışlarında görünür. Kasada girilen siparişler henüz takip QR kodu yazdırmaz.
