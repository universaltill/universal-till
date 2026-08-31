---
id: order-status
title: Sipariş durumu (mutfak ilerlemesi)
section: Günlük satış
order: 45
summary: "Her siparişin ilerlemesini — hazırlanıyor, hazır, teslim edildi — tek dokunuşla işaretleyin; herkes siparişin nerede olduğunu görsün."
routes: [/orders]
---

# Sipariş durumu (mutfak ilerlemesi)

Her siparişin ilerlemesini — hazırlanıyor, hazır, teslim edildi — tek dokunuşla işaretleyin; herkes siparişin nerede olduğunu görsün.

## Nasıl kullanılır

1. Menüden **Siparişler**'i açın. Son satışlar en yenisi başta olacak şekilde, her biri güncel durumuyla listelenir.
2. Mutfak siparişe başladığında **Hazırlanıyor**'a, sipariş alınabilir olduğunda **Hazır**'a, müşteri aldığında **Teslim edildi**'ye dokunun. Bunu her kasiyer yapabilir — yönetici PIN'i gerekmez.
3. Her değişiklik, kimin ne zaman yaptığını durumun yanına kaydeder. Fiş numarası, İşlem geçmişindeki tam fişe götürür — başka bir kasada alındığı için burada listelenen bir sipariş hariç (aşağıdaki nota bakın): bu kasa o fişi kendi kaydında tutmadığından numara görünür ama bağlantı değildir.
4. **Siparişi iptal et** siparişi iptal eder. Teslim edilene kadar her aşamada çalışır; teslim edilmiş bir sipariş iptal edilemez.
5. Sipariş durumu yalnızca ileri gider. Yanlışlıkla önceki bir adıma dokunmak (ya da çevrimdışı kalmış ikinci bir kasanın eski bir durumu bildirmesi) hiçbir şeyi değiştirmez — kasa daha yeni durumu göstermeye devam eder. Bu bilinçli bir davranıştır, arıza değildir.
6. Bu özellikten önce yapılmış (ya da henüz kimsenin dokunmadığı) satışların siparişleri **Başlamadı** olarak görünür — biri bir duruma dokunana kadar onlar için hiçbir şey değişmez.

## Notlar

- Liste birkaç saniyede bir kendini yeniler — yeni siparişler (self-servis kiosktan verilenler dahil) sayfa yenilenmeden görünür.
- Bir mutfak fişi ya da fiş yazdırılamazsa — kâğıdı biten, fişi çekilmiş ya da kapalı bir yazıcı — siparişin durumunun yanında ⚠ uyarısı görünür; böylece ödenmiş bir sipariş (örneğin self-servis kiosktan gelen) asla sessizce kaybolmaz. Mutfak ⚠ uyarısı, fişin mutfağa hiç ulaşmadığı anlamına gelir: yazıcıyı düzeltin ve siparişi mutfağa kendiniz iletin. Fiş ⚠ uyarısı ise, yazıcıyı düzeltip İşlem geçmişinde siparişin sayfasından fişi yeniden yazdırdığınız anda kaybolur.
- Birden fazla kasa birbirine bağlıyken, ana kasaya erişilebildiği sürece her kasa tüm dükkânın siparişlerini aynı liste üzerinden görür ve günceller — yalnızca siparişi alan kasa değil. Ana kasaya ulaşamayan bir kasa kendi yerel listesiyle çalışmaya devam eder ve bağlantı geri gelir gelmez ortak listeyi yeniden gösterir — ama bağlantı kesikken yapılan bir durum değişikliği başka hiçbir yere gönderilmez: o kasa yeniden bağlanır bağlanmaz ekranı ortak listeye döner ve çevrimdışıyken yapılan dokunuş o ekrandan gözle görülür şekilde kaybolabilir (gerçekten çevrimdışı bir kasada "Hazır"a dokunan bir yönetici, kasa yeniden bağlandığında bunu bir daha kontrol etmeli). Yeni bir sipariş de — onu alan kasada bile — ana kasayla senkronizasyonu tamamlanana kadar ortak listede görünmez; bu genelde birkaç saniye sürer.
- Buradaki her şey, kasanın geri kalanı gibi tamamen çevrimdışı çalışır.
- Bu ekran, sıradaki özelliklerin temelidir: mutfak ekranı, müşteri çağrı cihazları ve sipariş takibi aynı durumları kullanacak.
