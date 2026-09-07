---
id: fiscal-device
title: Mali cihaz (Türkiye)
section: Dükkanı kurma
order: 113
summary: "Kasanın hangi yazarkasa (YN ÖKC) üzerinden ödeme aldığını, cihazın fiş bastığının doğrulanıp doğrulanmadığını ve son verdiği fişi görün — yasal fişi cihaz basar, kasa her satışta cihazın fiş numarasını saklar."
routes: [/fiscal-device]
keywords: [mali, türkiye, ÖKC, yazarkasa, GİB, fiş, cihaz, Z raporu]
---

# Mali cihaz (Türkiye)

Türkiye'de perakende satış, onaylı bir yazarkasa ile belgelenir — tezgâhtaki "yazarkasa POS", yani *Yeni Nesil Ödeme Kaydedici Cihaz* (YN ÖKC). Parayı cihaz alır, yasal fişi (*mali fiş*) cihaz basar ve günlük toplamlarını üreticisi üzerinden vergi dairesine iletir. Universal Till bu cihazın yerine geçmez: Türkiye mali cihaz eklentisi kuruluyken cihaz, **Yazarkasa (ÖKC)** adlı bir ödeme yöntemi olur. Ödeme anında kasa sepeti cihaza gönderir, cihaz nakit veya kartla tahsilat yapıp fişi basar; kasa da cihazın fiş numarasını, seri numarasını ve Z sayacını satışa kaydeder ve kendi fiş kopyasında gösterir.

Bu sayfa bu düzeni kasanın tarafından gösterir. Cihazla kendisi konuşmaz ve vergi dairesiyle hiçbir şeyi kontrol etmez; kasanın, cihazın verdiği yanıtlardan kaydettiklerini okur.

## Nasıl kullanılır

1. **Eklentiler**'den **Türkiye mali cihazı (YN ÖKC)** eklentisini kurup etkinleştirin ve istediği ağ iznini verin. Dükkanınızın ülkesi Türkiye ise menüde **Mali cihaz** görünür (yalnızca yönetici).
2. **Eklenti** altında **Eklenti ayarlarını aç** bağlantısını izleyip cihazın dükkan ağındaki yerini girin — sürücü, adres ve port. Bir üreticinin sürücüsü tamamlanana kadar *bridge* sürücüsü bir köprü programıyla ya da test için kullanılan simülatörle konuşur; sürücüsü hazır olmayan eklenti, -mış gibi yapmak yerine her ödemeyi reddeder.
3. Ödeme olarak **Yazarkasa (ÖKC)** seçerek bir satış yapın. Cihaz fişi basar; kasa fişi kaydeder. İlk fiş, bu sayfada cihazı otomatik olarak **doğrulanmış** yapar.
4. Cihazın test fişi bastığını zaten gördüyseniz ve ilk gerçek satıştan önce doğrulanmış olarak işaretlemek istiyorsanız **Cihazı doğrula**'ya basın. Cihaz kaldırıldığında ya da değiştirildiğinde **Cihazı ayır**'a basın — bir cihaz kendini kanıtlayana kadar sistem kaydı olarak satışlar yeniden reddedilir.

## Bilmekte fayda var

- **Doğrulanmış**, kasanın Türkiye güvencesinin okuduğu durumdur: dükkan sistem kaydı olarak ayarlıyken ve doğrulanmış cihaz yokken kasa, fişsiz satış almak yerine satışı tamamlamayı reddeder. Gölge modunda (dükkanın mevcut cihazı yasal kayıt olmaya devam eder) hiçbir şey reddedilmez.
- Cihaz satış başına tek mali fiş basar; bu yüzden ÖKC ödemesi satışın tamamını kapsamalıdır. Cihaz ile başka bir yöntem arasında bölünmüş ödeme reddedilir.
- Cihaz reddederse, zaman aşımına uğrarsa ya da ulaşılamazsa ödeme reddedilir ve sepet korunur — cihazı ya da ağı düzeltip yeniden deneyin. Reddedilen bir ödeme için iki tarafta da hiçbir şey kaydedilmez.
- **Bugünkü fişler**, raporların kullandığı aynı sınırla, iş günü başlangıcından bu yana cihaz fişlerini sayar.
- Bu sayfa veri kaydeder ve durumu gösterir. Dükkanınızın cihazının, mükellefiyet sınıfının ve evrakının yükümlülüklerinizi karşılayıp karşılamadığı sizinle mali müşaviriniz arasındadır; sayfa bunu tasdik etmez.
