---
id: my-reports
title: Raporlarım
section: Bağlantı ve eklentiler
order: 361
summary: "Bu kasadan gönderilen sorun raporlarını (en son 100 tanesi) bilinen son durumlarıyla görün — çevrimdışı da çalışır."
routes: [/my-reports]
---

# Raporlarım

Bu kasadan gönderilen sorun raporlarını (en son 100 tanesi) bilinen son durumlarıyla görün — çevrimdışı da çalışır.

## Sayfa neyi gösterir

Her satır bu kasanın kaydettiği bir rapordur — gönderilmiş olsun ya da olmasın: ne zaman kaydedildiği, ne içerdiği (yazdığınız not, ayrıca sesli not, ekran kaydı veya ekran görüntüleri için etiketler) ve güncel durumu. Bir rapor GitHub'da bir kayda dönüştüğünde yanında **GitHub'da görüntüle** bağlantısı belirir.

Durumların anlamı:

- **Burada kaydedildi, gönderim bekleniyor** — bu kasada kaydedildi, henüz yüklenmedi (mağaza çevrimdışıyken normaldir).
- **Gönderilemedi** — bu rapor bir süredir yüklenemiyor; altında kısa bir neden görünür (örneğin bu kasanın kaydının tamamlanması gerekiyor). Yine de kayıtlıdır ve kasa otomatik olarak yeniden denemeye devam eder — hiçbir şey kaybolmaz.
- **Gönderildi, inceleme bekliyor** — bu kasadan yüklendi; bulut henüz bir gelişme bildirmedi.
- **Alındı / Yazıya dökülüyor / İncelemeye hazır** — rapor işleniyor (sesli notlar otomatik olarak yazıya dökülür).
- **GitHub'a kaydedildi** — takip edilen bir kayda dönüştü; ilerlemeyi görmek için bağlantıyı izleyin.
- **Kapatıldı** — incelendi ve kayıt açılmadan kapatıldı.

## Çevrimdışı

Bu sayfanın internete hiç ihtiyacı yoktur: her zaman mağazanın son çevrimiçi olduğu andaki durumları gösterir ve bağlantı geri geldiğinde bunları arka planda otomatik olarak yeniler. Az önce kaydettiğiniz bir rapor, yüklendikten kısa süre sonra burada görünür.

Sayfayı bildirim panelindeki **Raporlarımı görüntüle** bağlantısından açın (üst çubuktaki 🐞 düğmesi — Sorun bildirme konusuna bakın).
