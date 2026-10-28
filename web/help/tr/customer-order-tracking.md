---
id: customer-order-tracking
title: Müşteri sipariş takibi (QR)
section: Günlük satış
order: 46
summary: "Müşteri, self servis kioskun onay ekranındaki ya da kasada girilen bir satışın fişindeki QR kodu okutarak siparişinin durumunu kendi telefonundan izler. Shop, Pro ve Multi-location planlarına dahildir."
routes: [/o/{token}, /o/{token}/status]
---

# Müşteri sipariş takibi (QR)

Müşteri bir QR kodu okutarak siparişinin durumunu kendi telefonundan izler: self servis kioskta sipariş verdikten sonra (ister orada ödemiş ister kasada ödeyecek olsun) ya da personelinizin kasada girdiği bir satışın fişinden. Self servis ekranının kendisi gibi, müşterinin gördüğü sayfa da müşteriye yöneliktir; bu yüzden bu kılavuza dönen bir "?" bağlantısı taşımaz — bu konuyu kılavuzun aramasından veya konu listesinden bulun.

**Henüz kullanılamıyor:** Sipariş takibi bulutumuza taşınıyor. Taşınana kadar kasa hiçbir yerde takip QR kodu göstermez — ne kiosk onay ekranında ne fişlerde —, çünkü kasanın kendisine giden bir bağlantı yalnızca mağazanın Wi-Fi ağında çalışır ve her müşteriye kasanın adresini verir. Bu sayfanın geri kalanı, bulut sayfası yayına girdiğinde nasıl çalışacağını anlatır.

## Nasıl çalışır

1. Müşteri siparişini self servis kioskta tamamladığında, onay ekranı sipariş numarasının yanında bir QR kodu gösterir (onay ekranı yaklaşık 20 saniye açık kalır; okutmak için yeterli).
2. Personeliniz kasada bir satış girdiğinde, fişte **Siparişinizi takip etmek için kodu okutun** satırıyla birlikte bir QR kodu bulunur: hem yazdırılan fişte hem de ödemeden sonra ekranda gösterilen fişte. Fişi yeniden yazdırmak aynı QR kodunu yazdırır. Bu, aşağıdaki notlarda açıklanan fiş ayarına bağlıdır.
3. Kod okutulunca müşterinin telefonunda sipariş numarasını ve güncel durumunu gösteren küçük bir sayfa açılır — personelinizin **Siparişler** ekranında işaretlediği durumların aynısı: hazırlanıyor, hazır, teslim edildi.
4. Sayfa birkaç saniyede bir kendini günceller; personel "Hazır"a dokunduğu anda müşteri bunu görür — sayfa yenilemeden, tezgâha sormadan.
5. Bağlantı, kioskun kullanıldığı dilde açılır. Yazdırılan bir fişin bağlantısı mağazanın dilinde açılır.
6. Müşteri kasada ödemeyi seçtiyse, personeliniz siparişin ödemesini alana kadar sayfa **Kasada ödeme bekleniyor** gösterir. Ondan sonra siparişin durumunu diğer siparişlerdeki gibi gösterir. Yaklaşık 2 saatten uzun süre ödenmeyen bir sipariş, ödenene kadar sayfada görünmez.

## Müşteri neyi görür, neyi görmez

- Sayfa **yalnızca sipariş numarasını ve durumunu** gösterir — isim, ürün, fiyat veya ödeme bilgisi yoktur. Bağlantı, tahmin edilemeyen uzun ve rastgele bir koddur; her kod yalnızca tek bir siparişe aittir.
- Sipariş teslim edildikten (veya iptal edildikten) sonra bağlantı yaklaşık 2 saat daha çalışır, sonra yanıt vermez — eski bir fişin QR kodu sonsuza dek açık kalmaz.

## Notlar

- Sipariş takibi Shop, Pro ve Multi-location planlarına dahildir. Ücretsiz Yerel planda, Local Plus planında ya da ücretli özellikler duraklatılmışken (abonelik sona erdi veya kasa aboneliği 7 günden uzun süredir bulutla doğrulayamadı) onay ekranı ve fişler QR olmadan gösterilir; siparişin veya satışın kendisi bundan asla etkilenmez.
- Takip sayfası bulutumuzda barındırılacak: müşterinin telefonu mağazanızın Wi-Fi ağına ihtiyaç duymaz ve kasanın kendi ağ adresi hiçbir QR kodunda ya da fişte görünmez.
- Mağazanızda birden fazla kasa varsa ve sipariş kiosktan farklı bir kasada ödenirse, kioskun bağlantısı ödemeyi göremez: yaklaşık 2 saate kadar **Kasada ödeme bekleniyor** göstermeye devam eder, sonra yanıt vermez. Sipariş takibi bulutumuza taşınana kadar bunu önlemek için kiosk siparişlerinin ödemesini kioskun kendi kasasında alın.
- [Fiş tasarımcısı](/help/designer) içindeki **Sipariş takip QR kodu (ücretli planlar)** kutusu, kasa fişlerindeki QR kodunu açar veya kapatır. Fiş tasarımcısını ilk kez kaydedene kadar açıktır; ancak mağazanız "Burada mı, paket mi" diye sormuyorsa (perakende ve hizmet mağazaları bu soru kapalı olarak başlar) kapalıdır. Tasarımcıyı kaydetmek kutuyu gösterildiği gibi bırakır. İadelerin ve iptal edilen satışların fişlerinde asla QR kodu bulunmaz; birden fazla kasası olan bir mağazada ek bir kasanın fişlerinde de bulunmaz: bu kasanın takip bağlantısını, sipariş takibi bulutumuza taşınana kadar ana kasanın **Siparişler** ekranı göremez.
- Kimsenin **Siparişler** ekranında takip etmediği bir sipariş (kasadaki gündelik bir satış için olağan durum), satıştan 12 saat sonra yanıt vermez. Personeliniz durumunu belirlediği anda bunun yerine yukarıdaki kurallar geçerli olur.
