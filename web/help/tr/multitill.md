---
id: multitill
title: Birden çok kasa (tek dükkân)
section: Bağlantı ve eklentiler
order: 310
summary: "Aynı dükkân ağında birden çok kasa çalıştırın: biri ana kasadır, diğerleri ona katılır; katalog, fiyatlar, ayarlar ve stok otomatik paylaşılır."
routes: [/tills, /ui/tills/pending-pairings]
---

# Birden çok kasa (tek dükkân)

Aynı dükkân ağında birden çok kasa çalıştırın: biri ana kasadır, diğerleri ona katılır; katalog, fiyatlar, ayarlar ve stok otomatik paylaşılır.

## Nasıl kullanılır

1. **Ana kasada** Ayarlar → Kasalar bölümüne gidin ve eşleştirme kodunu görüntüleyin. Bu kod, hem adresi hem de tek kullanımlık bir jetonu içeren bir metin bloğudur (ya da bir QR kodu).
2. **İkinci cihaza** kasa uygulamasını kurun. İlk kurulum ekranında **Mevcut bir dükkâna katıl**'ı seçin. Katılmanın iki yolu vardır: ana kasayı ağda otomatik bulmak ya da eşleştirme kodunu elle yapıştırmak.
3. **Otomatik bulma** (en kolayı): **Bu ağda bir birincil kasa bul** düğmesine basın, listeden ana kasayı seçin, bu kasaya bir ad verin ve **Eşleştirme iste**'ye basın. Artık iki ekran da aynı 6 haneli kodu gösterir — eşleştiklerini kontrol edin, sonra isteği ana kasanın Kasalar sayfasından onaylayın (bunu bir yönetici yapar). Kimse onaylamazsa istek 10 dakika sonra zaman aşımına uğrar. **Ya da kodu yapıştırın**: 1. adımdaki eşleştirme kodunun **tamamını** Eşleştirme kodu kutusuna yapıştırın, kasaya bir ad verin ve Katıl'a basın. Ana kasanın yalnızca adresi yeterli değildir — kaydı yetkilendiren tek kullanımlık jeton kodun içindedir.
4. Bitmesini bekleyin. Dükkânın tamamı — katalog, fiyatlar, ayarlar, stok ve kullanıcılar — kopyalanır; yoğun bir ağda bu bir dakikayı bulabilir. Katıl'a iki kez basmayın: jeton tek kullanımlıktır ve ikinci basış "kod kullanılmış veya süresi dolmuş" hatasıyla başarısız olur.
5. Başarısız olursa nedeni düğmenin altında görünür. En sık nedenler: iki kasanın aynı ağda olmaması ve kodun yalnızca bir kısmının yapıştırılması.
6. Her kasadaki satış ana kasaya akar; katalog değişiklikleri yaklaşık yarım dakika içinde tüm kasalara yayılır.
7. Katılmış bir kasada Katalog ve Envanter salt okunurdur — üstteki bir şerit bunu belirtir ve sizi ana kasaya yönlendirir. Windows ve Mac'te bu şerit tıklanabilir bir bağlantıdır; kiosk uygulamasında ise düz metin olarak kalır, çünkü kiosk cihazının bir bağlantı onu başka bir kasanın ekranına götürürse geri dönecek bir yolu yoktur.
8. Stok konusunda karar yalnızca ana kasaya aittir. Katılmış bir kasa, stok yetersiz diye bir satışı asla reddetmez — kendi stok sayısı sadece birkaç saniye geride kalabilen bir kopyadır, bu yüzden satışa her zaman izin verir. Bu durum bir ürünün stoğunu gerçekten eksiye düşürürse (iki kasanın son adedi neredeyse aynı anda satması gibi), bu hiçbir satışı engellemek yerine ana kasanın panosunda bir sorun olarak görünür.
