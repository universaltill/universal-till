---
id: printing
title: Fiş ve yazdırma
section: İşi yürütme
order: 230
summary: Fişleri, faturaları ve gün sonu raporlarını termal fiş yazıcısında veya normal ofis yazıcısında basar; mutfak siparişleri ayrı yazıcıya gidebilir.
---

# Fiş ve yazdırma

Fişleri, faturaları ve gün sonu raporlarını termal fiş yazıcısında veya normal ofis yazıcısında basar; mutfak siparişleri ayrı yazıcıya gidebilir.

## Nasıl kullanılır

1. Ayarlar'dan yazıcınızı ve türünü seçin — termal veya normal.
2. Ağ yazıcınızın adresini bilmiyor musunuz? Elle yazmak yerine **Bu ağda yazıcı bul** düğmesiyle tarayın — yazıcı adresi alanlarının yanında yalnızca yöneticilere gösterilir. Yalnızca kendini AppSocket/JetDirect olarak duyuran yazıcıları bulur; yalnızca IPP destekleyen veya USB ile bağlı bir yazıcı görünmez ve adresi ya da aygıt yolu yine de elle girilmelidir.
3. Bağlantıyı test yazdırma düğmesiyle kontrol edin.
4. Mutfak yazıcısı ayrı ayarlanabilir; yemek siparişleri hazırlandıkları yerde yazdırılır — aynı **Bu ağda yazıcı bul** düğmesi her iki alan için de aday sunar.
5. Birden fazla mutfak yazıcısına mı ihtiyacınız var — bir ızgara yazıcısı ve bir bar yazıcısı gibi? Kategorileri veya tek tük ürünleri kendi istasyonlarına yönlendirmek için **Mutfak istasyonları**na bakın.
6. Mutfak fişlerindeki sipariş türü ve istasyon başlığı, her zaman İngilizce yerine kasanın ayarlı dilinde basılır. Yalnızca mutfağa özel ayrı bir dil ayarı yoktur — kasanın tek ayarlı diline bağlıdır.
7. Nakit satıştan sonra kasa çekmecesi açılmıyor mu? Çoğu çekmece pim 2'ye (varsayılan) bağlıdır — çekmeceniz pim 5 gerektiriyorsa yazıcı ayarlarındaki **Kasa çekmecesi pimi**nden değiştirin.
8. Para birimi simgeleri bozuk mu basılıyor — `€` veya `£` garip karakterler olarak mı çıkıyor? Birçok termal yazıcı UTF-8 anlamaz. v0.12.12 sürümünden itibaren kasa bunu sizin için seçer: avro veya sterlin ile ve bir Batı Avrupa diliyle kurulmuş bir mağaza **Batı Avrupa (CP858 — €/£)** biçimini kendiliğinden gönderir; daha önce kurulmuş bir kasa ise bu sürümde ilk açılışında bir kez bu biçime geçirilir. Avro veya sterlin ile bir Hırvatça, Slovence veya Slovakça ile kurulmuş bir mağaza aynı şekilde **Orta Avrupa (Windows-1250)** gönderir (sterlin yalnızca para birimi sembolü gerçekten karşılandığında değişir — Windows-1250'de `£` yoktur, bu yüzden sterlin kullanan bir Hırvat/Sloven/Slovak mağazası UTF-8 üzerinde kalır); Estonca, Letonca veya Litvanca **Baltık (Windows-1257)** gönderir; Yunanca **Yunanca (Windows-1253)** gönderir; ve Türkçe **Türkçe (Windows-1254)** gönderir — Baltık, Yunanca ve Türkçe `€` ve `£`'yi kapsar. Ayarlar → Yazıcı'daki **Karakterler** ayarına yalnızca bunu geçersiz kılmak isterseniz ihtiyacınız olur — örneğin UTF-8'i gerçekten destekleyen bir yazıcı için. Bu yalnızca fiş yazıcısına gönderileni değiştirir — kasanın ekrandaki dil desteği etkilenmez. Mağaza Türk lirası (`₺`) ile fatura kesiyorsa **UTF-8** üzerinde kalır: yukarıdaki kod sayfalarının hiçbiri lira işaretini yazdıramaz, bu nedenle geçiş yapmak bozulmayı düzeltmez. Arapça ve Farsça kasalar da benzer şekilde **UTF-8** üzerinde kalır ve asla otomatik olarak değiştirilmez — burada tek baytlı hiçbir kod sayfası bu alfabeleri kapsamaz.
9. **Her satıştan sonra fiş** (Ayarlar → Yazıcı), bir satış tamamlandığında kâğıt fişe ne olacağını belirler: **Her zaman yazdır** (varsayılan — her satış yazdırılır), **Müşteriye sor** (fiş ekranı **Fişi yazdır** / **Fiş istemiyorum** düğmeleriyle *Fiş ister misiniz?* diye sorar — her iki durumda da satış zaten tamamlanmıştır ve alışılmış Yazdır düğmesi altta kullanılabilir kalır) veya **Hiçbir zaman otomatik yazdırma** (kasiyer gerektiğinde fiş ekranından yazdırır). Almanya'daki mağazalar, Alman mağazaları için fiş kuralları doğrulanana kadar şimdilik Her zaman yazdır olarak sabittir — kurulu bir ülke eklentisi de seçenekleri kendi pazarının izin verdikleriyle sınırlayabilir.
