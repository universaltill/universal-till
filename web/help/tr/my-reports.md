---
id: my-reports
title: Raporlarım
section: Bağlantı ve eklentiler
order: 361
summary: "Bu kasanın kaydettiği sorun raporlarını — gönderilmiş ya da gönderim bekleyen (en son 100 tanesi) — bilinen son durumlarıyla görün. Çevrimdışı da çalışır."
routes: [/my-reports]
keywords: [bug, issue, report, status, sent, pending, github, tracking]
---

# Raporlarım

Bu kasanın kaydettiği sorun raporlarını — gönderilmiş ya da gönderim bekleyen (en son 100 tanesi) — bilinen son durumlarıyla görün. Çevrimdışı da çalışır.

## Sayfa neyi gösterir

Her satır bu kasanın kaydettiği bir rapordur — gönderilmiş olsun ya da olmasın: ne zaman kaydedildiği, ne içerdiği (yazdığınız not, ayrıca sesli not, ekran kaydı veya ekran görüntüleri için etiketler) ve güncel durumu. Bir rapor GitHub'da bir kayda dönüştüğünde yanında **GitHub'da görüntüle** bağlantısı belirir.

Bu kasa 100'den fazla rapor gönderdiyse, girişin altında kaç tanesinin gösterilmediğini belirten bir satır görünür — daha eski raporlar GitHub'a kaydedildikçe veya kapatıldıkça bunlar tekrar görünür hâle gelir.

Durumların anlamı:

- **Burada kaydedildi, gönderim bekleniyor** — bu kasada kaydedildi, henüz yüklenmedi (mağaza çevrimdışıyken normaldir).
- **Gönderilemedi** — bu rapor bir süredir yüklenemiyor; altında kısa bir neden görünür (örneğin bu kasanın kaydının tamamlanması gerekiyor). Yine de kayıtlıdır ve kasa otomatik olarak yeniden denemeye devam eder — hiçbir şey kaybolmaz.
- **Gönderildi, inceleme bekliyor** — bu kasadan yüklendi; bulut henüz bir gelişme bildirmedi.
- **Alındı / Yazıya dökülüyor / İncelemeye hazır** — rapor işleniyor (sesli notlar otomatik olarak yazıya dökülür).
- **GitHub'a kaydedildi** — takip edilen bir kayda dönüştü; ilerlemeyi görmek için bağlantıyı izleyin.
- **Kapatıldı** — incelendi ve kayıt açılmadan kapatıldı.

## Çevrimdışı

Bu sayfanın internete hiç ihtiyacı yoktur: her zaman mağazanın son çevrimiçi olduğu andaki durumları gösterir ve bağlantı geri geldiğinde bunları arka planda otomatik olarak yeniler. Az önce kaydettiğiniz bir rapor burada hemen "gönderim bekliyor" olarak görünür — gerçekten yüklendiğinde "Gönderildi, inceleme bekliyor" durumuna geçer.

Eğer bu kasa bir raporun kendi kopyasını kaydedemezse — örneğin depolaması dolmuşsa — yeniden denemeye devam eder, ama sadece bir süre. Birkaç başarısız denemeden sonra raporu yerel olarak hatırlamayı bırakır. Raporun kendisi her halükarda desteğe ulaşır; sadece burada listelenmez.

Sayfayı bildirim panelindeki **Raporlarımı görüntüle** bağlantısından açın (yan menüdeki 🐞 düğmesi — Sorun bildirme konusuna bakın).
