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
bir tutar, örn. bozukluk takviyesi) buna gerek duymaz.

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

## Counting the drawer at close: skim & new float

The opening cash for a new shift is **carried over automatically** from
the register's last close — whatever the previous close left in the
drawer is pre-filled, so you confirm it rather than re-type it. You can
still edit the figure if the drawer was corrected in between; whatever
you submit is what's recorded.

When you close a shift, count the drawer and enter the counted cash as
before. Two optional extras join it:

- **Skim to safe** — the amount you move from the drawer to the safe as
  part of the close. The counted cash minus the skim becomes the drawer's
  **new float**, which is what the next shift on that register opens with.
  A skim can't exceed the counted cash, and it never changes the expected
  figure — the variance always compares your count against takings
  *before* the skim, so moving money to the safe can't hide a shortage.
  An optional reason can be recorded with it.
- **Denomination count** — an optional per-denomination count (how many
  of each coin and note) stored with the close as a count protocol, for
  shops that want the till count documented piece by piece. Leave it
  empty to skip it entirely.

## Cash reconciliation on the day-end report

The printed end-of-day (Z) report gains a **CASH RECONCILIATION**
section on any day at least one shift was closed: opening float, cash
sales, tips held out (only printed on a day that actually has a cash
tip), pay-ins, pay-outs, calculated (what should be in the drawers),
counted (what was in them), variance, skim to safe, and the new float
carried to the next day. Cash sales excludes any cash tip the same way
tips are already held out of revenue elsewhere on the report — that is
why the "Tips held out" line sits between cash sales and pay-ins:
opening float + cash sales + tips held out + pay-ins + pay-outs together
equal calculated, so the section's own figures still add up once cash
tipping is in use, not just on an ordinary no-tip day. Skim to safe is
entered as part of closing the shift, after calculated is already fixed
for the day, which is why it is listed below variance rather than folded
into that sum. A non-zero variance is flagged with
`!!` on the printout, and the Day-end tab marks that day's row with a
warning tag so a discrepancy is visible on screen without reprinting
each period. A day with no closed shift still produces a complete
report — the section is simply absent, and running End of day is never
blocked on closing a shift.

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
