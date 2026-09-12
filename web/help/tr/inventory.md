---
id: inventory
title: Stok ve envanter
section: Dükkanı kurma
order: 120
summary: Ürün ve varyant bazında eldeki miktarı izler.
routes: [/inventory, /locations, /ui/inventory/stock-table]
---

# Stok ve envanter

Ürün ve varyant bazında eldeki miktarı izler. Satışlar stoğu otomatik düşer; mal kabul ve düzeltmeler teslimatları ve sayım farklarını kaydeder.

## Nasıl kullanılır

1. Güncel stok seviyeleri için Envanter'i açın.
2. O ürünle önceden doldurulmuş mal kabul/düzeltme penceresini açmak için bir stok satırına — veya arama kutusunun yanındaki **+** düğmesine — dokunun. Teslimatı mal kabul ile girin; fire, kırılma veya sayım farkı için düzeltme kullanın — stoktan düşmek için sayıyı negatif girin (dokunmatik kasada önce ekran klavyesinin "-" tuşuna dokunun).
3. Envanter sayfası stokun kaç gün yeteceğini tahmin eder ve ne kadar sipariş verileceğini önerir; raporlar sayfasında da düşük stok uyarısı görünür.
4. Stok konumları (Konumlar, yalnızca yönetici) mağaza geneli kalemlerdir ve her zaman **ana kasadan** yönetilir: katılmış bir kasada bir konum oluşturmak, yeniden adlandırmak veya devre dışı bırakmak, sizi ana kasaya yönlendiren bir mesaj gösterir.
5. Tap the filter icon beside the search box to open the category list, then tap a category to narrow the list to items in that category — tapping a category that has sub-categories includes their items too. It combines with the search box; tap **All categories** to clear it.

## Hiç stok takibi yapmıyorsanız

Bazı işletmeler stok saymaz — her ürünün her zaman satılabilmesini isterler. Bunun için **Ayarlar → Stok → „Ürünleri stok takibi yapmadan sat"** seçeneğini açın. Ürünler, kasada onlara ait bir stok kaydı olmasa bile satılır ve hiçbir satış stok yetersizliği nedeniyle reddedilmez.

Kasanın, ürün bittiğinde satışı durdurmasını istiyorsanız kapalı bırakın. Varsayılan budur; stok takibi yapmayan bir sistemden aktarılan bir katalogun siz bu seçeneği açana kadar hiçbir şey satamamasının nedeni de budur.

Bir ürün için **„Stok takibi? Hayır"** bilgisini taşıyan bir sistemden içe aktarma yaptığınızda kasa, o ürünün miktar sütununu gerçek bir stok seviyesi saymaz — eski sisteminizin hiç iddia etmediği bir mevcut miktarı uydurmak yerine, miktarın aktarılmadığını size bildirir.

## Tek bir ürün için stok takibini kapatma

Yukarıdaki ayar mağaza geneli. Yalnızca birkaç ürün hiç stok taşımamalıysa — sadece teslimatla gelen bir kalem, tek tek saymadığınız bir fıçı gibi — her şey için mağaza geneli ayarı açmak yerine, o ürünün Katalog'daki kendi kaydında **„Stok takibi yapılmıyor"** seçeneğini işaretleyin. Bu ürün böylece serbestçe satılır, her stok kontrolünde atlanır ve Envanter'de veya düşük stok listesinde hiç görünmez; mağazanızdaki diğer tüm ürünler ise normal şekilde takip edilmeye devam eder.
