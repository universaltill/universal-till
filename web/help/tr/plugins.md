---
id: plugins
title: Eklenti mağazası
section: Bağlantı ve eklentiler
order: 330
summary: "Çekirdek uygulamayı değiştirmeden özellik ekleyin: ödemeler, temalar, dil paketleri, entegrasyonlar, yapay zekâ araçları ve daha fazlası."
routes: [/plugins, /plugins/store, /plugins/{id}/settings]
---

# Eklenti mağazası

Çekirdek uygulamayı değiştirmeden özellik ekleyin: ödemeler, temalar, dil paketleri, entegrasyonlar, yapay zekâ araçları ve daha fazlası. Her eklenti çalışmadan önce imzalanır ve doğrulanır.

## Nasıl kullanılır

1. Kataloğa göz atmak için Eklentiler → Mağaza'yı açın.
2. Tek tıkla kurun; eklentiler güven rozetleri taşır (altın = resmî Universal Till, yeşil = doğrulanmış geliştirici) ve doğrulanmamış yayıncılar önce onayınızı ister.
3. Her eklentinin kendi ayar sayfası vardır; bazı ayarlar dükkân genelinde, bazıları kasa başınadır.
4. Kendi kılavuzuyla gelen kurulu bir eklenti, kartında bir Kılavuz düğmesi gösterir; bu düğme kılavuzu doğrudan kasanın içinde açar.
5. Birden çok kasalı bir dükkânda eklentileri yalnızca **ana kasada** kurun ve kaldırın: her katılmış kasa aynı eklentiyi mağazadan kendisi alır ve değişikliği yaklaşık yarım dakika içinde otomatik olarak uygular. Katılmış bir kasadaki kurma, kaldırma, etkinleştirme/devre dışı bırakma ve güncelleme denemeleri, sizi ana kasaya yönlendiren bir mesajla reddedilir. Dosyadan içe aktarılan eklentiler istisnadır — içe aktarma her kasada, katılmış olanlar dahil, çalışmaya devam eder; ancak eklenti yalnızca içe aktarıldığı kasada kalır ve diğerlerine kopyalanmaz.
6. Eklentiler sayfasında bir eklentide kırmızı **Bozuk ⚠** rozeti görünüyorsa, dosyaları o kasada eksik veya okunamıyor demektir (bu, bir kasa dükkâna katıldıktan hemen sonra olabilir). Nasıl düzeleceği eklentinin nereden geldiğine bağlıdır: mağazadan kurulan bir eklenti yaklaşık yarım dakika içinde otomatik olarak yeniden yüklenir — bir şey yapmanız gerekmez. Dosyadan içe aktarılan bir eklentinin ise yeniden alınabileceği bir mağaza kaydı yoktur, bu yüzden kendiliğinden **onarılmaz** — düzeltmek için eklenti dosyasını o kasada yeniden içe aktarın. Eklenti düzelene kadar, vergisini o eklentinin belirlediği ürünler o kasada satılamaz: sepette yalnızca bir ürün etkilendiyse satış ekranı hangi ürün olduğunu söyler ve onu kaldırınca satışın kalanı tamamlanabilir; her ürün etkilendiyse (örneğin bozuk eklenti kasanın tek vergi eklentisiyse) eklenti düzelene kadar o kasada ödeme yapılamaz — biraz bekleyip tekrar deneyin.
