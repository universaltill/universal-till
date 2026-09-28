---
id: updates
title: Yazılım güncellemeleri
section: Bağlantı ve eklentiler
order: 350
summary: Kasa yeni sürümleri denetler ve çıktığında haber verir; çoğu platformda tek tıkla kendini günceller.
---

# Yazılım güncellemeleri

Kasa yeni sürümleri denetler ve çıktığında haber verir; çoğu platformda tek tıkla kendini günceller.

## Nasıl kullanılır

1. Ayarlar → Yazılım güncelleme → Şimdi denetle güncel olup olmadığınızı gösterir.
2. Güncelleme önerildiğinde Şimdi güncelle'ye tıklayın — uygulama yeni sürümle yeniden başlar. Linux bilgisayarda masaüstü uygulaması, bir sistem yükseltmesi (apt) yeni bir sürüm kurduğunda yaklaşık bir dakika içinde kendini de yeniden başlatır; kasanın sayfası penceresinde görünmez olursa, kasa yeniden yanıt verene kadar sayfayı kendiliğinden yeniden yükler.
3. Durum çubuğundaki güncelleme rozeti de aynısını gösterir. Kasanın güncellemeyi kendisi kuramadığı durumlarda (taşınabilir Windows zip'i veya Intel Mac) bunun yerine indirme sayfasını açar. Uygulama içi güncellemesi olmayan bir kioskta sadece bağlantısız bilgilendirme metni gösterilir.

## Otomatik güncellemeler

Kasa, siz kapatmadıkça gece kendini günceller.

1. Ayarlar → Yazılım güncelleme → Otomatik güncelle baştan açıktır, saat 03:00'te. Her kasa bu saatten sonraki 30 dakika içinde kendi anını seçer, böylece birkaç kasa aynı anda yeniden başlamaz.
2. Bir satışın ortasında asla yeniden başlamaz. Bir sepette (self servis veya masa siparişi dahil) hâlâ ürün varsa, sepet boşalana kadar bekler. Bu yarım saat içinde olmazsa ya da kasa o sırada kapalıysa, ertesi gece yeniden dener.
3. Otomatik güncellemeleri durdurmak için Otomatik güncelle işaretini kaldırıp Kaydet'e tıklayın. Kasa bu seçimi korur. Ana kasada bu, ek kasaların onu izlemesini de durdurur; ek kasa bu seçimi her zaman ana kasasından alır.
4. Ek kasa ana kasayı izler: ana kasa daha yeni bir sürüm çalıştırdığında, ek kasa tam olarak o sürümü açık satışın olmadığı ilk anda kurar. Android ek kasalar ve taşınabilir Windows zip'inden kurulan ek kasalar, kendi başlarına kurabilene kadar bunun yerine bir 'gerekli' notu gösterir.
5. Windows'ta kasa imzalı kurulum programını indirir ve denetler. Ardından kasa kapanır, güncellemeyi kurar ve kendiliğinden yeniden açılır. Bu yaklaşık bir dakika sürer. Android kasalar güncellemeleri henüz kendi başına kuramaz. Onları yukarıda anlatıldığı gibi elle güncelleyin. Windows yükleyicisini kendiniz çalıştırırsanız, Universal Till hâlâ açıksa önce onu kapatır; bu yüzden başlatmadan önce devam eden satışı tamamlayın.

## Her sürümdeki yenilikler

Bir güncellemeden sonra kasa neyin değiştiğini size anlatır — sade bir dille ve kasanın dilinde.

1. Ayarlar → Hakkında, bu kasanın çalıştırdığı sürümü, bu kasada ilk ne zaman başlatıldığını ve "Yenilikler"i gösterir: bu sürümün ve ondan önceki birkaç sürümün notları, en yenisi önce, Yeni, İyileştirmeler ve Düzeltmeler olarak gruplanmış.
2. Bir güncellemeden sonra kasayı ilk kez bir yönetici veya admin açtığında, durum çubuğunda küçük bir "… sürümüne güncellendi — yenilikleri görün" notu çıkar. Notları okumak için ona dokunun ya da gizlemek için × simgesine dokunun. Satışa asla engel olmaz ve yalnızca bir kez görünür.
3. Kasiyerler bu notu hiç görmez, self-servis kioskundaki müşteriler de görmez. Yeni kurulmuş bir kasa da bu notu göstermez.
4. Notlar kasanın içinde yerleşiktir, bu yüzden internet bağlantısı olmadan da çalışır. Bir not henüz dilinize çevrilmediyse İngilizce gösterilir.

## Android kasalarda

Android uygulaması kendini masaüstü sürümleri gibi değiştiremez; bunun yerine yeni sürümü Android'in kendi yükleyicisine verir. Adımlar biraz farklıdır:

1. Ekranın altındaki yeşil güncelleme rozetine dokunun, sonra onaylamak için tekrar dokunun. Hepsi bu — yönetici olarak oturum açtıysanız PIN sorulmaz, tıpkı Windows ve Mac'te olduğu gibi.
2. Android yeni uygulamayı indirir (yaklaşık 140 MB, yavaş bağlantıda biraz zaman tanıyın; bu süre boyunca rozet "İndiriliyor" yazar) ve ardından kendi "Bu güncellemeyi yüklemek istiyor musunuz?" ekranını gösterir. Oradan onaylayın.
3. Bunu ilk yapışınızda Android, Universal Till'in uygulama yüklemesine izin vermenizi isteyebilir. İzin verin, sonra rozete tekrar dokunun.
4. Ayarlar → Yazılım güncelleme de aynı işi yapar; ayrıca hangi sürümde olduğunuzu ve hangi sürümün mevcut olduğunu gösterir.
5. Yönetici PIN kutusu ve İndir düğmesi yalnızca gerçekten yeni bir sürüm varken görünür — güncel bir kasada dokunulacak bir şey yoktur. Kasa siz dokunurken yeniden denetleyip zaten en yeni sürümde olduğunuzu görürse, hiçbir şey indirmeden bunu söyler.

İki durumda yönetici PIN'i istenir: kasiyer olarak oturum açtıysanız ya da kasa self-servis sipariş modundaysa. Self-servis modda kurulum aynı zamanda kiosk kilidini açacağı için, orada yönetici olsanız bile PIN gerekir.

İlk kurulum sırasında kasa bir güncelleme olduğunu söyler ama yüklemeyi önermez — önce kurulumu bitirin, sonra Ayarlar'dan güncelleyin.
