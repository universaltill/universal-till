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
2. Kaydolmadan önce "Universal Till bulut hizmeti şartlarını kabul ediyorum (sürüm v0.1-draft)" kutusunu işaretleyin; "Şartları oku" şartları yeni bir sekmede açar. İşaret yoksa Şimdi kaydet hiçbir şey yapmaz ve bunu size söyler. Şartları her sürüm için bir kez kabul edersiniz: sonrasında kart kutu yerine tarihle birlikte "Şartlar kabul edildi: sürüm v0.1-draft" gösterir ve kasa kabulünüzü (sürümü ve dilinizi) kayıtla birlikte gönderir. Bu adımdan önce (ya da bir eklenti kurulumuyla) kaydedilmiş bir kasa, Kasa kaydı altında aynı kutuyu **Şartları kabul et** düğmesiyle gösterir: kutuyu işaretleyip düğmeye basın. Kasa çevrimdışıysa kabulünüz kasada saklanır ve kart henüz buluta ulaşmadığını söyler; tekrar çevrimiçi olunca **Buluta gönder**'e basın. Satış hiçbir zaman etkilenmez.
3. Bu mağazayı sahiplen'e tıklayıp kısa kodu (15 dakika geçerli) ve QR'ı alın — telefonunuzla tarayıp oradan sahiplenin.
4. Universal Till kimliğinizle marketplace'e giriş yapın, sahiplenme sayfasını açın ve kodu girin.

## Otomatik kayıt — kurulumda sizin seçiminiz

Kurulum sihirbazının son ekranı, kasayı hemen kaydetmek isteyip istemediğinizi bir kez sorar — siz işaretlemedikçe işaretli değildir. Aynı seçenek Ayarlar → Kasa kaydı altında "Bu kasayı pazaryerine otomatik olarak kaydet" olarak da bulunur:

1. Açarsanız kasa; cihaz kimliğini, mağaza adını, mağaza ülkesini, mağaza bölgesini ve yazılım sürümünü (destek, güncellemeler ve lisanslama için kullanılır) Universal Till bulut pazaryerine gönderir ve hemen kaydolur. Adresiniz gönderilmez. O an çevrimdışı mı? Hiçbir şey beklemez, kurulum yine de tamamlanır — ama kasa, eklenti mağazasını açana veya Şimdi kaydol'a basana kadar kayıtsız kalır; yeniden çevrimiçi olduğunuzda Ayarlar → Kasa kaydı bölümünü kontrol edin. Seçenek işaretliyken şartlar kutusu altında görünür; seçeneği işaretleyip şartları işaretlemezseniz seçenek kapalı olarak kaydedilir ve hiçbir şey gönderilmez. Ayarlar'da açmak aynı işareti gerektirir (daha önce kabul etmediyseniz ve kasa henüz kayıtlı değilse); kapatmak hiçbir zaman gerektirmez.
2. Kapatırsanız kasa yalnızca eklenti mağazasını ilk kullandığınızda veya Şimdi kaydol'a bastığınızda kaydolmaya döner. Kapatmak, daha önce yapılmış bir kaydı asla kaldırmaz.

## Abonelik

Ayarlar → Abonelik (yalnızca yöneticiler) planınızı bu kasanın Universal Till bulutundan en son aldığı haliyle, hangi tarihe kadar ödendiğini ve en son ne zaman doğrulandığını gösterir. Kasada satış, fiş ve raporlar her planda ücretsizdir ve abonelik yüzünden asla durmaz.

- Yerel (ücretsiz): yapılacak bir şey yok, kart yalnızca ücretsiz planda olduğunuzu söyler.
- Etkin: planınız, ödendiği son tarih ve son doğrulama.
- Abonelik doğrulanmadı: kasa planınızı 7 günden uzun süredir bulutla doğrulayamadı, bu yüzden ücretli özellikler (bulut eşitleme, ürünleri tarayıcıdan yönetme ve yönetilen TSE kurulumu gibi) duraklatıldı. Kasanın internet bağlantısını kontrol edin; bağlandığında kendiliğinden düzelir.
- Abonelik sona erdi: aboneliğiniz artık etkin değil, bu yüzden ücretli özellikler Universal Till hesabınızdan yenileyene kadar duraklatılır. Planın son ödemesinden sonraki 90 gün boyunca, aboneliği sona ermiş bir kasa saatte bir kendiliğinden bulutla bağlantı kurar ve yenilemeyi genellikle bir saat içinde öğrenir. Hemen görmek için bu kartta ya da Ayarlar → Kasa kaydı bölümündeki duraklatılan özellikler uyarısında “Şimdi kontrol et” düğmesine basın (yalnızca yöneticiler ve admin'ler): kasa buluta sorar ve yanıtı yerinde gösterir; ya planın artık hangi tarihe kadar ödendiğini ya da henüz yenilenmediğini. Ana kasa üzerinden kayıtlı bir kasada düğmeye ana kasada basın: bu kasa yenilemeyi oradan alır.

Son iki durumda durum çubuğundaki bir işaret aşağıda anlatılan paneli açar; Kasa kaydı ve Fiş imzalama (TSE) kartları da hangi özelliklerinin duraklatıldığını yerinde gösterir. Hiçbir şey gizlenmez ve önceden kurulmuş bir TSE fişleri imzalamaya devam eder.

**Planı yenile veya değiştir** (yalnızca yöneticiler ve idareciler) bu kartta, Kasa kaydı'ndaki duraklatılan özellikler uyarısında ve durum çubuğundaki işarette bulunur. Bulunduğunuz ekranın üzerinde bir panel açar. Kasa önce buluttan güncel planınızı sorar: plan zaten yenilendiyse (örneğin Universal Till hesabınızdan), panel hangi tarihe kadar ödendiğini hemen gösterir; başka bir plan isterseniz Planı değiştir de sunulur. Aksi halde Mevcut planı yenile veya Plan seç'i seçin ve QR kodunu telefonunuzla tarayın: ödeme kasada değil, telefonunuzda yapılır. Kod gösterildiği sürece kasa her 20 saniyede bir, en fazla 10 dakika boyunca kontrol eder ve yeni planınız gelir gelmez ona geçer. Kapat (veya Escape) sizi tam olarak bulunduğunuz yere geri götürür. Ana kasa üzerinden kaydedilmiş bir kasa, yenilemeyi ana kasadan alır.

## Bulut eşitlemesi ücretli planlara dahildir

Kayıt ve sahiplenme ücretsizdir, eklenti mağazası da öyle. Kasayı çevrimiçi hesabınızla eşitlenmiş tutmak (satış rakamları, ürün listesi, my.universaltill.com üzerinde yapılan değişiklikler) ücretli planlara dahildir. Mağaza ücretli bir planda değilken kasa bulutla eşitleme yapmaz ve Ayarlar → Kasa kaydı bulut eşitlemesinin kapalı olduğunu söyler. Kasa buluta yalnızca kısaca bilgi verir: kurulduğunda, bir güncellemeden sonra ve her gün ilk açıldığında.

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

- **Bu kasayı kaydetmek için Universal Till bulut hizmeti şartlarını kabul edin**: şartlar kutusu işaretli değildi, bu yüzden hiçbir şey gönderilmedi. Kutuyu işaretleyin (veya otomatik kaydı kutu işaretliyken açın) ve yeniden deneyin.
- **Universal Till bulut hizmeti şartlarını kabul etmek için kutuyu işaretleyin**: Zaten kayıtlı bir kasada kutu işaretlenmeden Şartları kabul et'e basıldı, bu yüzden hiçbir şey kaydedilmedi veya gönderilmedi. Kutuyu işaretleyip Şartları kabul et'e yeniden basın.
- **Buluta ulaşılamadı. Kabulünüz bu kasada kayıtlı**: Kabul kasada saklanır; internet bağlantısını kontrol edip Buluta gönder'e yeniden basın.
- **Universal Till bulutu bu mağaza için kullanılamıyor**: bulut bu mağazaya hizmet vermiyor. Katılmış bir kasada bu mesaj ana kasadan gelir. Kasa çevrimdışı çalışmaya devam eder.
- **Bu kasada bulut adresi ayarlanmamış**: kasa bulut adresi olmadan kuruldu, bu yüzden kaydolamıyor. Kurulumu yapan kişiden bir adres ayarlamasını isteyin.
- **Başka bir kayıt denemesi hâlâ sürüyor**: kasa zaten arka planda kaydoluyor. Biraz bekleyip yeniden deneyin.
- **Önce mağazanıza bir ad verin**: mağazanın henüz gerçek bir adı yok (boş ya da "My Store" gibi varsayılan bir ad), bu yüzden kasa onu buluta kaydetmedi. Mesajdaki **Dükkân adı** bağlantısını izleyin, mağazanın adını kaydedin, ardından Şimdi kaydol düğmesine yeniden basın.
- **Kayıt başarısız** veya **Sahiplenme kodu alınamadı**: kasa buluta ulaşamadı. İnternet bağlantısını kontrol edip yeniden deneyin.

## Bir mağazayla eşleştirme (ve yeniden eşleştirme)

Mağaza Universal Till bulutunda zaten varsa ve bu kasanın ona katılması gerekiyorsa ya da bir kasa mağazanın bulut hesabından kaldırıldıysa veya yeniden eşleştirilmesi gerektiğini söylüyorsa bunu kullanın.

1. Mağazanın bulut hesabında «Kasa ekle veya yeniden eşleştir»i seçerek bir eşleştirme kodu alın (8 karakter, 15 dakika geçerli, tek kullanımlık).
2. Bu kasada Ayarlar → Kasa kaydı → Bir mağazayla eşleştir'i açın, kodu girin ve Eşleştir'e basın. Bir yönetici veya admin onaylar.
3. Kasa yalnızca kendi bulut bağlantısını değiştirir: yeni bir cihaz kimliği ve kendine ait bir kimlik bilgisi alır. Satışlar, ürünler ve ayarlar kasada kalır; satış bu süre boyunca çevrimdışı çalışmaya devam eder.
4. Katılmış bir kasada kod, ana kasasıyla aynı mağazadan gelmelidir; başka bir mağazanın kodu reddedilir ve hiçbir şey değişmez. Reddedilen veya süresi dolmuş bir koddan sonra kasa, yeni bir kodla eşleştirene kadar kayıtsız kalır.

## Mağazanın para birimi

Bulut hesabında (my.) oluşturulan bir mağaza para birimini ülkesinden alır. Böyle bir mağazanın ana kasası henüz hiçbir satışı tamamlamamış veya iptal etmemişse, bir sonraki bulut eşitlemesinde kendiliğinden mağazanın para birimine geçer (devam eden veya park edilmiş bir satış varken bekler) ve bu geçiş denetim kaydına yazılır. Katılmış kasalar ana kasayı izler.

Bir kasada satış olduktan sonra para birimini asla kendiliğinden değiştirmez: bu, kaydedilmiş her tutarı yeniden etiketlerdi. Kendi para birimiyle satmaya devam eder, bir yönetici durum çubuğunda **Para birimi mağazadan farklı** çipini görür (iki para birimini görmek için üzerine gelin) ve bulut hesabı bu kasa için aynı sorunu listeler. Ayarlar → Para birimi'ni açmak için çipe dokunun ve kayıtlarınız için güvenli olduğunda orada değiştirin. İkisi eşleştiği anda çip kaybolur. Bu kasa sürümünün tanımadığı bir para birimine asla geçilmez; kasa güncellenene kadar aynı şekilde gösterilir.
