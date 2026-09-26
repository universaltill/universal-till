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

1. Raporlar'ı açın: üstteki satır seçili dönemin ana rakamlarını (ciro, satış, ortalama satış, vergi, indirimler, iadeler, net, geçen yıl) ve düşük stok uyarısını her zaman gösterir. Dönemi üstündeki çiplerle seçin — Bugün, Dün, Bu hafta veya Bu ay; Özel seçeneği son günler, tam bir yıl veya başka bir tarih için önceki denetimleri korur. Çipler iş günü başlangıcınızı izler; geç bir kapanıştan sonra "Bugün" hâlâ içinde bulunduğunuz iş günüdür.
2. Altındaki sekmelerden birini seçin — Satış eğilimi, Ürünler, Vergi, Tahmin, Ödemeler ve kanallar, Bahşişler veya Gün sonu (EOD) — o rapor yalnızca sekmeyi açtığınızda çalışır.
3. Kapanışta Gün sonu'nu (Gün sonu sekmesinde) çalıştırın: günü toplar ve kayıtlarınız için yazdırabilir.

## Rapor dönemleri

Üstteki satırın yanında, dönemin nasıl hesaplanacağını seçin:

- **Özel** — orijinal kayan pencere (bugün, şimdiden geriye 7/14/30/90 gün).
- **Gün / Hafta / Ay / Yıl** — kayan bir sayım yerine gerçek bir takvim dönemi: Gün bir işletme günü, Hafta Pazartesi-Pazar, Ay bir takvim ayı, Yıl bir takvim yılıdır. Seçimin yanında bir tarih seçici görünür, böylece geçmiş bir dönemi görebilirsiniz — örneğin Ay'ı seçip Temmuz'da bir tarih seçerek, bugün Ağustos olsa bile Temmuz'un rakamlarını görebilirsiniz.
- Satış eğilimi, Ürünler, Vergi ve Ödemeler ve kanallar seçili dönemi kullanır, böylece her zaman yukarıdaki rakamlarla uyuşur. Tahmin ve Gün sonu arşiv listesi ise kullanmaz — bunlar dönem seçiciden bağımsız olarak kendi sabit pencerelerini gösterir.

## İşletme günü başlangıcı

Varsayılan olarak bir rapor "günü" gece yarısından gece yarısına kadar sürer. Gece yarısından sonra da satış yapıyorsanız — bir bar, geç saatlere kadar açık bir mutfak — bu, bir gecelik hasılatı iki rapor gününe böler. "İşletme günü şu saatte başlar" ayarını (Gün sonu sekmesinde, otomatik gün sonu saatinin yanında) işletme gününüzün gerçekten başladığı saate, örneğin 06:00'ya ayarlayın; böylece Gün/Hafta/Ay/Yıl dönemleri saatin değil gerçek işletme gününüzün sınırlarını izler.

Bu kaydırma, Satış eğilimi sekmesindeki en yoğun saat grafiğine de uygulanır — gece yarısından hemen sonra yapılan bir satış, gerçek saati yerine gece yarısından önceki bir saat etiketinin altında (örneğin 22:00) görünebilir; bu, Gün/Hafta/Ay/Yıl'ın bu satışı zaten önceki işletme gününe ait saymasıyla tutarlıdır.

## Gün sonu (Z) raporunda hediye çekleri

Mağazanız çok amaçlı hediye çeki satıyor veya kabul ediyorsa, yazdırılan
gün sonu raporunda ayrı bir **GUTSCHEINE** bölümü görünür: o gün kaç çekin
satıldığı ve kullanıldığı, ve tutarları. Çek satışı, ürün geliri olarak
değil, çekin gelecekteki sahibine borçlanılan para olarak kaydedilir — bu
yüzden tutar günün genel hasılatında görünür ama bölüm bazlı veya vergi
oranı bazlı ürün rakamlarına asla girmez. Malların vergisi, çek daha sonra
harcandığında, o malların kendi oranlarıyla kaydedilir — müşteri nakit
ödemiş gibi. Bölüm yalnızca çek hareketi olan günlerde yazdırılır.

Bir çekin satıldığı satış iptal edilirse, çek henüz hiç kullanılmamışsa
çek de onunla birlikte iptal edilir — rapordan kaybolur ve artık
harcanamaz. Çekin herhangi bir kısmı zaten harcanmışsa kasa o satışın
iptalini reddeder: önce bekleyen çeki müşteriyle çözüme kavuşturun.

## İptaller ve kapanışı kim yaptı

Basılı gün sonu (Z) raporu, en az bir satışın iptal edildiği her günde,
İadeler'den ayrı bir **STORNOS** (iptaller) bölümü gösterir: buradaki bir
iptal, sonradan iptal edilen/geri alınan tamamlanmış bir satış anlamına
gelir (örn. aynı gün yapılan bir düzeltme), oysa iade sonradan işlenen
resmi bir geri ödemedir. Bu ikisi bir denetçi için farklı şeyler ifade
eder, bu yüzden asla tek bir rakamda birleştirilmezler — bir satışı iptal
etmek zaten hiçbir hasılat taşımaz ve günün Net tutarını hiçbir zaman
değiştirmez. Hiç iptal olmayan bir günde bölüm tamamen bulunmaz.

Rapor ayrıca her zaman **Erstellt von** (kapanışı kim yaptı) satırını
yazdırır — kişinin görünen adı, ya da otomatik zamanlanmış kapanış için
"System" — böylece Denetim sayfasını açmadan bile günü kimin kapattığının
bir kaydı olur. Şu anda isteğe bir `annotation` değeri gönderen herhangi
bir şey, kapanışa isteğe bağlı bir not ekleyebilir; bunun için henüz
ekranda bir alan yok, bu yüzden bu esas olarak bir entegrasyon veya
gelecekteki bir kasa güncellemesi için yararlıdır — mevcut olduğunda
Erstellt von'un hemen altında **Anmerkung** (not) olarak yazdırılır.

## Ürün grubu, ürün ve kasiyer bazında dökümler

Gün sonu raporunu tek bir gün için (tarih aralığı değil) çalıştırmak,
hem yazdırılan raporda hem de arşivlenmiş rapor listesinde ekranda üç
döküm daha ekler: **ürün grubuna göre** (her ürünün kendi kategorisine
göre gelir — "Telefonlar" gibi bir alt kategori, departman dökümünde
olduğu gibi üst departmanına dahil edilmek yerine burada kendi satırında
gösterilir), **ürüne göre** (o gün satılan her ürün, sadece en çok/en az
satanlar değil — yoğun bir gün onlarca satır listeleyebileceğinden
ekranda katlanmış olarak gösterilir), ve **kasiyere göre** (kasiyer
başına gelir ve satış sayısı). Bir yönetici bir satış üzerinde onay
verse bile gelir her zaman satışı gerçekten gerçekleştiren kasiyere
atfedilmeye devam eder, böylece kasiyer dökümü her zaman kasada
gerçekte kimin çalıştığını yansıtır.

Ekrandaki ürün listesi her zaman o gün satılan tüm ürünleri gösterir,
sayıları ne olursa olsun. Ancak **basılı** gün sonu (Z) raporunun
"ürüne göre" bölümü yapılandırılabilir (Gün sonu sekmesi, otomatik
kapanış zamanlamasının yanında) — çünkü çok sayıda üründen oluşan bir
mağaza her kapanışta yüzlerce fazladan satır yazdırabilir: **Gelire göre
ilk N'i yazdır** (varsayılan — ilk 30 ürünü, ya da belirlediğiniz
sayıyı, daha fazlası varsa bir "+N more" satırıyla birlikte yazdırır),
**Her ürünü yazdır** (önceki davranış, sınırsız) ya da **Yazdırma** (bu
bölümü yalnızca basılı rapordan kaldırır, ekrandaki rapordan değil).

## Sipariş türüne göre döküm

Tek bir gün için Gün sonu işlemini çalıştırmak, hem basılı raporda hem de
arşivlenmiş rapor listesinde ekranda bir **BY ORDER TYPE** (sipariş
türüne göre) dökümü de ekler: hasılat ve adet, satış ekranının kendi
burada/paket anahtarındaki etiketlerle eşleşen **Dine in** (Burada) ve
**Takeaway** (Paket) arasında bölünür. Her ikisini de içeren bir satış
(bazı ürünler burada, bazıları paket) üçüncü bir "karma" satıra ihtiyaç
duymadan kendi ürünlerine göre iki satıra doğru şekilde bölünür. Yukarıdaki
ürün grubu/ürün/kasiyer dökümleri gibi, bir satır yalnızca o modda en az
bir satış olduğunda görünür — tamamen burada geçen bir gün yalnızca Dine
in satırını gösterir, boş bir Takeaway satırı göstermez.

## Türkiye için mali cihaz (ÖKC) durumu

Mali cihaz eklentisi aktif olan Türkiye pazarındaki mağazalarda, hem
basılı gün sonu raporunda hem de arşivlenmiş rapor listesinde ekranda,
o kapanışın kapsadığı aynı dönem için ek bir **Mali cihaz (ÖKC)** bloğu
görünür: yeni nesil ödeme kaydedici cihazın o dönem için kendi üreticisi,
seri numarası ve Z rapor numarası (numaraları), fiş türüne göre sayıları
(mali fiş, iade fişi, bilgi fişi ve bastığı başka bir tür varsa) ve
kasanın kendi kayıtlarına göre cihaz üzerinden alınan tahsilat sayısı.
Cihazın dönem ortasında Z kapanışı yaptığı bir dönemde birden fazla Z
numarası görünür ve günün dönem ortasında kapandığına dair bir not
eklenir. Blok, sadece renkle değil metin ve bir simgeyle gösterilen
sade bir eşleşme/uyuşmazlık satırıyla biter — böylece cihazın kaydettiği
ile kasanın onun için kaydettiği arasındaki bir tutarsızlık, cihazın
kendi basılı Z raporunu elle karşılaştırmaya gerek kalmadan görülebilir.
Diğer tüm pazarların gün sonu raporu bundan etkilenmez: bu blok yalnızca
bu eklentisi açık olan bir Türkiye kasasında görünür.

## Gün sonu (Z) raporunda ödeme yöntemi ve KDV oranı bir arada

Yazdırılan gün sonu raporunda **BY METHOD & VAT RATE** tablosu bulunur:
günün hasılatı aynı anda hem ödeme yöntemine hem KDV oranına göre ayrılır
— her kombinasyon için bir satır (ör. %7'de nakit, %19'da kart) ve her
satırda net, vergi ve brüt tutar. Bu, muhasebecinin muhasebe yazılımına
işlediği tablodur: para hangi ödeme yöntemiyle geldi, hangi KDV oranına
karşılık. Satırlar her zaman günün KDV oranı bazlı toplamlarına tam olarak
denk gelir. Bir satış birden fazla yöntemle ödendiyse, tutarları her
yöntemin ödediği payla orantılı olarak bölünür. Bahşişler buraya dahil
edilmez — bahşişin KDV'si yoktur — bu yüzden bir kartın satır toplamı, o
kartın hasılat satırından tam olarak günün kart bahşişleri kadar az
olabilir; o gün bir kart iadesi varsa daha da az olur (iade kart hasılat
satırını da azalttığı için ikisi birbiriyle uyumlu kalır).

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

Aynı Ayarlar → Veri yönetimi listesinden bir sıfırlama arşivi grubu da
**kalıcı olarak silinebilir**, ama yalnızca yeterince eskiyse: gerçek
satış içeren bir grup, arşivlendiğinden bu yana mağazanızın ülkesinin
saklama süresi (Ülke ayarları sayfasında belirlenir) geçene kadar
korunur — daha erken silme reddedilir ve mesaj, grubun ne zaman
silinebilir hale geleceğini belirtir. Hiç satış içermeyen bir grup
(sıfırlandığında henüz hiçbir şey satılmamıştı) hemen silinir — kasa
vardiyaları gibi başka test verileri içerse bile; koruma özellikle satış
kayıtlarıyla ilgilidir. Bu, yukarıdaki 10 yıllık rapor saklamasından
ayrıdır ve onu değiştirmez.

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
bir tutar, örn. bozukluk takviyesi) buna gerek duymaz. Çıkan tutarı negatif
girin (ör. 50 birimlik bir çıkış için "-50") — fiziksel klavyesi olmayan
dokunmatik bir kasada önce ekran klavyesinin "-" tuşuna dokunun. Vardiya tutarları (açılış ve kapanış nakdi, kasadan alma, düzeltme) ondalık kısmından önce nokta veya virgülle yazılabilir ("-3.50" veya "-3,50").

Resmi kayıt modundaki bir Alman mağazasında, nakdi azaltan bir düzeltme —
ve bir Pfandrückgabe çıkışı — bir satışın veya iadenin geçtiği aynı TSE
denetiminden geçer; bu yüzden kurulu bir TSE yokken veya TSE arızalıyken
reddedilir. Bu mesajın ne anlama geldiğini ve mağaza sahibinin nasıl geçici
bir geçersiz kılma verebileceğini **Satış** → "Almanya'daki mağazalar: TSE
ve gerçek satışlar" bölümünde bulabilirsiniz. Nakit eklemek bundan hiçbir
zaman etkilenmez.

Depozito iadesi (Pfandrückgabe) çıkışı **bu cihazın kendi kasasının** açık
vardiyasına kaydedilir — aynı anda başka bir kasanın vardiyası açık olsa
bile asla diğer çekmeceye düşmez. Birden fazla kasası olan bir mağazada
cihazın önce hangi kasa olduğunu bilmesi gerekir: Ayarlar → Kasalar'da
"Bu cihazın kasası"nı ayarlayın; yoksa çıkış, sizi oraya yönlendiren bir
mesajla reddedilir.

Raporlar'daki Ödemeler ve kanallar sekmesi, seçili dönem için bir "Nedene
göre nakit düzeltmeleri" dökümü gösterir — örneğin o dönemdeki tüm
"Pfandrückgabe" çıkışlarının toplamı — böylece Denetim sayfasını açmadan
"bu hafta ödenen toplam depozito iadesi" gibi bir rakam görebilirsiniz. Bu
bölüm yalnızca dönem içinde en az bir düzeltme olduğunda görünür.

## Kapanışta çekmece sayımı: çelik kasaya çekim ve yeni bakiye

Yeni bir vardiyanın açılış nakdi, kasanın son kapanışından **otomatik
olarak devralınır** — önceki kapanışta çekmecede kalan tutar önceden
doldurulur, böylece yeniden yazmak yerine yalnızca onaylarsınız. Arada
çekmece düzeltildiyse rakamı yine de düzenleyebilirsiniz; ne gönderirseniz
o kaydedilir.

Bir vardiyayı kapatırken çekmeceyi sayın ve sayılan nakdi eskisi gibi
girin. Buna iki isteğe bağlı ek eklenir:

- **Çelik kasaya çekim** — kapanışın bir parçası olarak çekmeceden çelik
  kasaya taşıdığınız tutar. Sayılan nakit eksi çekim, çekmecenin **yeni
  bakiyesini** oluşturur; bu da o kasadaki bir sonraki vardiyanın hangi
  tutarla açılacağıdır. Çekim, sayılan nakdi aşamaz ve beklenen tutarı
  asla değiştirmez — fark her zaman sayımınızı çekimden *önceki*
  hasılatla karşılaştırır, böylece parayı çelik kasaya taşımak bir açığı
  gizleyemez. İsteğe bağlı bir gerekçe de kaydedilebilir.
- **Kupür sayımı** — kapanışla birlikte bir sayım tutanağı olarak
  saklanan, isteğe bağlı kupür bazlı sayım (her bozuk para ve banknottan
  kaç adet olduğu); kasa sayımını kupür kupür belgelemek isteyen
  mağazalar içindir. Tamamen atlamak için boş bırakın.

## Gün sonu (Z) raporunda nakit mutabakatı

En az bir vardiyanın kapatıldığı her günde, basılı gün sonu (Z) raporuna
bir **CASH RECONCILIATION** (nakit mutabakatı) bölümü eklenir: açılış bakiyesi, nakit satışlar,
ayrılan bahşişler (yalnızca o gün gerçekten nakit bahşiş varsa basılır),
nakit girişleri, nakit çıkışları, beklenen (çekmecelerde olması gereken),
sayılan (çekmecelerde bulunan), fark, çelik kasaya çekim ve ertesi güne
devreden yeni bakiye. Nakit satışlar, bahşişlerin raporun başka yerlerinde
zaten hasılattan ayrı tutulmasıyla aynı şekilde, hiçbir nakit bahşişi
içermez — "Tips held out" (ayrılan bahşişler) satırının nakit satışlar ile nakit girişleri
arasında yer almasının nedeni de budur: açılış bakiyesi + nakit satışlar +
ayrılan bahşişler + nakit girişleri + nakit çıkışları toplamı beklenen
tutara eşittir, böylece nakit bahşiş kullanıldığında da, sıradan bahşişsiz
bir günde olduğu gibi, bölümün kendi rakamları tutar. Çelik kasaya çekim,
vardiya kapatılırken, o gün için beklenen tutar zaten kesinleşmişken
girilir; bu yüzden o toplama katılmak yerine farkın altında listelenir.
Sıfırdan farklı bir fark, çıktıda `!!` ile işaretlenir ve Gün sonu sekmesi,
her dönemi yeniden yazdırmaya gerek kalmadan ekranda görülebilmesi için o
günün satırını bir uyarı etiketiyle işaretler. Kapatılmış vardiyası
olmayan bir gün yine de eksiksiz bir rapor üretir — bölüm yalnızca
bulunmaz ve Gün sonu işlemi bir vardiyanın kapatılmasına asla bağlı
değildir.

## Tüm kasaların satışlarını görme (İşlem geçmişi)

İşlem geçmişi sayfası (fiş/eşitleme listesi, satış ekranının dışında)
varsayılan olarak tüm kasaların satışlarını, en yeniden en eskiye, hangi
kasanın hangi siparişi aldığını kendi sütununda göstererek listeler —
böylece tek bir cihaz her kasaya gitmeden tüm mağazanın hasılatını
inceleyebilir. Yalnızca bu cihazın kendi satışlarını görmek için seçimi
"Bu kasa"ya değiştirin.

Listenin üstündeki filtre satırını kullanın:

- **Kasa** — "Tüm kasalar" (varsayılan) tüm kasaların satışlarını, en
  yeniden en eskiye, gösterir; "Bu kasa" listeyi yalnızca bu cihazın kendi
  yerel satışlarıyla sınırlar; ya da belirli bir kasayı adıyla seçip
  yalnızca onun satışlarını görebilirsiniz.
- **Gün** — listeyi o takvim gününe daraltmak için bir tarih seçin; günden
  bağımsız en son satışları görmek için boş bırakın.

"Bu kasa" dışında bir kasa kayıtlıysa, filtrelerin altındaki bir satır
her kayıtlı kasanın adını ve bu cihazla en son ne zaman bağlantı
kurduğunu gösterir ("*kasa*'dan son bağlantı: *zaman*") — bağlantısı
düzgün çalışan bir kasanın satış eşitlemesi yine de arka planda bozulmuş
olabilir, bu yüzden bu yalnızca bir ağ bağlantısı sinyalidir, satışlarının
gerçekten ulaştığının kanıtı değildir; eşitlemesi gecikmiş bir kasayı fark
etmek için kullanışlıdır, toplamları uzlaştırmanın yerine geçmez. Bir kasa
hiç bağlantı kurmadıysa, ya da (bir yedek kasada) bağlantı zamanı ana
kasadan paylaşılmadığı için burada "—" gösterilir.

Bu görünüm yalnızca mağazanın ana kasasında işe yarar, çünkü diğer
kasaların satışlarını yalnızca o biriktirir; bir yedek (replica) kasa,
hangi kasa filtresi seçilirse seçilsin, her zaman yalnızca kendi
satışlarını gösterebilir — çünkü bir yedek kasa kendi satışlarını yalnızca
tek yönlü olarak ana kasaya gönderir ve diğer kasaların satışlarını asla
geri almaz. Bir yedek kasada "Tüm kasalar" ya da belirli başka bir kasa
seçmek, neden boş kaldığını açıklamayan bir tablo yerine, kasalar arası
satışların yalnızca mağazanın ana kasasında kullanılabildiğini açıklayan
bir mesaj gösterir.

## Çalışanlara bahşiş ve servis ücreti ödemeleri (Bahşişler sekmesi)

**Bahşişler** sekmesi, bahşiş ve servis ücretlerinin çalışanlara nasıl
ödendiğini kaydeder ve bunu raporlar — işverenlerin tutmasını isteyen
Birleşik Krallık'ın Bahşiş Dağıtımı Yasası'nın (Employment (Allocation of
Tips) Act 2023) kayıt tutma gerekliliğinin bir parçası. Yalnızca bir
yöneticinin girdiğini kaydeder: yazılım kendiliğinden hiçbir parayı tespit
etmez veya taşımaz.

- **Alınan / dağıtılan** — seçili dönem için iki toplam: ne geldi (tamamlanan
  satışlardaki bahşişler, ya da tahsil edildiyse servis ücreti) ve bir
  çalışana ödendiği kaydedilen ne. Bu ikisi farklı saatlerde işler — bugün
  alınan para bir sonraki vardiyaya kadar ödenmemiş olabilir — bu yüzden
  kısa bir pencerede iki rakamın uyuşmaması normaldir, tek başına bir sorun
  değildir; her ikisini de kapsayacak kadar geniş bir pencerede kontrol
  edin.
- **Ödeme kaydetme** — bir yönetici ("Çalışan ödemeleri" yetkisiyle)
  çalışanı, paranın gerçekten ödendiği tarihi, türünü (bahşiş ya da servis
  ücreti), tutarı ve isteğe bağlı bir not seçip gönderir. Tarih gelecekte
  olamaz — bu zaten gerçekleşmiş bir ödemeyi kaydeder.
- **Bir çalışanın kendi kayıtları** — toplamları ve ödeme listesini tek bir
  kişiyle sınırlamak için Çalışan filtresini kullanın, örneğin talep
  üzerine bir çalışana kendisine ödendiği kaydedileni göstermek için.
- **Dışa aktarma** — bir tarih aralığı (isteğe bağlı olarak tek bir çalışan)
  için ödeme kayıtlarını CSV dosyası olarak indirin; bir çalışana, bir
  muhasebeciye ya da yalnızca toplamlar değil ayrıntılı kayıtlara ihtiyaç
  duyan başka birine vermek için.
- Ödeme kayıtları, mağazanın diğer mali kayıtlarıyla birlikte tutulur ve
  erken silinmez — bu sayfadaki diğer her şeyle aynı saklama süresi
  (yukarıdaki Rapor saklama bölümüne bakın).
- **Yazdırılan Gün sonu (Z) raporunda** — en az bir ödemenin bahşiş
  kaydettiği her gün için rapor, ödeme yöntemine göre kısa bir bahşiş
  satırı da yazdırır (örn. "4x Card £3.20") — en sık, kart terminalinin
  kendi bahşiş isteminin kullanıldığı bir ödeme. Bu, günün satış
  toplamlarından ayrı tutulur, gelir sayılmaz. Yukarıdaki "Alınan"
  rakamından farklı okunabilir: Z-raporu satırı, bahşişin kime ait
  olduğuna bakmaksızın bahşişli her ödemeyi sayar; "Alınan" ise yalnızca
  çalışana kaydedilen bahşişleri sayar (varsayılan) — bir bahşiş
  işletmeye kaydedildiğinde ikisinin farklı çıkması beklenir.

## Yüzde usulü havuz dağıtımı (Türkiye)

Bu **Yüzde usulü** sekmesi, Türkiye'de yüzde usulü havuz uygulayan
işyerleri içindir (İş Kanunu 4857 md. 51): tahsil edilip personel arasında paylaşılan
bir yüzde. Yalnızca işyerinin ülkesi Türkiye olarak ayarlandığında görünür.
Bahşişler sekmesi gibi, yalnızca bir yöneticinin girdiğini kaydeder —
yazılım kendiliğinden hiçbir parayı tespit etmez, hesaplamaz veya taşımaz;
işyerinizin havuz uygulayıp uygulamayacağına ya da hangi yüzdeyle
uygulayacağına da karar vermez.

- **Müşterinin adisyonunda satır yok** — bu kasa, adisyona bir Türkiye
  servis ücreti satırı eklemez; bu yüzden havuz tahsilatı ayrıca, bir
  yönetici tarafından kaydedilir ve hiçbir fişte görünmez.
- **Tahsilat kaydetme** — bir yönetici ("Çalışan ödemeleri" yetkisiyle)
  paranın tahsil edildiği tarihi, tutarı ve havuzun nasıl paylaşılacağını
  anlatan isteğe bağlı bir dağıtım esası notunu ("mutfak %30 / servis %70")
  seçip gönderir. Tarih gelecekte olamaz — bu, halihazırda alınmış parayı
  kaydeder.
- **Dağıtım kaydetme** — paranın çıktığı tahsilatı, çalışanı, paranın
  gerçekten ödendiği tarihi, tutarı ve isteğe bağlı bir notu seçin. Her
  dağıtım, bir tahsilattan tek bir çalışanın payıdır; dolayısıyla dört
  kişiye ödenen bir havuz dört kayıt eder.
- **Toplanan ve dağıtılan** — seçili dönem için iki toplam: havuzda
  toplanan ve bundan adı belirtilen çalışanlara ödendiği kaydedilen.
  Bunlar iki ayrı kayıttır, dolayısıyla farklı olabilirler — bugün
  toplanıp hafta sonunda ödenen bir havuz, o ödemeler kaydedilene kadar
  toplanmış ama henüz dağıtılmamış olarak okunur. Bir fark tek başına
  sorun değildir; her ikisini de kapsayacak kadar geniş bir pencerede
  kontrol edin.
- **Bir çalışanın kendi kayıtları** — dağıtım listesini tek bir kişiyle
  sınırlamak için Çalışan filtresini kullanın, örneğin kendisine ödendiği
  kaydedileni ona göstermek için.
- **Dışa aktarma** — Bahşişler sekmesindeki CSV dışa aktarmanın aynısı;
  havuz dağıtımlarını bahşiş ve servis ücreti kayıtlarıyla birlikte içerir,
  bir çalışana, bir muhasebeciye ya da ayrıntılı kayıtlara ihtiyaç duyan
  başka birine vermek için.
- Havuz kayıtları, işyerinin diğer mali kayıtlarıyla birlikte tutulur ve
  erken silinmez — bu sayfadaki diğer her şeyle aynı saklama süresi
  (yukarıdaki Rapor saklama bölümüne bakın).

## Kart ödemesi mutabakatı (fiş detayı)

İşlem geçmişinden bir fişi açmak tam ödeme detayını gösterir — bir kart
ödemesinin satıştan günler sonra sonradan mutabakatının yapıldığı yer
burasıdır. Bir ödeme kart okutmalı bir terminalde alındığında, ödeme
satırı maskelenmiş kart numarasını ve onay kodunu (basılı fişin tahsilat
anında gösterdiği aynı mutabakat satırı) gösterir, ayrıca terminalin
kendi mutabakat raporuyla eşleştirmek için terminal ve işlem numarasını
da gösterir. Bu alanlar yalnızca bir ödeme yöntemi bunları gerçekten
kaydettiğinde görünür — bugünkü yerleşik ödeme yöntemleri (nakit, Stripe,
SumUp, QR ödeme) kaydetmiyor, bu yüzden mevcut fişler bundan etkilenmez.

