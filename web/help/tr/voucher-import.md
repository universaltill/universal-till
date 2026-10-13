---
id: voucher-import
title: Kupon bakiyelerini içe aktar
section: Dükkanı kurma
order: 148
summary: Başka bir kasadan mı geçiyorsunuz? Müşterilerinizin zaten elinde bulunan fiziksel kupon (hediye kartı) kartlarındaki kalan bakiyeleri, yeni bir satış olarak değil, açılış bakiyesi olarak içeri aktarın.
keywords: [kupon, hediye kuponu, hediye kartı, içe aktarma, geçiş, bakiye, açılış bakiyesi, csv]
---

# Kupon bakiyelerini içe aktar

Başka bir sistemden bu kasaya geçiyorsanız, bazı müşterileriniz zaten üzerinde gerçek para kalan fiziksel kupon (hediye kartı) kartlarına sahip olabilir. Bu sayfa o bakiyeleri, yeni bir satış kaydetmeden, **açılış bakiyesi** olarak — bu kasanın artık o müşterilere borçlu olduğu para olarak — içeri aktarır. Bir kuponun günlük kullanımda nasıl çalıştığı için bkz. [Hediye kuponları](/help/vouchers).

## Nasıl kullanılır

1. Önce **Menü → Hediye çekleri**'ni açın (yalnızca yönetici) ve sayfanın altındaki **Kupon bakiyelerini içe aktar** bölümüne inin.
2. Bir **code** sütunu (her fiziksel kartın üzerindeki kod), bir **balance** sütunu (üzerinde kalan tutar) ve isteğe bağlı bir **label** sütunu (kartı kimin taşıdığı) içeren bir CSV dosyası hazırlayın. Sütun adları esnektir — "amount" da "balance" kadar, "holder" da "label" kadar işe yarar — her sütun açıkça adlandırıldığı sürece.
3. Dosyayı seçin ve **Önizle**'ye basın. Henüz hiçbir şey kaydedilmez: kaç kupon oluşturulacağını, toplam değerlerini ve okunamayan satırların listesini, her birinin nedeniyle birlikte görürsünüz.
4. Önizlemeyi kontrol edin, ardından **Aktarımı onayla**'ya basın. Geçerli her satır, bu kasada, diğer her kupon gibi harcanmaya hazır, etkin bir kupona dönüşür.

## Bilmekte fayda var

- Bu kasada zaten var olan bir kod (önceki bir içe aktarımdan, ya da bu kasanın kendisinin verdiği bir koddan) asla üzerine yazılmaz — o satır atlanır ve bildirilir, böylece aynı dosyanın yanlışlıkla tekrar yüklenmesi bakiyeyi ikiye katlayamaz.
- Kodu olmayan, bakiyesi okunamayan ya da bakiyesi sıfır veya altında olan bir satır atlanır ve bildirilir; dosyadaki diğer her satır yine de içe aktarılır.
- Aynı dosyada birden fazla kez geçen bir kod, her iki seferinde de tamamen atlanır — hangisinin doğru olduğunu tahmin etmek yerine dosyayı düzeltip yeniden yükleyin.
- Hediye çekleri sayfasından dışa aktarılan bir dosya olduğu gibi içe aktarılır. `purpose` sütununda `single_purpose` yazan bir satır (belirli bir ürün için çek) reddedilir: vergisi satıldığında zaten kaydedildi ve bakiye olarak içe aktarmak, harcandığında onu ikinci kez vergilendirir. Dosyadaki tamamen kullanılmış ve iptal edilmiş çekler, içe aktarılacak bakiyesi olmayan satırlar olarak listelenir; bunları dışarıda bırakmak için dışa aktarmadan önce listeyi Etkin olarak filtreleyin.
- İçe aktarılan kuponlar, bu kasada satılanlarla tamamen aynı şekilde çalışır — bunları harcamak ve ödeme olarak almak için bkz. [Hediye kuponları](/help/vouchers).
- İçe aktarma, gün sonu raporunda "satılan" bir kupon olarak sayılmaz, çünkü burada gerçek bir satış gerçekleşmedi — bkz. [Raporlar ve gün sonu](/help/reports).
