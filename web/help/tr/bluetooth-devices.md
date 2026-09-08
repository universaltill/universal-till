---
id: bluetooth-devices
title: Bluetooth cihazları
section: Bağlantı ve eklentiler
order: 236
summary: "Bluetooth barkod okuyucuyu veya teraziyi kasayla POS'un içinden eşleştirin — tara, Eşleştir'e dokun, bitti — işletim sisteminin ayarlarını hiç açmadan."
routes: [/bluetooth-devices]
---

# Bluetooth cihazları

Bluetooth barkod okuyucuyu veya teraziyi kasayla POS'un içinden eşleştirin — tara, Eşleştir'e dokun, bitti — işletim sisteminin ayarlarını hiç açmadan.

## Nasıl kullanılır

1. Menüden **Bluetooth cihazları**'nı açın (yalnızca yönetici). **Eşleşmiş cihazlar** listesi, bu kasayla zaten eşleşmiş olan her şeyi adresiyle ve şu anda bağlı olup olmadığıyla gösterir.
2. Yeni cihazı eşleştirme moduna alın (çoğu okuyucuda: tetiği basılı tutun ya da kılavuzundaki "eşleştirme" barkodunu okutun), sonra **Cihaz tara**'ya dokunun. Tarama yaklaşık on saniye sürer ve bulduklarını listeler; okuyucu ya da klavyeye benzeyen cihazlar **Okuyucu / klavye** olarak işaretlenir.
3. Cihazın yanındaki **Eşleştir**'e dokunun. Kasa cihazı tek seferde eşleştirir, güvenilir yapar ve bağlar; sayfa yenilenir ve cihaz eşleşmiş listesinde görünür. Bundan sonra yakınlarda açıldığında kendiliğinden bağlanır — okuyucu o andan itibaren kabloyla takılı bir okuyucu gibi çalışır.
4. Bir cihazı kaldırmak için yanındaki **Unut**'a dokunun. Artık güvenilir ya da bağlı değildir ve siz yeniden eşleştirene kadar bağlanmaz.

## Bilmekte fayda var

- Tarama tek başına asla hiçbir şeyi eşleştirmez — bir cihaz kasaya yalnızca üzerindeki **Eşleştir**'e dokunduğunuzda katılır.
- Cihaz PIN isterse buradan eşleştirilemez. Neredeyse her Bluetooth okuyucu ve terazi PIN'siz eşleşir; ısrar eden nadir bir cihaz için kurulumu yapan kişiye danışın.
- Menzil dışındaki ya da kapalı bir cihaz yalnızca *bağlı değil* olarak görünür; bu asla bir satışı ya da kasadaki başka bir şeyi durdurmaz.
- Bluetooth'u olmayan bir kasada (adaptör yok ya da Bluetooth hizmeti çalışmıyor) sayfa bunu söyler ve tarama düğmesi kapalıdır — başka hiçbir şey değişmez.
- Android tabanlı bir kasada (tablet), sayfa aynı şekilde çalışır; yalnızca Android'in kendisinin dayattığı üç şey vardır:
    - **Bluetooth kapalıysa**, sayfa bunu söyler ve **Bluetooth aç** düğmesini sunar. Ardından Android onayınızı ister — hiçbir uygulamanın Bluetooth'u kendiliğinden açmasına izin verilmez, bu yüzden kasa bu onayı atlayamaz.
    - **İlk seferde** sayfa, bu kasaya henüz Bluetooth kullanma izni verilmediğini söyler ve **Bluetooth erişim izni ver** düğmesini sunar; Android yalnızca siz bu düğmeye bastığınızda sorar. Reddederseniz sayfa bunu söyler ve düğmeye yeniden basabilirsiniz; "bir daha sorma" dediyseniz sizi uygulamanın kendi izin ekranına götürür.
    - **Unut** uygulamanın içinden çalışmayabilir. Android, uygulamalara eşleştirmeyi kaldırmak için desteklenen bir yol vermez; bu yüzden bazı Android sürümlerinde başarılı olur, diğerlerinde sayfa bunu açıkça söyler ve bunu orada yapmanız için **Bluetooth ayarlarını aç** düğmesini verir. Her iki durumda da, hâlâ eşleştirilmiş bir cihazı unuttuğunu asla iddia etmez.
- Bu üç düğme yalnızca kasanın kendisinde görünür. Bu sayfayı ağdaki başka bir bilgisayardan açarsanız neyin yanlış olduğunu yine görürsünüz, ancak değişikliğin kasada yapılması gerekir.
- Eşleştirme ve unutma, kimin yaptığıyla birlikte denetim izine kaydedilir.
