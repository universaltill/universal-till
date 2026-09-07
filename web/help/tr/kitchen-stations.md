---
id: kitchen-stations
title: Mutfak istasyonları
section: İşi yürütme
order: 235
summary: "Her yiyecek kategorisini — veya tek bir ürünü — kendi mutfak yazıcısına veya mutfak ekranına yönlendirin; ızgara siparişi ızgarada, içecek siparişi barda yazdırılsın."
routes: [/kitchen-stations, /kitchen-display/{station_id}]
---

# Mutfak istasyonları

Her yiyecek kategorisini — veya tek bir ürünü — kendi mutfak yazıcısına veya mutfak ekranına yönlendirin; ızgara siparişi ızgarada, içecek siparişi barda yazdırılsın.

## Nasıl kullanılır

1. Menüden **Mutfak istasyonları**nı açın (yalnızca yönetici) ve yemeğin hazırlandığı her yer için bir istasyon oluşturun — örneğin "Izgara" veya "Bar". **Hedef**ini seçin: **Yazıcı** (bir fiş), **Ekran** (bir mutfak ekranı — aşağıya bakın) veya **Yazıcı ve ekran**. Yazdıran bir istasyonun yazıcısının ağ adresi veya aygıt yolu gerekir; yalnızca ekran olan istasyona gerekmez. Yazıcı bu ağda zaten varsa önce **Bu ağda yazıcı bul**'a tıklayın — bulduklarını listeler, böylece adresi elle yazmak yerine birini seçebilirsiniz; siz istasyonu kaydetmeden hiçbir şey eklenmez.
2. **Kategori yönlendirme** bölümünde her kategorinin ürünlerinin yazdırılacağı istasyonları işaretleyin. Yönlendirmenin ana yolu budur — tek işaret kategorideki tüm ürünleri kapsar.
3. Farklı bir yere gitmesi gereken tek tük ürün için **Ürün istisnaları** altında ürünü arayıp istasyonlarını işaretleyin. Ürün istisnası yalnızca o ürün için kategori kuralının yerine geçer.
4. Satış tamamlandığında yazdıran her istasyon yalnızca kendi satırlarını içeren kendi fişini yazdırır. İki istasyona yönlendirilen ürün ikisinde de yazdırılır. Hiçbir yere yönlendirilmeyen her şey — tek istasyonu yalnızca ekran olan bir ürün dahil — eskisi gibi Ayarlar'daki varsayılan mutfak yazıcısında yazdırılır.

## Mutfak ekranı (fiş yerine veya fişin yanında bir ekran)

Hedefi **Ekran** veya **Yazıcı ve ekran** olan istasyonun kendi canlı sipariş ekranı vardır. İstasyonun yanındaki **Ekranı görüntüle**'ye tıklayarak açın, sonra o pencereyi bu kasaya bağlı ikinci monitöre taşıyın — bu kasadaki bir sayfadır, eşleştirme ya da ağ gerekmez.

- Ekran, en az bir ürünü bu istasyona yönlendirilmiş siparişleri en yeniden başlayarak listeler; Siparişler sayfasındaki aynı tek dokunuşlu **Hazırlanıyor** / **Hazır** / **Teslim alındı** düğmeleriyle. Birkaç saniyede bir ve herhangi bir siparişin durumu değiştiği anda kendini yeniler.
- Durum her ürüne değil, siparişin tamamına aittir: iki istasyon için ürün içeren bir sipariş iki ekranda da görünür; birinde Hazır veya Teslim alındı işaretlemek ikisini de günceller.
- Ekran yalnızca **bu** kasada alınan siparişleri gösterir — kasanın kendi Siparişler sayfasının aksine, eşitlendikten sonra bile başka bir kasada alınan siparişleri göstermez. Birden çok bağlı kasası olan bir dükkânda, mutfak ekranını ilgili siparişleri gerçekten alan kasada açın.
- Devre dışı bırakılan istasyonun ekranı siz yeniden etkinleştirene kadar çalışmaz; yalnızca yazıcı olan istasyonun ekranı yoktur.

## Bilmekte fayda var

- İstasyonu silmek yerine devre dışı bırakın — ürünleri siz yeniden etkinleştirene kadar varsayılan mutfak yazıcısına döner.
- Bir yazıcıya ulaşılamaması diğer istasyonları veya satışın kendisini asla durdurmaz.
- İstasyonlar ve yönlendirme mağaza geneli öğelerdir ve her zaman **ana kasadan** yönetilir: katılmış bir kasada bir istasyon eklemek, yeniden adlandırmak, hedefini değiştirmek veya kategori/ürün yönlendirmesini düzenlemek, uygulanmak yerine sizi ana kasaya yönlendiren bir mesaj gösterir.
- **Yazıcı adresi kasıtlı olarak kasaya özgü olan tek istisnadır**: katılmış bir kasada Mutfak istasyonlarını açın, o istasyonun kendi adresini yine de orada ayarlayabilirsiniz, çünkü her kasanın aynı paylaşılan istasyon için kendi bağlı yazıcısı olabilir. Siz ayarlayana kadar o istasyonun fişleri, katılmış kasanın kendi varsayılan mutfak yazıcısında (Ayarlar) yazdırılır.
