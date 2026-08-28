---
id: sell
title: Satış ve ödeme ekranı
section: Günlük satış
order: 10
summary: "Ana kasa ekranı: ürünleri okutun veya seçin, ödemeyi alın, fişi yazdırın ya da atlayın."
routes: [/, /ui/basket, /ui/buttons, /ui/held, /refund/{receipt}]
keywords: [kamera, masa, masalar]
---

# Satış ve ödeme ekranı

Ana kasa ekranı: ürünleri okutun veya seçin, ödemeyi alın, fişi yazdırın ya da atlayın. Tamamen çevrimdışı çalışır — internet kesintisi satışı asla engellemez.

## Nasıl kullanılır

1. Barkodu okutun veya satış ekranında ürünü bulup dokunun: kategori sekmeleri arasında geçiş yapın ya da geçerli sekmeyi ada göre filtrelemek için arama kutusuna yazın, ardından eklemek için ürün kartına dokunun.
2. Barkod okuyucunuz yok mu? Kameralı ve desteklenen bir tarayıcıya sahip bir cihazda, tarama kutusunun yanındaki 🔳 düğmesine dokunarak bunun yerine kamerayla tarayın — barkodu veya QR kodunu gösterin; ürün otomatik olarak eklenir ve kamera kapanır. Bu, takılı bir okuyucunun yerini almaz veya onu devre dışı bırakmaz, ve hiçbir fotoğraf ya da video cihazdan asla çıkmaz.
3. Sepette adedi değiştirin veya satırı silin, sonra Öde'ye geçin.
4. Sepeti beklet ile sıradaki müşteriye geçin, sonra geri çağırın; iadeler satış geçmişindedir.
5. Masalar kurduysanız (bkz. **Masalar ve kat planı**), geçerli sepeti sipariş türü düğmesinin altındaki masa seçiciden bir masaya atayın — kaldırmak için "Masa yok"u seçin. Bekletilen bir sipariş masasını korur ve bu, bekletilen siparişler şeridinde bir rozet olarak gösterilir; bekletilen bir siparişi kaybetmeden veya devam ettirmeden başka bir boş masaya taşımak için şerit üzerindeki **Masayı taşı**'ya dokunun.
6. Tamamlanmış bir satışı iade etmek için Journal → satış geçmişinden açın: hangi satırların ne kadarının iade edileceğini seçin (daha önce iade edilen miktar izlenir, satılandan fazlasını iade edemezsiniz), ardından nakit mi yoksa orijinal ödeme yöntemine mi iade edileceğini seçin.

## Almanya'daki mağazalar: TSE ve gerçek satışlar

Mağazanızın ülkesi Almanya ise ve bir yönetici mağazayı resmi kayıt moduna aldıysa (deneme değil, gerçek ve yasal olarak bağlayıcı satışların kaydedildiği mod), kasa her satışı **veya iadeyi** tamamlamadan önce mağazanın TSE'sini (teknik güvenlik birimi) denetler — bir iade de tıpkı bir satış gibi gerçek parayı hareket ettirdiği için aynı denetimden geçer, ayrı bir kural değildir:

- **Kurulu TSE yok:** satış veya iade, ekranda bir mesajla reddedilir. Bu durum için geçersiz kılma yoktur. Bunu düzeltmek için bir TSE kurun — kendi donanımınız, kendi bulut hesabınız veya yönetilen bir abonelik — ya da hazır olana kadar mağazayı resmi kayıt modundan çıkarması için bir yöneticiden isteyin.
- **TSE kurulu ama şu anda arızalı:** satışlar ve iadeler duraklatılır. Mağaza sahibi (yönetici) geçici bir geçersiz kılma verebilir — bunun için bir onay ifadesinin yazılması, bir gerekçe ve bir süre (en fazla 8 saat) gerekir. Geçersiz kılma etkinken satış ve iade ekranlarında bir şerit görünür, alınan her satış veya iade denetim kaydında işaretlenir ve fişte TSE'nin kullanılamadığı belirtilir. Süre dolduğunda satışlar ve iadeler yeniden otomatik olarak duraklar.
- Deneme ve tanıtım mağazaları (resmi kayıt modunda olmayanlar) bu denetimden hiçbir zaman etkilenmez.

Bu denetim yalnızca mağazanızın kasadaki kendi ayarlarına bakar — asla ağa bağlı değildir ve çevrimdışı olmak TSE arızası sayılmaz.

### Satış sırasında TSE imzalamaya ulaşılamazsa

Bir mali imzalama eklentisi kuruluysa, kasa ödeme anında her satışı imzalamasını ister (imzalama hizmeti yavaşsa bu birkaç saniye sürebilir). İmzalama hizmetine ulaşılamadığında — ya da kasa zaten çevrimdışı olduğunu biliyorsa — satış yine normal şekilde tamamlanır: ödeme hiçbir zaman ağ yüzünden engellenmez. Bunun yerine, eksiklik açıkça ve kalıcı olarak kaydedilir:

- satış denetim kaydında imzasız olarak işaretlenir,
- müşteri fişinde satışın TSE imzası olmadan kaydedildiğine dair bir not yer alır,
- ve kasanın sorun listesinde bir uyarı görünür.

Bu kalıcıdır, bekleyen bir kurtarma değildir: kasa, bir satış tamamlandıktan sonra onu imzalamayı yeniden denemez. TSE sağlayıcıları (Almanya'daki SIGN DE hizmeti için fiskaly'nin kendi kılavuzu dahil) bir işlemin sonradan imzalanmasına izin vermez, bu yüzden ödeme anında imzalanamayan bir satış, kasanın kendi kayıtlarında imzasız kalır — fişteki ve denetim kayıtındaki not, o satış hakkındaki son sözdür, değiştirilmeyi bekleyen bir yer tutucu değildir. Bir imzalama kesintisi satışı asla kendiliğinden durdurmaz — neden ağ olsun, imzalama hizmeti olsun, kasa satışları tamamlamaya ve eksikliği yukarıda açıklandığı gibi kaydetmeye devam eder. (Bu sayfada daha önce açıklanan duraklama ayrı bir güvenlik önlemidir; bir imzalama kesintisi bunu tetiklemez.)

### Hiç imzalama eklentisi yüklü değilse

Yukarıdaki iki durum, bir imzalama eklentisinin var olduğunu ama ulaşılamadığını veya bu satışı reddettiğini varsayar. Bir işletme, TSE yapılandırılmış ve sistem-kaydı modu açıkken de **hiçbir mali imzalama eklentisi yüklü olmadan** kalabilir — hiçbir şeyi imzalamak için abone olmuş hiçbir şey yoktur. Satışlar yine de tamamlanır (yukarıdaki kesinti durumundaki gibi aynı "devam et ve bildir" davranışı), ama bu tür bir eksiklik satıştan satışa fark edilmesi kolay bir durum değildir, bu yüzden **Ayarlar** bu durum sürdüğü sürece kalıcı bir uyarı gösterir — "Mali imzalayıcı yüklü değil" — ve doğrudan ülkenin vergi eklentisini yüklemek için Eklentiler sayfasına giden bir bağlantı sunar. Kapatılamaz: Ayarlar her açıldığında geçerli durumu kontrol eder ve bir imzalama eklentisi etkin olur olmaz kendiliğinden kaybolur.

### Hiç vergi oranı eklentisi yüklü değilse

İmzalamadan ayrı olarak, bazı ülkeler (bugün: Almanya) her satışa doğru salonda tüketim/paket servis KDV oranını uygulamak için bir eklentiye ihtiyaç duyar. Bu, imzalama sorunsuz çalışırken veya hiç TSE yapılandırılmamışken bile olabilir. İşletmenizin ülkesi buna ihtiyaç duyuyorsa ve şu anda etkin, çalışan bir vergi oranı eklentisi yoksa, **Ayarlar** kalıcı bir uyarı gösterir — "Vergi oranı eklentisi yüklü değil" — ve doğrudan Eklentiler sayfasına giden bir bağlantı sunar. Yukarıdaki mali imzalayıcı uyarısı gibi, bu da kapatılamaz: Ayarlar her açıldığında geçerli durumu kontrol eder ve çalışan bir vergi oranı eklentisi etkin olur olmaz kendiliğinden kaybolur. Bu sırada satışlara ne olacağı eklentinin neden yanıt vermediğine bağlıdır: hiç eklenti yüklü değilse satışlar yine de tamamlanır, siz bir tane yükleyene kadar sadece tek bir sabit oranla; eklenti yüklü ama bozuksa, cihaz ona ihtiyaç duyan satışları tamamlamayı reddeder — bozuk bir imzalama eklentisi için açıklanan aynı "hata durumunda reddet" davranışı.

### Mali imzalama durum rozeti

Mağazanızda bir TSE yapılandırılmışsa, üst gezinme çubuğundaki küçük bir rozet mali imzalamanın anlık durumunu gösterir: **✓ TSE imzalama sorunsuz**, ya da bu kasadaki en son satış TSE imzası olmadan tamamlandıysa **⚠ TSE imzalama kullanılamıyor**, bugün hâlâ imzasız kaç satış olduğuyla birlikte. Hiçbir TSE yapılandırılmamışsa rozet hiç görünmez — buna hiç ihtiyaç duymayacak bir mağaza için ne rozet ne de uyarı olur. Bu yalnızca bir durum göstergesidir, tamamen kasanın kendi imzaladıklarına dair kaydından beslenir — satış ekranını asla engellemez ve asla ağa erişime bağlı değildir.

### Bir satış hiçbir şekilde imzalanamıyorsa

Kesintiden ayrı olarak, imzalama eklentisi belirli bir satışın olduğu haliyle imzalanamayacağını bildirebilir — örneğin imzalama hizmetinin geçerli bir fiş kaydına dönüştüremediği bir bahşiş veya indirim. Bu, hizmete ulaşılamamasından farklıdır: bu, o tek satışa özgü bir durumdur. Satış yine normal şekilde tamamlanır ve kesintiyle aynı şeyler olur — denetim kaydında işaretlenir ve kasanın sorun listesinde bir uyarı görünür — ama müşteri fişindeki not farklıdır: TSE imzalamanın kullanılamadığını değil, satışın olduğu haliyle imzalanamadığını belirtir, böylece hiçbir zaman yaşanmamış bir bağlantı sorununu ima etmez. Kesintide olduğu gibi, bu kalıcıdır — kasa daha sonra satışı yeniden imzalamayı denemez.

### Fişlerdeki TSE imza bloğu

İmzalama eklentisi bir satışı imzalayıp imza ayrıntılarını döndürdüğünde, fişte eklentinin döndürdüğü imza ayrıntılarını gösteren bir "TSE imzası" bloğu yer alır: TSE seri numarası, işlem numarası, imza sayacı, işlem başlangıç ve bitiş zamanları, imza algoritması ve imzanın kendisi. Ekrandaki fiş bu ayrıntıları ayrıca QR kod olarak da gösterir (QR biçimi henüz geçicidir ve Almanya'daki kullanıma geçmeden önce sertifikalı bir TSE'ye karşı doğrulanacaktır); termal baskı ise metin satırları olarak yazar. Bu blok yalnızca imzalama hizmeti o satış için bu verileri gerçekten döndürdüğünde görünür — bloğu olmayan bir fiş, verinin sağlanmadığı anlamına gelir ve hiçbir alan asla yer tutucu değerlerle doldurulmaz.
