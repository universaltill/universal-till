---
id: channel-orders
title: Çevrimiçi siparişler
section: Günlük satış
order: 16
summary: "Web mağazanızdan veya pazaryerlerinden gelip bu kasada kaydedilen siparişler: ne sipariş edildi, nasıl ilerliyor ve stokunuza ne yaptı."
routes: [/channel-orders, /channel-orders/{id}]
keywords: [çevrimiçi siparişler, web mağaza, pazaryeri, kanal, shopify, sipariş numarası, iade, stok, satış değil, kabul, toplama, paketleme, gönderme, takip, iptal]
---

# Çevrimiçi siparişler

Web mağazanızdan veya pazaryerlerinden gelip bu kasada kaydedilen siparişler: ne sipariş edildi, nasıl ilerliyor ve stokunuza ne yaptı. Bunlar kasa satışı değildir.

## Nasıl kullanılır

1. ☰ menüsünden **Çevrimiçi siparişler**i açın. En yeni sipariş en üsttedir. Her satırda sipariş numarası, verildiği zaman, geldiği kanal, durumu, kaç satırı olduğu ve toplamı görünür.
2. Satırlarını görmek için bir sipariş numarasına dokunun: ürün, stok kodu, adet, birim fiyat, kaç adedin iade edildiği ve bunlardan kaçının stoğa döndüğü. **Bir ürünle eşleşmedi** işaretli satırın burada karşılığı yoktur, bu yüzden stokunuz değişmedi; kasa ayrıca arka ofis sayfasında görebileceğiniz bir sorun bildirir.
3. Sipariş kaydedilirken stok sipariş edilen adet kadar düşer; bir ürün sıfırın altına inse bile (kasa sizi uyarır). Kanalın stoğa döndü olarak işaretlediği bir iade o adetleri geri ekler. Müşteri bilgileri asla saklanmaz: yalnızca müşterinin ülkesi gösterilir.
4. **Siparişi kabul edin.** **Eylemler** bölümü siparişin en üstündedir. Yeni bir sipariş için **Siparişi kabul et** (kabul etmek çevrimiçi mağazaya verdiğiniz sözdür) ve **Siparişi iptal et…** görünür. **Siparişi kabul et**e dokunun.
5. **Toplayın ve paketleyin.** **Toplandı olarak işaretle** ve **Paketlendi olarak işaretle** isteğe bağlıdır; mağazayı haberdar etmek için kullanın. Sipariş kabul edildikten sonra doğrudan gönderime de geçebilirsiniz.
6. **Gönderin.** **Siparişi gönder…** küçük bir form açar: kargo firmasını ve varsa takip numarasını ve takip bağlantısını (https:// ile başlayan tam bir web adresi) girin. Sonra **Gönderildi olarak işaretle**ye dokunun.
7. **İptal edin.** **Siparişi iptal et…** bir form açar: bir neden seçin, ürünlerin stoğa dönüp dönmeyeceğine karar verin (varsayılan olarak işaretlidir) ve **Bu siparişi iptal et**e dokunun. Bu işlem siparişi çevrimiçi mağazada da iptal eder ve geri alınamaz. Gönderilmiş bir sipariş iptal edilemez.
8. **Gönderilmeyi bekleyen işlem.** Dokunduktan sonra **Gönderi henüz iletilmedi** gibi kesik çizgili bir etiket görünür ve durum olduğu gibi kalır. İşlem bu kasada kaydedilir ve kasa çevrimiçi olur olmaz çevrimiçi mağazaya gönderilir. Durum, mağaza onayladığında değişir; stok da ancak o zaman değişir.
9. **Mağaza reddederse.** Bir uyarı nedenini söyler (örneğin sipariş zaten iptal edilmişti) ve sipariş listede **Kabul edilmedi** olarak görünür. Okuduktan sonra **Gizle**ye dokunun. Çevrimiçi mağaza bir gönderiyi veya iptali otomatik olarak alamıyorsa, uyarı aynı değişikliği çevrimiçi mağazanın kendi yönetim panelinde yapmanızı ister.

## Notlar

- Bu siparişler **kasa satışı değildir**. Çekmecede, Z raporunda, Günlükte ve mali günlükte yer almazlar; mağaza bunları çevrimiçi aldı ve tahsil etti.
- Çevrimiçi siparişleri yalnızca ana kasa kaydeder ve onlar üzerindeki işlemleri yalnızca ana kasa yapar. Bu sayfayı Raporları görebilenler açabilir; işlem yapmak için ayrıca stoku yönetme yetkisi gerekir. Ek bir kasada sayfa yalnızca görüntülenir.
- Kasa çevrimdışıysa siparişler bulutta bekler ve kasa bir sonraki bağlanışında kaydedilir; yaptığınız işlemler de kasada bekler ve kasa yeniden bağlanınca gönderilir. Kasadaki satış bundan asla etkilenmez.
- Bu kasa artık ana kasa olmazsa, henüz gönderilmemiş işlemler siz gizleyene kadar bir uyarıyla sayfada kalır. Sessizce atılmazlar.
