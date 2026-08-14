---
id: country-settings
title: Ülke ayarları
section: Kurulum
order: 145
summary: "Her ülke için kullanılan varsayılan para birimi, vergi oranı ve arşiv saklama süresi ve bunları kendi dükkânınız için nasıl değiştireceğiniz."
keywords: [ülke, para birimi, vergi, kdv, saklama, arşiv, bölge, varsayılan]
routes: [/country-settings]
---

# Ülke ayarları

Kasanın tanıdığı her ülke makul varsayılanlarla gelir: hangi para birimini kullandığı, alışılmış vergi oranı, bu verginin fiyata dahil olup olmadığı ve arşivlenmiş işlem gruplarının en az kaç gün saklanması gerektiği.

Bu sayfa, o ülke varsayılanlarının bulunduğu yerdir. **İlk kurulum sihirbazı artık bunları okuyor**, bu yüzden buradaki bir düzenleme, kasa sıfırdan kurulduğunda ülke adımına ulaşır. Bu, zaten kurulmuş bir dükkânı değiştirmez — para birimi/vergisi o zaman seçilen şekilde kalır. Arşiv saklama farklıdır: burada gösterilen değer, bir sıfırlama arşivi grubunun (Ayarlar → Veri yönetimi) kalıcı silinmesinin kaydedildiği andan itibaren her dükkân için ölçüldüğü değerdir — bunun nasıl işlediğini görmek için Raporlar yardım konusundaki "Rapor saklama" bölümüne bakın.

## Nasıl kullanılır

1. Menüden **Ülke ayarları**'nı açın (yalnızca yönetici). Her ülke para birimi, vergi oranı ve arşiv saklama alt sınırıyla listelenir.
2. Bir satırdaki değerleri düzenleyip **Kaydet**'e basın.
3. Listede olmayan bir yeri eklemek için **Ülke ekle** bölümünü kendi seçeceğiniz kısa bir kodla (yalnızca harf/rakam, en fazla 8 karakter), para birimi ve vergi oranıyla doldurun.
4. **Varsayılanlara dön**, yerleşik bir ülkeyi geldiği değerlere geri getirir. Kendi eklediğiniz bir ülke **Sil** ile tamamen kaldırılır.

## Bilmekte fayda var

- Vergi yüzde olarak girilir — %19 için `19` yazın. `8.5` gibi buçuklu oranlar da kullanılabilir.
- **Vergi fiyata dahil**, raf fiyatının vergiyi zaten içerdiği anlamına gelir; Avrupa'nın çoğunda normal olan budur. Verginin kasada eklendiği yerlerde bu seçeneği kapalı bırakın.
- **Arşiv saklama** burada artırabileceğiniz ama gösterilen alt sınırın altına indiremeyeceğiniz bir tabandır. Bu değer, bir sıfırlama arşivi grubunun (Ayarlar → Veri yönetimi → Sıfırlama arşivleri) ne zaman kalıcı silme için uygun hale geleceğini belirler: gerçek satış içeren bir grup, arşivlendiğinden bu yana bu kadar gün geçmeden silinemez. Değeri artırmak mevcut grupları hemen daha uzun süre korur; halihazırda geçerli olan korumayı asla kısaltmaz.
- Burada bir ülkeyi düzenlemek, halihazırda kurulmuş bir dükkânı değiştirmez ve zaten aldığınız satışları yeniden yazmaz.
- Kendi ülkenizin sizden neleri saklamanızı istediğinden emin değilseniz, burada gösterilen herhangi bir sayıyı bir uyumluluk garantisi saymadan önce mali müşavirinize danışın — bu sayfa hiçbir ülkenin kayıt tutma yasasına uyumu belgelemez.
