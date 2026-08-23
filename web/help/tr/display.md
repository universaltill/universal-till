---
id: display
title: Diller ve görünüm
section: Dükkanı kurma
order: 140
summary: Kasa baştan sona English, Türkçe, فارسی ve العربية konuşur — sağdan sola düzen dâhil — ve ekran klavyesiyle, ayarlanabilir yazı boyutuyla dokunmatik ekranlara uyum sağlar.
routes: [/settings]
---

# Diller ve görünüm

Kasa baştan sona English, Türkçe, فارسی ve العربية konuşur — sağdan sola düzen dâhil — ve ekran klavyesiyle, ayarlanabilir yazı boyutuyla dokunmatik ekranlara uyum sağlar.

## Nasıl kullanılır

1. Dili menüden değiştirin; her kullanıcı kendi dilini seçebilir.
2. Ayarlar → Görünüm: ekranınıza göre arayüz ölçeğini ayarlayın.
3. Ekran klavyesi dokunmatik ekranlarda otomatik açılır (Görünüm ayarlarından zorla açıp kapatabilirsiniz).
4. Ayarlar → Kasalar: bu kasaya diğer kasalardan kolayca ayırt edilecek kendi adını verin (ör. "Ön Kasa") — Kasalar sayfasında görünür ve siz bir ad belirleyene kadar varsayılan olarak "Kasa 1" olur.
5. Ayarlar → Kasalar: "Bu cihazın kasası" bu cihazın hangi kasa olduğunu belirler. Tek kasa varsa otomatik seçilir; birden fazla kasası olan bir mağazada burada seçin — nakit çıkışları (ör. depozito iadesi) bu kasanın açık vardiyasına kaydedilir ve cihaz, hangi kasa olduğunu bilmeden çıkış kaydetmeyi reddeder.
6. Ayarlar → İşletme türü: kurulum sihirbazında seçtiğiniz işletme türü (kafe, perakende, hizmet, konaklama, pazar tezgâhı veya diğer) — istediğiniz zaman buradan değiştirin.
7. Ayarlar → Veriler → Örnek veriler: kurulumda başlangıç kataloğunu yüklediyseniz hepsini tek dokunuşla buradan kaldırın — örnek ürünler, 3 örnek müşteri ve 3 örnek promosyon kodu birlikte, böylece örnek verilerden kalan bir indirim kodu siz gerçek verilerinize geçtikten sonra da kullanılabilir kalmaz. Zaten gerçekten kullanılan her şey korunur (satış geçmişiniz bozulmaz, ve gerçekten kullanmaya başladığınız bir örnek müşteri ya da kod kalır) — kasa kaç kaydı kaldırdığını, kaçını tuttuğunu söyler. Örnek bir ürün ya da müşteri şu anda kasanın sepetindeyse (kasiyer veya self-order), sepet boşaltılana kadar kaldırma engellenir — sepeti boşaltıp tekrar deneyin.
8. Ayarlar → Görünüm'de ayrıca bir pencere modu seçici (normal, büyütülmüş, tam ekran, kiosk), açılışta başlat anahtarı ve yanlarında bir "İşletim sistemi penceresine çık" eylemi (yönetici PIN'i ister) bulunur. Linux masaüstünde pencere modu seçici ve İşletim sistemi penceresine çık ikisi de hemen uygulanır, yeniden başlatmaya gerek yoktur — tam ekran/kiosk için işletim sisteminin pencere çerçevesi gizlenir, İşletim sistemi penceresine çık ise kasayı tam ekrandan çıkarıp kontrolü masaüstüne geri verir; açılışta başlat hâlâ kasayı bir sonraki başlatışınızda uygulanır (gerçek bir otomatik başlatma girdisini ekler veya kaldırır). Raspberry Pi kiosk kasasında pencere modu seçici de gerçektir ve yeniden başlatmaya gerek kalmadan hemen uygulanır: "kiosk" moduna geçirin, özel kiosk ekranı hemen açılır; başka bir moda geçirin, hemen kapanır (Pi'nin açılışta başlat anahtarı hâlâ iskelet niteliğindedir — kutu zaten kiosk ekranını kendiliğinden açılışta başlatır); Pi cihazında İşletim sistemi penceresine çık'ın hiçbir etkisi yoktur — çıkılacak bir işletim sistemi masaüstü yoktur, geri dönüş yolunuz pencere modu anahtarıdır. Pi cihazında kiosk ekranı tek ekrandır — "kiosk" dışına geçmek onu kapatır ve arkasında bir konsol yoktur, bu yüzden geri açmak için aynı ağda başka bir cihaza (ya da SSH'a) ihtiyacınız olur. macOS ve Windows'ta üç ayar da hâlâ iskelet niteliğinde: kasa pencere modu/açılışta başlat seçiminizi kaydeder ve İşletim sistemi penceresine çık PIN'ini kabul eder, ama bu platform için destek çıkana kadar hiçbir şey gerçekten değişmez — bu arada orada farklı bir pencere modunu etkinleştirmek için kasayı yeniden başlatın.
9. Kurulum sihirbazında bir ülke seçmek, o ülkenin ücretsiz eklentilerini (bugün için: eşleşen dil paketini) arka planda otomatik olarak getirir — mağazada aramanıza gerek kalmaz, ve o sırada çevrimdışı olsanız bile çalışır, bağlantı geri gelince kendiliğinden yeniden dener. Kurulum sürerken (veya yeniden denemeyi beklerken) Ayarlar → Veriler'de istemiyorsanız kaldırabileceğiniz küçük bir not ve Kaldır düğmesi görürsünüz — hiçbir şey zorunlu değildir.
