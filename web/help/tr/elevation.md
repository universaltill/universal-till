---
id: elevation
title: Anında yönetici onayı
section: Bağlantı ve eklentiler
order: 341
summary: Bir ekran yönetici onayı istediğinde, bir yönetici veya admin oturumu kapatıp yeniden açmadan orada PIN'iyle onaylayabilir.
keywords: [pin, onay, yönetici pin, geçersiz kılma, izin]
---

# Anında yönetici onayı

Bir izni değiştirmek, bir cihazı yükseltmek, yedek almak, gün sonu raporunu
çalıştırmak veya yeniden yazdırmak, bir kullanıcı oluşturmak ya da birinin
PIN'ini veya rolünü değiştirmek gibi bazı işlemler bir yöneticinin veya
adminin onayını gerektirir. Kendi hesabınız bu
işlemlerden birini yapmaya yetkili değilse, ekran işlemi sadece
reddetmez: hemen orada küçük bir PIN penceresi açar.

## Nasıl kullanılır

1. İşlemi her zamanki gibi deneyin. Rolünüz buna izin vermiyorsa, düz bir
   hata yerine bir yönetici veya adminin PIN'ini isteyen bir pencere açılır.
2. Bir yönetici veya admin kendi PIN'ini o pencereye girip onaylasın.
3. PIN doğruysa ve o kişi gerçekten bu işlemi yapmaya yetkiliyse, işlem
   hemen onun adına gerçekleşir — sizin adınıza değil. Kimse oturumu
   kapatıp hesap değiştirmez.
4. Yanlış bir PIN, çok fazla deneme veya hâlâ bu belirli işleme yetkisi
   olmayan birine ait bir PIN — hepsi aynı pencerede açık bir neden
   gösterir, böylece sonraki adımı bilirsiniz.

## Neler kaydedilir

İşlem, denetim kaydındaki (Raporlar → Denetim Kaydı) her şey gibi, onaylayan
yönetici veya admin tarafından yapılmış olarak kaydedilir. İlk başta engellenen
hesap da aynı kayıtta tutulur, böylece kimin denediği ve kimin gerçekten
onayladığı hikâyesi hiç kaybolmaz — bugünkü Denetim Kaydı ekranı yalnızca
onaylayanın adını kaydın sahibi olarak gösterse bile.
