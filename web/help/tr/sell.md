---
id: sell
title: Satış ve ödeme ekranı
section: Günlük satış
order: 10
summary: "Ana kasa ekranı: ürünleri okutun veya seçin, ödemeyi alın, fişi yazdırın ya da atlayın."
routes: [/, /ui/basket, /ui/buttons, /ui/held, /refund/{receipt}]
keywords: [kamera]
---

# Satış ve ödeme ekranı

Ana kasa ekranı: ürünleri okutun veya seçin, ödemeyi alın, fişi yazdırın ya da atlayın. Tamamen çevrimdışı çalışır — internet kesintisi satışı asla engellemez.

## Nasıl kullanılır

1. Barkodu okutun veya satış ekranında ürünü bulup dokunun: kategori sekmeleri arasında geçiş yapın ya da geçerli sekmeyi ada göre filtrelemek için arama kutusuna yazın, ardından eklemek için ürün kartına dokunun.
2. Barkod okuyucunuz yok mu? Kameralı ve desteklenen bir tarayıcıya sahip bir cihazda, tarama kutusunun yanındaki 🔳 düğmesine dokunarak bunun yerine kamerayla tarayın — barkodu veya QR kodunu gösterin; ürün otomatik olarak eklenir ve kamera kapanır. Bu, takılı bir okuyucunun yerini almaz veya onu devre dışı bırakmaz, ve hiçbir fotoğraf ya da video cihazdan asla çıkmaz.
3. Sepette adedi değiştirin veya satırı silin, sonra Öde'ye geçin.
4. Sepeti beklet ile sıradaki müşteriye geçin, sonra geri çağırın; iadeler satış geçmişindedir.
5. Tamamlanmış bir satışı iade etmek için Journal → satış geçmişinden açın: hangi satırların ne kadarının iade edileceğini seçin (daha önce iade edilen miktar izlenir, satılandan fazlasını iade edemezsiniz), ardından nakit mi yoksa orijinal ödeme yöntemine mi iade edileceğini seçin.

## Almanya'daki mağazalar: TSE ve gerçek satışlar

Mağazanızın ülkesi Almanya ise ve bir yönetici mağazayı resmi kayıt moduna aldıysa (deneme değil, gerçek ve yasal olarak bağlayıcı satışların kaydedildiği mod), kasa her satışı tamamlamadan önce mağazanın TSE'sini (teknik güvenlik birimi) denetler:

- **Kurulu TSE yok:** satış, satış ekranında bir mesajla reddedilir. Bu durum için geçersiz kılma yoktur. Bunu düzeltmek için bir TSE kurun — kendi donanımınız, kendi bulut hesabınız veya yönetilen bir abonelik — ya da hazır olana kadar mağazayı resmi kayıt modundan çıkarması için bir yöneticiden isteyin.
- **TSE kurulu ama şu anda arızalı:** satışlar duraklatılır. Mağaza sahibi (yönetici) geçici bir geçersiz kılma verebilir — bunun için bir onay ifadesinin yazılması, bir gerekçe ve bir süre (en fazla 8 saat) gerekir. Geçersiz kılma etkinken satış ekranında bir şerit görünür, alınan her satış denetim kaydında işaretlenir ve fişte TSE'nin kullanılamadığı belirtilir. Süre dolduğunda satışlar yeniden otomatik olarak duraklar.
- Deneme ve tanıtım mağazaları (resmi kayıt modunda olmayanlar) bu denetimden hiçbir zaman etkilenmez.

Bu denetim yalnızca mağazanızın kasadaki kendi ayarlarına bakar — asla ağa bağlı değildir ve çevrimdışı olmak TSE arızası sayılmaz.
