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

1. Menüden **Ülke ayarları**'nı açın (yalnızca yönetici). Varsayılan olarak yalnızca kendi dükkânınızın ülkesini, para birimi, vergi oranı ve arşiv saklama alt sınırıyla görürsünüz.
2. Bir ülke eklemek için arama kutusunun yanındaki **+** düğmesine dokunun, ya da düzenlemek için mevcut bir satıra (veya kalem düğmesine) dokunun. Kod, para birimi, simge, vergi oranı ve arşiv saklama alt sınırını içeren tam ekran bir iletişim kutusu açılır; gereken alanları doldurup **Kaydet**'e dokunun. Bir ülke eklerken kod serbest metindir (yalnızca harf/rakam, en fazla 8 karakter) — mevcut bir satırı düzenlerken kod kilitlenir, çünkü değişikliklerinizin hangi satıra kaydedileceğini belirleyen odur.
3. Kasanın tanıdığı tüm ülkeleri görmek için — örneğin farklı bir ülkede çalışacak bir kasa için değerleri önceden ayarlamak istiyorsanız — tablonun üstünde **Tüm ülkeleri göster**'e dokunun. **Yalnızca kendi ülkemi göster**, sizi yalnızca kendi ülkenize geri götürür. Tablo yerinde değişir — aynı sayfada kalırsınız ve Yönetim ekranında yanındaki menü ağacı yerinde durur.
4. İletişim kutusunu kaydedilmemiş değişikliklerle kapatmak önce sizden onay ister, böylece Kapat'a yanlışlıkla dokunmak yazdıklarınızı asla kaybetmez.
5. Açık bir ülke iletişim kutusunda, Kapat'ın yanındaki düğme yerleşik bir ülke için **Varsayılanlara dön**'dür (geldiği değerlere geri getirir) veya kendi eklediğiniz bir ülke için **Sil**'dir (tamamen kaldırır) — her ikisi de önce sizden onay ister.

## Bilmekte fayda var

- Vergi yüzde olarak girilir — %19 için `19` yazın. `8.5` (veya `8,5` — nokta ya da virgül) gibi buçuklu oranlar da kullanılabilir.
- **Vergi fiyata dahil**, raf fiyatının vergiyi zaten içerdiği anlamına gelir; Avrupa'nın çoğunda normal olan budur. Verginin kasada eklendiği yerlerde bu seçeneği kapalı bırakın.
- **Arşiv saklama** burada artırabileceğiniz ama gösterilen alt sınırın altına indiremeyeceğiniz bir tabandır. Bu değer, bir sıfırlama arşivi grubunun (Ayarlar → Veri yönetimi → Sıfırlama arşivleri) ne zaman kalıcı silme için uygun hale geleceğini belirler: gerçek satış içeren bir grup, arşivlendiğinden bu yana bu kadar gün geçmeden silinemez. Değeri artırmak mevcut grupları hemen daha uzun süre korur; halihazırda geçerli olan korumayı asla kısaltmaz. Sıfırlama arşivleri listesinin kendisi, korunan her grubun saklama bitiş tarihini doğrudan gösterir ve o tarihe kadar Kalıcı olarak sil düğmesini gizler — böylece onay ifadesini yazıp yalnızca reddedilmezsiniz.
- Burada bir ülkeyi düzenlemek, halihazırda kurulmuş bir dükkânı değiştirmez ve zaten aldığınız satışları yeniden yazmaz.
- Kendi ülkenizin sizden neleri saklamanızı istediğinden emin değilseniz, burada gösterilen herhangi bir sayıyı bir uyumluluk garantisi saymadan önce mali müşavirinize danışın — bu sayfa hiçbir ülkenin kayıt tutma yasasına uyumu belgelemez.
- Dükkânınızın ülke ayarı bu sayfadaki hiçbir şeyle eşleşmiyorsa (nadir bir durum — normalde bu olmamalıdır), sayfa neden olduğunu açıklayan bir notla birlikte tüm ülkeleri gösterir, alışılmış tek ülke görünümü yerine.
