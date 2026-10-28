---
id: vouchers
title: Hediye çekleri
section: Günlük satış
order: 25
summary: Hediye çeklerini bir Hediye çeki ürününe dokunarak satın — kartı okutun ya da kasanın numara oluşturmasına izin verin — ve satış ekranında okutarak ya da ödeme panelinin Böl sekmesinde ödeme olarak alın.
keywords: [hediye çeki, hediye kartı, gutschein, belirli ürün, kullanma, bakiye, düzenleme, kod, kart numarası, ürün türü, düzelt, düzeltme, bakiye düzeltme, bakiye yükleme, yükle, değer ekle]
routes: [/vouchers, /vouchers/{id}]
---

# Hediye çekleri

Hediye çeki, müşterinin şimdi ödeyip daha sonra harcadığı paradır — dükkândaki her şey için, her kasada. Kasa her hediye çekinin bakiyesini **kart numarası** altında tutar: teslim ettiğiniz fiziksel kartın üzerinde basılı numara ya da numarayı boş bıraktığınızda kasanın oluşturduğu 13 haneli numara. Hediye çekini satış ekranında bir **Hediye çeki** ürününe dokunarak satarsınız; ödeme olarak ise okutarak ya da ödeme panelinin **Böl** sekmesinde alırsınız (sekmenin kendisi için bkz. [Ödemeler](/help/payments)).

## Hediye çeki satmak

Satış için katalogda **Türü** *Hediye çeki* olan bir ürün gerekir (bkz. [Katalog](/help/catalog)). Yeni bir işletmede satışa hazır bir tane zaten vardır: kurulum sırasında işletmenin dili yüklüyse *Hediye çeki*, değilse dil yüklenene kadar *Gift voucher* adını taşır, sonra siz kendiniz yeniden adlandırmadıysanız o dildeki adını alır (isterseniz katalogda yeniden adlandırın). Ardından bir katalog içe aktarması kendi hediye çeki ürününü getirirse, kurulumda oluşturulan ürün henüz hiç kullanılmadıysa kaldırılır ve içe aktarma özeti bunu belirtir. Bundan önce kurulmuş bir kasada bir kez oluşturun. Fiyatını boş bırakırsanız tutar her seferinde sorulur; bir fiyat verirseniz (örneğin *Hediye çeki 25 ₺*) pencere o tutarla açılır.

1. Satış ekranında hediye çeki ürününe dokunun (ya da barkodunu okutun). **Hediye çeki** penceresi açılır.
2. **Kart numarası**: teslim ettiğiniz kartı okutun — pencere açılır açılmaz alan okuyucunuza hazırdır — ya da numarasını yazın. Boş bırakırsanız **Ekle**'ye dokunduğunuzda kasa bir numara oluşturur.
3. **Tutar**: hediye çekinin değeri, normal bir para tutarı, örneğin `25,00`. Ürünün bir fiyatı varsa alan o fiyatla başlar.
4. **Kimin için** isteğe bağlıdır — hediye çekinin kimin için alındığını hatırlamak için bir ad.
5. **Kullanılabileceği yer**: **Her alışveriş** olağan hediye çekidir; **Belirli bir ürün (vergi şimdi alınır)** aşağıda anlatılır. **Ekle**'ye dokunun.
6. Hediye çeki, kart numarasını gösteren ayrı bir satır olarak sepete girer. Ödemeyi her zamanki gibi alın (**Ödeme**, nakit, kart, …). Fiş her hediye çekini **Düzenlenen hediye çekleri** altında numarası, kimin için olduğu ve basılı fişte ve ekranda barkod olarak numarasıyla listeler; böylece fiş sonradan okutulunca kart gibi kullanılır. Kasanın oluşturduğu bir numarayı müşteri bu fişte bulur; fişi verin ya da numarayı karta yazın.

Bir hediye çeki tek başına, sepette başka hiçbir şey olmadan ya da ürünlerle birlikte tek bir satışta satılabilir. Sepet satırı her zaman değeriyle tek bir hediye çekidir: adet düğmesi ve satır indirimi yoktur, bir kampanya ya da sepet indirimi onu asla düşürmez. Hediye çekinden vazgeçmek için satırdaki ✕'e dokunun — neden sorulmaz, çünkü dükkândan hiçbir şey çıkmamıştır. Hediye çeki satmak ürün geliri değildir ve stok hareketi yaratmaz: daha sonra harcayacak kişiye borçlu olunan para olarak kaydedilir ve ürünler hediye çeki harcandığında vergilendirilir — aşağıdaki gün sonu notlarına bakın. Bekletilen bir satış hediye çeki satırlarını korur ve hediye çeki satış ödendiğinde düzenlenir.

Neler ters gidebilir:

- **Ekle**, tutar sıfırın üzerine çıkana kadar soluk kalır.
- Zaten kullanımda olan bir kart numarası — daha önce satılmış ya da bu satışın başka bir satırında olan — hiçbir şey eklenmeden önce pencerede bunu belirten bir mesajla reddedilir. Başka bir kart okutun ya da numarayı boş bırakın.
- Tek bir satışta en fazla 50 hediye çeki satılabilir.

## Hediye çekini ödeme olarak almak

En hızlı yol: hediye çekinin barkodunu — kartı ya da fişte basılı numarayı — satış ekranında okutun. Kasa onu bulur ve ödeme panelinde, satışın hâlâ gerektirdiği tutarı aşmamak üzere bakiyesi için bir **Hediye çeki** düğmesi sunar. Numarayı yazmak ya da ödemeyi bölmek için:

1. **Böl** sekmesinde yöntem olarak **voucher**'ı seçin. Bir **Hediye çeki kodu** kutusu belirir ve Para üstü kutusu kaybolur — hediye çeki asla para üstü vermez.
2. Müşterinin kartındaki kodu yazın.
3. **Bakiyeyi kontrol et** (isteğe bağlı) hediye çekini arar ve tamamlamadan önce kalan bakiyesini gösterir. Tutar kutusu hâlâ boşsa doğal tutarı da doldurur — bakiyeyi ya da daha azsa satışta hâlâ ödenmesi gerekeni. Kasa şu anda ana kasaya ulaşamıyorsa kontrol yalnızca bunu söyler; ödemeyi yine de ekleyebilirsiniz ve asıl kontrol satış tamamlanırken yapılır.
4. Hediye çekinden alınacak **Tutar**'ı girin (en fazla bakiyesi kadar ve satışın hâlâ gerektirdiğinden fazla olmayacak şekilde), ardından **Ödeme ekle**. Kalanı başka bir yöntemle ödeyin ve **Satışı tamamla**'ya basın. Fiş, o ödemede hediye çeki kodunu gösterir.

Neler ters gidebilir:

- Kod kutusu boşken **Ödeme ekle** reddedilir.
- Kasanın tanımadığı bir kod ya da iptal edilmiş veya tamamen harcanmış bir hediye çeki reddedilir — kullanırsanız **Bakiyeyi kontrol et**'te, aksi hâlde satış tamamlanırken.
- Hediye çekinin bakiyesini aşan bir tutar reddedilir («Hediye çeki bakiyesi bu ödemeyi karşılamıyor») — hediye çekinde kalanı alın, gerisini başka bir yolla ödetin.
- Satışın hâlâ gerektirdiğinden fazla bir tutar da reddedilir, çünkü hediye çeki para üstü veremez — bunun yerine kalan tutarı girin.
- Bir hediye çeki her satışta bir kez kullanılabilir ve aynı satışta satılmakta olan bir hediye çeki o satışı ödeyemez.

Aynı listedeki eski **gift** yöntemi, bakiye takip etmeyen düz bir ödeme yöntemidir — kasanın düzenlediği hediye çekleri için **voucher** kullanın.

## Belirli bir ürün için hediye çekleri

Hediye çeklerinin çoğu **her amaç için**dir — müşteri neye harcayacağına sonra karar verir ve mallar hediye çeki harcandığında vergilendirilir. **Belirli bir ürün** için satılan bir hediye çeki (KDV oranı bu yüzden zaten bilinen belirli bir ürün ya da hizmet — Almanya'da *Einzweck-Gutschein*) ise satıldığında vergilendirilir ve daha sonra kullanılması yalnızca ürünün teslim edilmesidir. Kasa iki türü birbirinden ayrı tutar.

Satmak:

1. **Hediye çeki** penceresinde, **Kullanılabileceği yer** altından **Belirli bir ürün (vergi şimdi alınır)** seçeneğini seçin (varsayılan olan **Her alışveriş**, yukarıda anlatılan olağan hediye çekidir).
2. Hediye çeki, hediye çeki ürününün kendi vergi koduyla vergilendirilir — katalogda o ürüne ürünün oranını verin (örneğin %7 kodlu bir *Kahve hediye çeki* ürünü).
3. Kart numarasını, tutarı ve sahibini her zamanki gibi doldurun ve **Ekle**'ye dokunun. Sepet satırında *Belirli bir ürün (vergi şimdi alınır)* yazar. Satış KDV'yi o oranla hemen kaydeder; dolayısıyla hediye çeki, günün KDV dilimlerinde normal bir satış satırı gibi görünür — **GUTSCHEINE** borç bölümünde değil.

Kullanmak:

1. Ödeme panelinin **Böl** sekmesinde yöntem olarak **voucher**'ı seçin, kodu yazın ve **Bakiyeyi kontrol et**'e basın.
2. Belirli bir ürün için hediye çekinde kasa tutar doldurmaz — bunun yerine hediye çekinin değerini ve sahibini bir **Kullan** düğmesinin yanında gösterir. Böyle bir hediye çeki asla ödeme olarak kullanılamaz: zaten vergilendirilmiştir, ödeme olarak almak aynı parayı iki kez vergilendirirdi.
3. Ürünü teslim edin ve **Kullan**'a dokunun. Bu kesindir: hediye çeki hemen kullanılmış olarak işaretlenir, sonrasında tamamlanacak ya da kaldırılacak bir şey yoktur. Başka bir ödeme ya da vergi kaydedilmez — hediye çekini satan satış bunu zaten yapmıştır.

Neler ters gidebilir:

- Vergi kodu olmayan bir hediye çeki ürünü, dükkânın standart oranıyla vergilendirilir — önce ürüne doğru vergi kodunu verin.
- Zaten kullanılmış bir hediye çekinde **Kullan**'a dokunmak ya da satış ekranında belirli bir ürün için hediye çekini taratmak, bunu belirten bir mesajla reddedilir.
- Başka bir kasada satılan belirli bir ürün için hediye çeki bu kasadan kontrol edilebilir ama henüz burada kullanılamaz — onu ana kasada ya da satan kasada kullanın.

## Hediye çekleri sayfası

**Menü → Hediye çekleri** (yalnızca yönetici), bu kasanın bildiği her hediye çekini en yenisi önce listeler: kart numarası, kimin için olduğu, ilk değeri, üzerinde kalan tutar, durumu — **Etkin**, **Tamamen kullanıldı** veya **İptal** — ve neye harcanabileceği (belirli bir ürün için olan çek işaretlenir). **Satıldığı fiş**, çeki satan satışın fişine bağlantı verir; içe aktarılan bir bakiyede bunun yerine bu yazar.

1. Bir çeki kart numarasına veya sahibine göre bulmak için **Ara** kutusuna yazın — liste siz yazarken güncellenir. Yalnızca etkin, tamamen kullanılmış veya iptal edilmiş çekleri göstermek için **Durum**'u kullanın. Arama, aksanlı harfler dahil büyük ve küçük harf farkını yok sayar (**MÜLLER**, *Müller*'i bulur). Arama ve durum sayfa adresinde kalır; bu yüzden bir çekten Geri dönünce ya da sayfayı yenileyince korunurlar.
2. Çekin **Geçmiş**ini görmek için bir kart numarasına dokunun: ne zaman düzenlendiği, her kullanıldığı an ve her düzeltme, tutarı ve her satışın fişine bağlantısıyla.
3. **CSV dışa aktar**, geçerli arama ve durumla eşleşen her çeki indirir — yalnızca ekrandakileri değil, hepsini — `code`, `balance`, `label`, `original_amount`, `status`, `purpose` ve `created_at` sütunlarıyla. Dosya başka bir kasada aynı sayfanın altındaki **Kupon bakiyelerini içe aktar** ile içe aktarılabilir (bkz. [Kupon bakiyelerini içe aktar](/help/voucher-import)).

Bilmekte fayda var:

- Sayfa en yeni 200 çeki gösterir. Daha fazlası varsa bunu belirtir — daha eskileri bulmak için arayın veya filtreleyin.
- Henüz hiç çek yoksa sayfa nasıl başlayacağınızı söyler: bir Hediye çeki ürünüyle satın veya önceki sisteminizden bakiyeleri içe aktarın.
- Ek bir kasada liste yalnızca bu kasanın gördüğü çekleri gösterir — burada satılan, içe aktarılan veya kullanılan. Tam liste ana kasadadır. Başka bir yerde satılan bir çeki açmak için tam kart numarasını arayın: kasa onu ana kasaya sorar.

## Bakiyeyi düzeltmek

Bir hediye çekinin bakiyesi yanlışsa — değer satışta yanlış girildiyse ya da hasarlı bir kart değiştiriliyorsa — bir yönetici doğru bakiyeyi çekin kendi sayfasından girebilir. Para el değiştirmez: kasa düzeltmeyi, kimin yaptığını ve nedenini kaydeder.

1. Önce **Menü → Hediye çekleri**'ni açın ve çekin kart numarasına dokunun.
2. Burada **Bakiyeyi düzelt** altında **Mevcut bakiye**yi kontrol edin, ardından **Yeni bakiye**yi yazın — farkı değil, çekin bundan sonra sahip olması gereken tutarın tamamını.
3. Bir **Neden** yazın (zorunlu, en fazla 200 karakter), örneğin *satışta tutar yanlış girildi*.
4. Ardından **Düzeltmeyi kaydet**e dokunun. Sayfa **Bakiye düzeltildi.** mesajı, yeni bakiye ve **Geçmiş**te farkı (+ veya −), nedeni ve düzeltmeyi yapanı gösteren bir **Düzeltme** satırıyla yeniden yüklenir.

0'a yapılan bir düzeltme çeki **Tamamen kullanıldı** olarak işaretler; tamamen kullanılmış bir çekin bakiyesini artırmak onu yeniden **Etkin** yapar. Çekin ilk değeri hiç değişmez. Gün sonu (Z) raporu günün düzeltmelerini **GUTSCHEINE** bölümünde kendi **Adjusted** satırında listeler — asla satış, ödeme ya da KDV olarak değil.

Neler ters gidebilir:

- Bakiyeyi yalnızca bir yönetici veya admin düzeltebilir.
- İptal edilmiş bir çek ya da belirli bir ürün için verilen bir çek düzeltilemez — sayfa formu göstermek yerine nedenini söyler.
- "Yeni bakiye mevcut bakiyeyle aynı" — düzeltilecek bir şey yok.
- "Siz bakarken bu hediye çekinin bakiyesi değişti" — bu arada biri çeki harcadı ya da düzeltti. Sayfayı yenileyin, yeni bakiyeyi kontrol edin ve hâlâ gerekiyorsa yeniden düzeltin.
- Ek bir kasada düzeltme ana kasada yapılır. Ana kasaya ulaşılamıyorsa hiçbir şey değişmez ve sayfa bunu söyler — ana kasa geri geldiğinde tekrar deneyin. Sayfa ana kasanın düzeltmeyi onaylamadığını söylüyorsa düzeltme kaydedilmiş de olabilir, kaydedilmemiş de: tekrar denemeden önce sayfayı yenileyin ve hediye çekinin geçmişini kontrol edin.

## Hediye çekine bakiye yükleme

Bir müşteri elindeki hediye çekine değer ekleyebilir — kart numarasını korur ve bakiyesi artar. Bakiye yükleme, yeni bir hediye çeki satmak gibi bir satıştır: müşteri bunu kasada öder ve hediye çeki harcanana kadar hiçbir şey vergilendirilmez.

1. Önce **Menü → Hediye çekleri** sayfasını açın ve hediye çekinin kart numarasına dokunun.
2. Açılan sayfada **Bakiye yükle** altında **Eklenecek tutar** alanına tutarı yazın, örn. `20.00`, ve **Satışa ekle**'ye dokunun.
3. Satış ekranı, bu kart numarası için *Bakiye yükleme* olarak işaretlenmiş bir hediye çeki satırıyla açılır. Müşterinin aldığı diğer ürünleri ekleyin, sonra ödemeyi her zamanki gibi alın.
4. Satış ödendiğinde hediye çekinin bakiyesi bu tutar kadar artar. Fiş bunu **Düzenlenen hediye çekleri** altında kart numarası, eklenen tutar ve yeni bakiyeyle *Hediye çeki bakiye yükleme* olarak listeler; hediye çekinin **Geçmiş** bölümünde fişe bağlantılı bir **Bakiye yüklendi** satırı görünür.

Kullanılmış bir hediye çeki yeniden **Etkin** olur; ilk değeri hiç değişmez. Satış ödenene kadar hediye çekine hiçbir şey eklenmez — yüklemeyi kaldırmak için satırdaki ✕'e dokunun. Gün sonu (Z) raporu bakiye yüklemeyi **GUTSCHEINE** bölümünde satılan hediye çekleriyle birlikte sayar. Satışı iptal etmek tutarı hediye çekinden geri alır, ancak yalnızca müşteri onu harcamamışsa.

Neler ters gidebilir:

- Yalnızca bir yönetici veya admin hediye çekine bakiye yükleyebilir.
- İptal edilmiş bir hediye çekine veya belirli bir ürün için verilen hediye çekine bakiye yüklenemez — sayfa formu göstermek yerine nedenini söyler.
- Tutar sıfırdan büyük olmalıdır ve bir hediye çekinin bakiyesi kasanın tek hediye çeki sınırını aşamaz.
- Bir hediye çekine satış başına bir kez bakiye yüklenebilir ve hediye çeki kendi yüklemesini ödeyemez.
- Bakiye yükleme için katalogda etkin bir hediye çeki ürünü gerekir — yoksa sayfa bunu söyler.
- Yüklenen tutarın bir kısmı harcandıktan sonra satışı iptal etmek reddedilir.
- Ek bir kasada, bakiye yüklemesini eklerken ana kasaya ulaşılabilir (ve güncel) olmalıdır; değilse sayfa bunu söyler ve satışa hiçbir şey eklenmez.

## Gün sonu raporları ve diğer kasalar

Gün sonu (Z) raporunun, o gün satılan, harcanan ve düzeltilen hediye çekleri için kendi **GUTSCHEINE** bölümü vardır; hediye çeki satan bir satışı iptal etmek hediye çekini de (yalnızca kullanılmamışken) iptal eder — bkz. [Raporlar ve gün sonu](/help/reports). Bir kasada satılan hediye çeki dükkândaki başka herhangi bir kasada harcanabilir; bir kasa çevrimdışıyken bunun nasıl işlediği [Birden çok kasa (tek dükkân)](/help/multitill) sayfasında anlatılır.
