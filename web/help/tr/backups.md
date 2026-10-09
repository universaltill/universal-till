---
id: backups
title: Yedekler
section: İşi yürütme
order: 250
summary: Tüm dükkân verinizin (katalog, satışlar, ayarlar) anlık kopyaları; indirip güvenli bir yerde saklayabilirsiniz.
---

# Yedekler

Tüm dükkân verinizin (katalog, satışlar, ayarlar) anlık kopyaları; indirip güvenli bir yerde saklayabilirsiniz. Her yedek, ürünler ve kategoriler için yüklediğiniz fotoğrafları ve fiş logonuzu da içerir; indirdiğiniz tek dosya bunları da geri getirir.

## Nasıl kullanılır

1. Ayarlar → Yedekler: istediğiniz an yedek oluşturun. Fotoğrafların eklenemediğini söylüyorsa, yedek verilerinizi içerir ama fotoğraflarınızı içermez — kasayı silmeden veya değiştirmeden önce tekrar deneyin.
2. İndir, kopyayı İndirilenler klasörünüze kaydeder — bir kopyayı kasa dışında saklayın.

## Bir yedeği geri yükleme

Geri yükleme, tüm mevcut verinin seçilen yedekle değiştirilmesi demektir —
onaylamak için yönetici PIN'inizi girin, çünkü bu, ayarlar sayfasının kendisinden
geri alınamaz (değiştirilen veri, ihtiyaç duyarsanız diye kendi yedeği
olarak saklanır). Geri yükledikten sonra **Şimdi yeniden başlat**'a
tıklayın; kasa kendini yeniden başlatır — klavyeye ya da fişi çekmeye
gerek yok. Windows'ta kasa henüz kendini yeniden başlatamaz — bunun
yerine pencereyi kapatıp Universal Till'i yeniden açın.

## Otomatik temizlik

Kasa, diski dolmasın diye günde bir kez artık gerekmeyen dosyaları siler:

- Yedekler: en yeni 14 yedek tutulur.
- Bir yedeği geri yüklerken kenara ayrılan veri kopyası: satış kayıtlarını yasal olarak saklamanız gereken süre boyunca tutulur; bu süre dolduktan sonra yalnızca en yeni 3 kopya tutulur, hiçbiri 30 günden eski olmaz.
- Gönderilemeyen sorun bildirimleri: 7 gün sonra silinir.
- İndirilen güncellemeler: 7 gün sonra silinir.
- Kasa bir yükleme veya içe aktarma sırasında kapandığı için geride kalan yüklenmiş dosyalar: 24 saat sonra silinir.

Bu temizlik satışları, fişleri, denetim kaydını, Z raporlarını veya yasal olarak saklamanız gereken başka hiçbir kaydı asla silmez. Her kasa yalnızca kendi diskini temizler.

## Kasayı bir Linux makinesinden kaldırma

Kasa `.deb` paketinden kurulduysa, kaldırmak için terminalde `sudo
unitill-uninstall` komutunu çalıştırın. Önce dükkân verinizin doğrulanmış
bir yedeğini oluşturur (ev klasörünüze kaydedilir), sonra verilerin
korunup korunmayacağını sorar — korursanız, ileride yeniden kurulum
kaldığınız yerden devam eder. Verileri silmek için `DELETE` yazmanız
gerekir; yanlışlıkla gerçekleşemez.
