---
id: catalog
title: Katalog, varyantlar ve barkodlar
section: Dükkanı kurma
order: 110
summary: "Ürünleriniz: adlar, fiyatlar, reyonlar, ürün varyantları (beden, aroma…) ve ürün ya da varyant başına istediğiniz kadar barkod."
routes: [/catalog, /import, /designer]
---

# Katalog, varyantlar ve barkodlar

Ürünleriniz: adlar, fiyatlar, reyonlar, ürün varyantları (beden, aroma…) ve ürün ya da varyant başına istediğiniz kadar barkod.

## Nasıl kullanılır

1. Katalog'u açıp bir ürün satırına tıklayın — alttaki düzenleme paneli tüm varyant ve barkodları gösterir. ÖRNEK rozetli ürünler isteğe bağlı başlangıç kataloğundan gelmiştir (Ayarlar → Veriler'den kaldırılabilir).
2. Varyant adlarını, SKU'ları, fiyatları, maliyetleri ve fotoğrafları doğrudan tabloda düzenleyin; barkodları çip olarak ekleyip çıkarın; varyant başına etiket yazdırın.
   - **Düz kod (ağırlık/fiyatı yok say):** Bir barkod eklerken bu kutuyu yalnızca mağazanız ağırlık veya fiyat gömülü terazi etiketleri kullanıyorsa *ve* tesadüfen aynı rakamlarla başlayan sıradan bir ürün barkodu giriyorsanız işaretleyin. Bu, kasaya kodun bir kısmını ağırlık veya fiyat olarak okumak yerine kodu yazıldığı gibi saklamasını söyler. Diğer tüm durumlarda işaretsiz bırakın — sıradan bir barkod üzerinde hiçbir etkisi yoktur.
3. İçe aktar ile CSV dosyasından ürün yükleyin, Dışa aktar ile kataloğunuzu kaydedin — speedy kasse / pepperm cashbox `.bkp` yedek dosyası da doğrudan kabul edilir, dönüştürmeye gerek kalmadan otomatik tanınır. Her iki dosya da `Tax rate` adlı bir vergi oranı sütunu taşıyabilir (farklıysa `Takeaway tax` adlı paket servis vergi oranı sütunuyla birlikte) — Universal Till eşleşen vergi kodlarını sizin için otomatik oluşturur.
4. Paket servis vergi oranının uygulanması için Alman vergi eklentisinin etkin olması gerekir — kurulu ama devre dışıysa, içe aktarma yine doğru vergi kodlarını oluşturur ama özet, paket servis oranının uygulanmadığını belirtir; eklentiyi etkinleştirip içe aktarmayı yeniden çalıştırın.
5. Para birimi hiç açıkça ayarlanmamış bir kasaya fiyatlı ürünler ilk kez içe aktarıldığında, İçe aktar hiçbir şey yazmadan önce durur ve dosyadaki fiyatların hangi para biriminde olduğunu onaylamanızı ister — yeni bir kasa varsayılan olarak GBP kullanır, bu yüzden bunu kontrol etmeden yabancı bir katalog içe aktarmak her şeyi yanlış para biriminde fiyatlandırabilir. Gösterilen para birimi doğruysa onaylayın veya doğru olanı seçin; her durumda içe aktarma otomatik olarak devam eder ve kasanızın para birimi ayarlandıktan sonra bir daha sormaz.
6. Bir taramanın veya elle girişin hangi barkod türlerini kabul ettiği, Ayarlar → Barkod türleri üzerinden mağaza bazında denetlenir. Yaygın perakende türlerinin tümü (EAN-13, EAN-8, UPC-A, UPC-E, GTIN-14, Code 128, Code 39, dahili/PLU kodları) varsayılan olarak açıktır, böylece mevcut barkodlar tıpkı önceki gibi çalışmaya devam eder. İki tartı etiketi türü (ağırlık gömülü ve fiyat gömülü EAN-13) varsayılan olarak kapalıdır — eşleşen bir kodun nasıl okunduğunu değiştirdiği için, yalnızca basılı tartı etiketlerini kullanmaya hazır olduğunuzda birini açın.
