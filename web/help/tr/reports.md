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
