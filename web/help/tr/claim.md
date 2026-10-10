---
id: claim
title: Mağaza kaydı ve sahiplenme
section: Bağlantı ve eklentiler
order: 320
summary: Kayıt, kasanızı Universal Till marketplace'e bağlar; sahiplenme mağazayı SİZİN hesabınıza bağlar — çevrimiçi yönetim (Mağazalarım, kasa filosu) ve ücretli özellikler böyle açılır.
keywords: [kayıt, sahiplenme, bulut, marketplace, mağaza, eşleştirme, eşleştirme kodu, yeniden eşleştirme]
---

# Mağaza kaydı ve sahiplenme

Kayıt, kasanızı Universal Till marketplace'e bağlar; sahiplenme mağazayı SİZİN hesabınıza bağlar — çevrimiçi yönetim (Mağazalarım, kasa filosu) ve ücretli özellikler böyle açılır.

## Nasıl kullanılır

1. Ayarlar → Kasa kaydı mağaza kimliğini gösterir; gerekirse Şimdi kaydet ile bağlayın. Katılmış bir kasada kaydı ana kasa sizin için yapar: bu kart o zaman kasanın ana kasa üzerinden kayıtlı olduğunu söyler ve sahiplenme ana kasada yapılır.
2. Bu mağazayı sahiplen'e tıklayıp kısa kodu (15 dakika geçerli) ve QR'ı alın — telefonunuzla tarayıp oradan sahiplenin.
3. Universal Till kimliğinizle marketplace'e giriş yapın, sahiplenme sayfasını açın ve kodu girin.

## Otomatik kayıt — kurulumda sizin seçiminiz

Kurulum sihirbazının son ekranı, kasayı hemen kaydetmek isteyip istemediğinizi bir kez sorar — siz işaretlemedikçe işaretli değildir. Aynı seçenek Ayarlar → Kasa kaydı altında "Bu kasayı pazaryerine otomatik olarak kaydet" olarak da bulunur:

1. Açarsanız kasa; cihaz kimliğini, mağaza adını, mağaza ülkesini, mağaza bölgesini ve yazılım sürümünü (destek, güncellemeler ve lisanslama için kullanılır) Universal Till bulut pazaryerine gönderir ve hemen kaydolur. Adresiniz gönderilmez. O an çevrimdışı mı? Hiçbir şey beklemez, kurulum yine de tamamlanır — ama kasa, eklenti mağazasını açana veya Şimdi kaydol'a basana kadar kayıtsız kalır; yeniden çevrimiçi olduğunuzda Ayarlar → Kasa kaydı bölümünü kontrol edin.
2. Kapatırsanız kasa yalnızca eklenti mağazasını ilk kullandığınızda veya Şimdi kaydol'a bastığınızda kaydolmaya döner. Kapatmak, daha önce yapılmış bir kaydı asla kaldırmaz.

## Abonelik

Ayarlar → Abonelik (yalnızca yöneticiler) planınızı bu kasanın Universal Till bulutundan en son aldığı haliyle, hangi tarihe kadar ödendiğini ve en son ne zaman doğrulandığını gösterir. Kasada satış, fiş ve raporlar her planda ücretsizdir ve abonelik yüzünden asla durmaz.

- Yerel (ücretsiz): yapılacak bir şey yok, kart yalnızca ücretsiz planda olduğunuzu söyler.
- Etkin: planınız, ödendiği son tarih ve son doğrulama.
- Abonelik doğrulanmadı: kasa planınızı 7 günden uzun süredir bulutla doğrulayamadı, bu yüzden ücretli özellikler (bulut eşitleme, ürünleri tarayıcıdan yönetme ve yönetilen TSE kurulumu gibi) duraklatıldı. Kasanın internet bağlantısını kontrol edin; bağlandığında kendiliğinden düzelir.
- Abonelik sona erdi: aboneliğiniz artık etkin değil, bu yüzden ücretli özellikler Universal Till hesabınızdan yenileyene kadar duraklatılır. Ardından Ayarlar → Kasa kaydı bölümünde “Ücretli plan var mı diye kontrol et” düğmesine basın: aboneliği sona ermiş bir kasa buluta kendiliğinden yalnızca her gün ilk açıldığında bağlanır, yenilemeyi en hızlı bu şekilde öğrenir. Ana kasa üzerinden kayıtlı bir kasada düğmeye ana kasada basın: bu kasa yenilemeyi oradan alır.

Son iki durumda durum çubuğundaki bir işaret bu karta götürür; Kasa kaydı ve Fiş imzalama (TSE) kartları da hangi özelliklerinin duraklatıldığını yerinde gösterir. Hiçbir şey gizlenmez ve önceden kurulmuş bir TSE fişleri imzalamaya devam eder.

## Bulut eşitlemesi ücretli planlara dahildir

Kayıt ve sahiplenme ücretsizdir, eklenti mağazası da öyle. Kasayı çevrimiçi hesabınızla eşitlenmiş tutmak (satış rakamları, ürün listesi, mağazanın bulut yönetim sayfasında yapılan değişiklikler) ücretli planlara dahildir. Mağaza ücretli bir planda değilken kasa bulutla eşitleme yapmaz ve Ayarlar → Kasa kaydı bulut eşitlemesinin kapalı olduğunu söyler. Kasa buluta yalnızca kısaca bilgi verir: kurulduğunda, bir güncellemeden sonra ve her gün ilk açıldığında.

1. Mağaza ücretli bir plana geçtiğinde kasa bunu yeni bir günde ilk açıldığında ya da daha önce, birisi o kasada kayıt, eşleştirme, eklenti kurma veya sahiplenme kodu oluşturma yaptığında öğrenir. Ayarlar → Kasa kaydı bölümünde **Ücretli planı kontrol et** düğmesine de basabilirsiniz (bir yönetici veya admin onaylar). Bulut eşitlemesi bir dakika içinde başlar ve kendiliğinden çalışmaya devam eder.
2. Plan sona ererse bulut eşitlemesi kasanın bir sonraki yoklamasından sonra durur. Satış hiçbir zaman buna bağlı değildir: her satış her durumda çevrimdışı çalışır.

## Bulut bu kasayı artık kabul etmediğinde

Bulut bu kasanın kimlik bilgisini art arda üç kez reddederse durum çubuğunda bir uyarı görünür. Uyarı hiçbir satışı engellemez: kasa çevrimdışı satışa devam eder, buluta saatte bir yeniden bağlanmayı dener ve göndermesi gereken her şeyi yeniden bağlanana kadar kasada tutar. Uyarı şu üçünden birini söyler:

- **Mağazanın bulut hesabından kaldırıldı**: mağaza sahibi bu kasayı çevrimiçi olarak mağazadan kaldırdı.
- **Bu kasanın yeniden eşleştirilmesi gerekiyor**: kasanın bulut kimlik bilgisi artık çalışmıyor, kasanın mağazayla yeniden eşleştirilmesi gerekir.
- **Yeni mağaza olarak kaydol**: mağazanın sahip hesabı yok, bu yüzden kasa eşleştirilemez. Kasayı yeni bir mağaza olarak yeniden kaydedin; yalnızca eski cihaz geçmişi kaybolur.

Bir yönetici uyarıya dokunarak Ayarlar → Kasa kaydı'nı açabilir. Uyarı, bulutla bir sonraki başarılı bağlantıdan sonra kaybolur.

## Şimdi kaydol veya Bu mağazayı sahiplen başarısız olduğunda

Kart sorunu tek satırda söyler; satış hiçbir zaman etkilenmez:

- **Universal Till bulutu bu mağaza için kullanılamıyor**: bulut bu mağazaya hizmet vermiyor. Katılmış bir kasada bu mesaj ana kasadan gelir. Kasa çevrimdışı çalışmaya devam eder.
- **Bu kasada bulut adresi ayarlanmamış**: kasa bulut adresi olmadan kuruldu, bu yüzden kaydolamıyor. Kurulumu yapan kişiden bir adres ayarlamasını isteyin.
- **Başka bir kayıt denemesi hâlâ sürüyor**: kasa zaten arka planda kaydoluyor. Biraz bekleyip yeniden deneyin.
- **Kayıt başarısız** veya **Sahiplenme kodu alınamadı**: kasa buluta ulaşamadı. İnternet bağlantısını kontrol edip yeniden deneyin.

## Bir mağazayla eşleştirme (ve yeniden eşleştirme)

Mağaza Universal Till bulutunda zaten varsa ve bu kasanın ona katılması gerekiyorsa ya da bir kasa mağazanın bulut hesabından kaldırıldıysa veya yeniden eşleştirilmesi gerektiğini söylüyorsa bunu kullanın.

1. Mağazanın bulut hesabında «Kasa ekle veya yeniden eşleştir»i seçerek bir eşleştirme kodu alın (8 karakter, 15 dakika geçerli, tek kullanımlık).
2. Bu kasada Ayarlar → Kasa kaydı → Bir mağazayla eşleştir'i açın, kodu girin ve Eşleştir'e basın. Bir yönetici veya admin onaylar.
3. Kasa yalnızca kendi bulut bağlantısını değiştirir: yeni bir cihaz kimliği ve kendine ait bir kimlik bilgisi alır. Satışlar, ürünler ve ayarlar kasada kalır; satış bu süre boyunca çevrimdışı çalışmaya devam eder.
4. Katılmış bir kasada kod, ana kasasıyla aynı mağazadan gelmelidir; başka bir mağazanın kodu reddedilir ve hiçbir şey değişmez. Reddedilen veya süresi dolmuş bir koddan sonra kasa, yeni bir kodla eşleştirene kadar kayıtsız kalır.
