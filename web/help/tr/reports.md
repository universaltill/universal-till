---
id: reports
title: Raporlar ve gün sonu
section: İşi yürütme
order: 210
summary: Gün, reyon ve ödeme türüne göre satış toplamları; en çok/yavaş satanlar; ölü stok; en yoğun gün ve saatler; kâr marjları; vergi özeti; geçen yıla göre karşılaştırma — ve kasa kapanışı için gün sonu (Z) raporu.
routes: [/reports, /journal, /journal/{receipt}, /shifts, /audit]
---

# Raporlar ve gün sonu

Gün, reyon ve ödeme türüne göre satış toplamları; en çok/yavaş satanlar; ölü stok; en yoğun gün ve saatler; kâr marjları; vergi özeti; geçen yıla göre karşılaştırma — ve kasa kapanışı için gün sonu (Z) raporu.

## Nasıl kullanılır

1. Raporlar'ı açın: üstteki satır seçili dönemin ana rakamlarını (ciro, satış, vergi, iadeler, net, geçen yıl) ve düşük stok uyarısını her zaman gösterir.
2. Altındaki sekmelerden birini seçin — Satış eğilimi, Ürünler, Vergi, Tahmin, Ödemeler ve kanallar veya Gün sonu (EOD) — o rapor yalnızca sekmeyi açtığınızda çalışır.
3. Kapanışta Gün sonu'nu (Gün sonu sekmesinde) çalıştırın: günü toplar ve kayıtlarınız için yazdırabilir.

## Rapor dönemleri

Üstteki satırın yanında, dönemin nasıl hesaplanacağını seçin:

- **Özel** — orijinal kayan pencere (bugün, şimdiden geriye 7/14/30/90 gün).
- **Gün / Hafta / Ay / Yıl** — kayan bir sayım yerine gerçek bir takvim dönemi: Gün bir işletme günü, Hafta Pazartesi-Pazar, Ay bir takvim ayı, Yıl bir takvim yılıdır. Seçimin yanında bir tarih seçici görünür, böylece geçmiş bir dönemi görebilirsiniz — örneğin Ay'ı seçip Temmuz'da bir tarih seçerek, bugün Ağustos olsa bile Temmuz'un rakamlarını görebilirsiniz.
- Satış eğilimi, Ürünler, Vergi ve Ödemeler ve kanallar seçili dönemi kullanır, böylece her zaman yukarıdaki rakamlarla uyuşur. Tahmin ve Gün sonu arşiv listesi ise kullanmaz — bunlar dönem seçiciden bağımsız olarak kendi sabit pencerelerini gösterir.

## İşletme günü başlangıcı

Varsayılan olarak bir rapor "günü" gece yarısından gece yarısına kadar sürer. Gece yarısından sonra da satış yapıyorsanız — bir bar, geç saatlere kadar açık bir mutfak — bu, bir gecelik hasılatı iki rapor gününe böler. "İşletme günü şu saatte başlar" ayarını (Gün sonu sekmesinde, otomatik gün sonu saatinin yanında) işletme gününüzün gerçekten başladığı saate, örneğin 06:00'ya ayarlayın; böylece Gün/Hafta/Ay/Yıl dönemleri saatin değil gerçek işletme gününüzün sınırlarını izler.

## Rapor saklama

Arşivlenen her gün sonu raporu **10 yıl** saklanır — bu yasal bir kayıttır
ve Veri yönetimi'ndeki "İşlem geçmişini temizle" sıfırlama düğmesi ona asla
dokunmaz. (O düğme artık hiçbir şeyi yok etmez: işlem geçmişinizi bir
sıfırlama arşivine taşır ve kasa o sıfırlamadan bu yana işlem yapmadığı
sürece arşivlenmiş grup Ayarlar → Veri yönetimi'nden geri yüklenebilir.)
Bir rapor 10. yılını doldurduğunda, arka
planda otomatik ve kalıcı olarak silinir — manuel bir adım veya onay
istemi yoktur, bu yüzden daha uzun süre saklamanız gereken her şeyi bundan
önce dışa aktarın.

Ayarlar → Rapor saklama'da raporların nerede tutulacağını seçin:

- **Yalnızca bu kasa** — bugün itibarıyla çalışır, ek bir kuruluma gerek
  yoktur. Rapor arşivleri küçüktür (kapanan her gün için birkaç KB), bu
  yüzden 10 yıllık bir arşiv modern bir kasanın diskini doldurmaz.
- **Yalnızca bulut** / **Kasa + bulut** — bulut depolama ve bir mağaza
  aboneliği kullanılabilir hale geldiğinde gelecek bir sürüm için
  gösterilir; henüz seçilemez.

Aynı sayfa **kayıtlarınızın ne kadar geriye gittiğini** (en eski ve en
yeni arşivlenmiş rapor ile sayıları) ve bir **dışa aktar** düğmesi
gösterir — bir tarih aralığı seçin ve eşleşen raporları CSV veya JSON
olarak indirin, örneğin bir denetçiye vermek için.

## Nakit düzeltmeleri ve çıkışları (Vardiyalar)

Vardiyalar sayfasındaki "Nakit düzeltme / çıkış" formu, bir satış dışında
kasadaki beklenen nakdi değiştiren her şeyi kaydeder — bozukluk takviyesi,
kasa sayım düzeltmesi veya çekmeceden nakit çıkışı. Nakdi **azaltan** her
düzeltme, seçilen türden bağımsız olarak yönetici PIN'i gerektirir — bir
iade veya depozito iadesi (Pfandrückgabe) çıkışının gerektirdiği aynı onay,
çünkü risk aynıdır (kasadan onaysız nakit çıkışı). Nakit eklemek (pozitif
bir tutar, örn. bozukluk takviyesi) buna gerek duymaz.
