---
id: power-supply
title: Güç kaynağı uyarısı
section: Bağlantı ve eklentiler
order: 355
summary: Raspberry Pi'de, güç kaynağı dokunmatik ekran ve diğer USB çevre birimleri için yeterli akım sağlayamadığında durum çubuğu uyarır.
---

# Güç kaynağı uyarısı

Raspberry Pi tabanlı bir kasada, yetersiz güç kaynağı USB çevre birimlerine — dokunmatik ekran dahil — ayrılan akımı kısıtlar ve bu da düzensiz davranışa (kaçan dokunuşlar, ara sıra donmalar) yol açabilir. Cihaz bunu zaten kendisi tespit eder, ama masaüstü arayüzü olmayan kiosk modunda çalışan bir mağaza bu uyarıyı normalde hiç göremez.

## Nasıl kullanılır

1. Durum çubuğunda **"Güç kaynağı yetersiz"** görünüyorsa, kasa güç kaynağının yeterli akımı güvenilir şekilde sağlayamadığını tespit etmiştir.
2. Bu kart için resmi güç kaynağıyla değiştirin (Raspberry Pi 5 özellikle resmi 27 W USB-C güç kaynağına ihtiyaç duyar — konektörü uysa bile daha düşük güçlü bir USB-C şarj cihazı yeterli değildir).
3. Uyarı, kasa uygun bir güç kaynağıyla yeniden başlatıldığında kaybolur.

Bu yerel, çevrimdışı bir kontroldür — kasanın internete bağlı olmasına hiçbir zaman bağlı değildir ve asla satışı engellemez.
