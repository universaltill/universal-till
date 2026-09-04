---
id: multitill
title: Birden çok kasa (tek dükkân)
section: Bağlantı ve eklentiler
order: 310
summary: "Aynı dükkân ağında birden çok kasa çalıştırın: biri ana kasadır, diğerleri ona katılır; katalog, fiyatlar, ayarlar ve stok otomatik paylaşılır."
routes: [/tills, /ui/tills/pending-pairings, /registers, /sync-quarantine]
---

# Birden çok kasa (tek dükkân)

Aynı dükkân ağında birden çok kasa çalıştırın: biri ana kasadır, diğerleri ona katılır; katalog, fiyatlar, ayarlar ve stok otomatik paylaşılır.

## Nasıl kullanılır

1. **Ana kasada** Ayarlar → Kasalar bölümüne gidin ve eşleştirme kodunu görüntüleyin. Bu kod, hem adresi hem de tek kullanımlık bir jetonu içeren bir metin bloğudur (ya da bir QR kodu).
2. **İkinci cihaza** kasa uygulamasını kurun. İlk kurulum ekranında **Mevcut bir dükkâna katıl**'ı seçin. Katılmanın iki yolu vardır: ana kasayı ağda otomatik bulmak ya da eşleştirme kodunu elle yapıştırmak.
3. İki sekmenin üstünde, bu kasaya bir kez ad verin — zorunludur; aramadan veya kod yapıştırmadan önce doldurmazsanız kutu kırmızıya döner ve bir mesaj gösterir. Sonra bir sekme seçin: **Bu ağda bul** (en kolayı): **Bu ağda bir birincil kasa bul** düğmesine basın, listeden ana kasayı seçin ve **Eşleştirme iste**'ye basın. Artık iki ekran da aynı 6 haneli kodu gösterir — eşleştiklerini kontrol edin, sonra isteği ana kasanın Kasalar sayfasından onaylayın (bunu bir yönetici yapar; bekleyen her isteğin PIN kutusu hangi cihaza ait olduğunu gösterir). Eşleştirme başarısız olursa neden ekranda kalır ve yeniden denemeniz yeterlidir — sayfayı yeniden yüklemeye gerek yoktur. Kimse onaylamazsa istek 10 dakika sonra zaman aşımına uğrar. Ya da **Eşleştirme kodunu yapıştır** sekmesi: 1. adımdaki eşleştirme kodunun **tamamını** Eşleştirme kodu kutusuna yapıştırın ve Katıl'a basın. Ana kasanın yalnızca adresi yeterli değildir — kaydı yetkilendiren tek kullanımlık jeton kodun içindedir. Verdiğiniz ad bu dükkânın ağında benzersiz olmalıdır: katılma, adın zaten kullanılıyor olması nedeniyle (ana kasa veya başka bir katılmış kasa tarafından) reddedilirse farklı bir ad seçip tekrar deneyin.
4. Bitmesini bekleyin. Dükkânın tamamı — katalog, fiyatlar, ayarlar, stok ve kullanıcılar — kopyalanır; yoğun bir ağda bu bir dakikayı bulabilir. Katıl'a iki kez basmayın: jeton tek kullanımlıktır ve ikinci basış "kod kullanılmış veya süresi dolmuş" hatasıyla başarısız olur.
5. Başarısız olursa nedeni düğmenin altında görünür. En sık nedenler: iki kasanın aynı ağda olmaması ve kodun yalnızca bir kısmının yapıştırılması.
6. Her kasadaki satış ana kasaya akar; katalog değişiklikleri yaklaşık yarım dakika içinde tüm kasalara yayılır.
7. Katılmış bir kasada Katalog ve Envanter salt okunurdur — üstteki bir şerit bunu belirtir ve sizi ana kasaya yönlendirir. Windows ve Mac'te bu şerit tıklanabilir bir bağlantıdır; kiosk uygulamasında ise düz metin olarak kalır, çünkü kiosk cihazının bir bağlantı onu başka bir kasanın ekranına götürürse geri dönecek bir yolu yoktur.
8. Stok konusunda karar yalnızca ana kasaya aittir. Katılmış bir kasa, stok yetersiz diye bir satışı asla reddetmez — kendi stok sayısı sadece birkaç saniye geride kalabilen bir kopyadır, bu yüzden satışa her zaman izin verir. Bu durum bir ürünün stoğunu gerçekten eksiye düşürürse (iki kasanın son adedi neredeyse aynı anda satması gibi), bu hiçbir satışı engellemek yerine ana kasanın panosunda bir sorun olarak görünür.
9. **Ana kasanın kendi** Kasalar sayfasında, Kayıtlı kasalar listesi katılmış kasaların yanında ana kasayı da ("bu kasa" etiketiyle) Ayarlar'da belirlenen adla gösterir — böylece tek kasalı bir dükkân da boş bir tablo yerine kendi kasasını görür.
10. **Katılmış** bir kasada, Kasalar sayfası aynı dükkân genelindeki listeyi gösterir — ana kasa ("birincil" etiketiyle) ve kendisi dahil diğer her katılmış kasa ("bu kasa" etiketiyle). Orada salt okunurdur: bir katılmış kasayı yalnızca ana kasa kaldırabilir ("iptal edebilir").
11. Sol menünün alt kısmına yakın küçük eşitleme rozeti bu kasanın kendi adını gösterir; hem ana kasada hem katılmış bir kasada tıklanabilir ve Kasalar sayfasını açar. Her menü düğmesi gibi düz bir rozettir — eşitleme durumu düğmenin rengini DEĞİŞTİRMEZ; bunun yerine dikkat gerektiren bir durum olduğunda (bir süredir haber alınamadı, ya da eşitlenmeyi bekleyen satışlar var) simgede küçük bir nokta belirir. Henüz hiçbir kasa katılmamış tek kasalı bir dükkânda rozet hiç görünmez — eşitlenecek bir şey yoktur.
   - **Ana kasada**, katılmış bir kasa uygulanamayan bir satış gönderirse (son derece nadir bir durum) rozet aynı noktayı alır ve bağlantısı Kasalar sayfası yerine karantina listesine gider. Bu satış sessizce kaybolmak yerine, ana kasadaki **Ayarlar → Karantinadaki eşitleme öğeleri** sayfasında incelenmek üzere tutulur.
12. Eklentiler de ana kasayı takip eder: bir eklentiyi **ana kasada** kurun veya kaldırın; her katılmış kasa aynı değişikliği yaklaşık yarım dakika içinde otomatik olarak uygular. Her kasa eklentiyi eklenti mağazasından kendisi indirir ve çalıştırmadan önce doğrular — eklentiler asla kasadan kasaya kopyalanmaz. Katılmış bir kasada doğrudan eklenti kurmaya, kaldırmaya, etkinleştirmeye/devre dışı bırakmaya veya güncellemeye çalışmak, sizi ana kasaya yönlendiren bir mesajla reddedilir. Tek istisna dosyadan içe aktarılan eklentidir: içe aktarma her kasada, katılmış bir kasada bile, çalışmaya devam eder; ancak eklenti yalnızca o kasada kalır ve yayılmaz.

## Kasalar

Bir **kasa**, vardiyanın açıldığı ve satışın işlendiği satış noktasıdır. Bir mağazada birden fazla kasa olabilir: örneğin aynı anda satış alan bir ön tezgah ve bir arka tezgah.

- Bir kasa oluşturmak için menüden **Kasalar**'ı açın (yalnızca yönetici). Ona bir ad verin ve isteğe bağlı olarak beslendiği stok konumunu seçin; bir yönetici daha sonra yeniden adlandırabilir, stok konumunu değiştirebilir veya devre dışı bırakabilir (devre dışı bırakmak satış/vardiya geçmişini korur — yalnızca yeni bir vardiya için sunulmaz). Stok konumunu değiştirmek geçmiş satışlarına veya vardiyalarına dokunmaz — bunlar kasanın kendisine bağlı kalır. Zaten vardiya/satış geçmişi olan bir kasa bu sayfada "Vardiya veya satış geçmişi var" ipucunu gösterir — yalnızca bilgilendirme amaçlıdır, devre dışı bırakmayı asla engellemez.
- İkinci bir kasanın katılması (yukarıya bakın) ona **otomatik olarak** bir kasa atar: kayıt sırasında ana kasa, katılan kasanın adını taşıyan yeni bir kasa oluşturur (ad zaten kullanılıyorsa "Till 2 (2)" gibi sayısal bir son ek eklenir) ve katılan kasa buna önceden ayarlanmış olarak gelir — manuel adım yoktur ve hemen kendi vardiyasını açabilir. Daha fazlası için manuel yol hâlâ durur: **Kasalar**'dan ek kasalar oluşturun (bir dükkânda kasa sayısı cihaz sayısından fazla olabilir), orada bir kasayı yeniden adlandırın veya bir cihazın kasasını o cihazda **Ayarlar → Kasalar** → **Bu cihazın kasası** üzerinden değiştirin.
