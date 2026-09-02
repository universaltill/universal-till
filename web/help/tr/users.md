---
id: users
title: Kullanıcılar, PIN'ler ve vardiyalar
section: Bağlantı ve eklentiler
order: 340
summary: PIN girişli ayrı kasiyer ve yönetici hesapları; kimin ne sattığını ve çekmecenin ne zaman sayıldığını gösteren vardiya takibi.
routes: [/users, /pin, /login, /setup, /users/permissions]
---

# Kullanıcılar, PIN'ler ve vardiyalar

PIN girişli ayrı kasiyer ve yönetici hesapları; kimin ne sattığını ve çekmecenin ne zaman sayıldığını gösteren vardiya takibi.

## Nasıl kullanılır

1. Hesapları Kullanıcılar altında yönetin; yöneticiler ayarlara ve raporlara erişir, kasiyerler satış yapar.
2. Herkes kasada kendi PIN'iyle giriş yapar.
3. Kişi başına çekmece sayımı için vardiyaları açıp kapatın.
4. İlk kurulum sihirbazının dükkân adı adımı bu kasaya ne ad verileceğini de sorar (tek dokunuşla kabul edebilmeniz için "Kasa 1" olarak önceden doldurulmuştur) — birden fazla kasanız olduğunda işe yarar ve daha sonra Ayarlar'dan değiştirilebilir.
5. Sihirbaz ayrıca dükkânınızın türünü (kafe, perakende, hizmet, konaklama, pazar tezgâhı veya diğer) ve kasayı denemek için örnek veriler — küçük bir başlangıç kataloğu, 3 örnek müşteri ve 3 örnek promosyon kodu (biri %10 indirim) — yüklenip yüklenmeyeceğini sorar. İkisi de isteğe bağlıdır: hangi işletme türünü seçerseniz seçin aynı genel settir, katalog ürünleri ÖRNEK rozetiyle işaretlenir ve hepsi Ayarlar → Veriler'den istediğiniz zaman kaldırılabilir.
6. Sihirbazın dil ve ülke adımları bu cihazın kendi sistem diline ve saat dilimine göre önceden doldurulur — internet araması yapılmaz, bu yüzden kasa henüz çevrimiçi olmadan da çalışır. Dil adımı butonlar arasında tek dokunuşla seçim olarak kalır. Ülke adımı başta yalnızca algılanan ülkeyi, önceden seçili olarak gösterir; farklı bir ülkeyi görüp seçmek için "Tüm ülkeleri göster"e dokunun. Algılanan dil henüz hazır değilse sihirbaz bunu belirtir ve İngilizce ile birlikte bugün hazır olan dilleri gösterir. Eklenti kataloğunda bulunan diller de dil adımında görünür — birini seçtiğinizde kasa onu hemen orada indirip kurar (yalnızca bu indirme için internet gerekir); indirme o an tamamlanamazsa kasa arka planda denemeye devam eder ve siz bu sırada hazır dillerden biriyle devam edebilirsiniz.
7. Sihirbazın ilk ekranı her şeyden önce Universal Till'in daha yeni bir sürümü olup olmadığını denetler ve varsa kuruluma devam etmeden önce hemen güncellemeyi önerir — kabul ederseniz yeni sürüm indirilir, kurulur ve kasa yeniden başlar, sizi yine bu ilk ekrana ama artık yeni sürümde geri getirir; reddederseniz ya da sadece devam ederseniz kurulum elinizdeki sürümle sürer. Bu kurulumu asla yavaşlatmaz: kasa çevrimdışıysa veya denetim güncelleme sunucusuna ulaşamıyorsa hiçbir şey görünmez — ne hata, ne gecikme. Bu şekilde kendini güncelleyemeyen bir kurulumda bunun yerine, kasa türünüze göre ya yeni sürümü kendiniz indirebileceğiniz bir bağlantı ya da bir güncellemenin var olduğunu belirten sade bir not gösterilir.
8. Almanya'da sihirbaz, ülke adımının hemen ardından bir "işletme bilgileri" adımı ekler: işletmenin yasal adı, sahibinin adı, vergi numarası (Steuernummer veya USt-IdNr.) ve işletme adresi. Bu bilgiler yalnızca mağazanız için fiş imzalamayı (TSE) Universal Till Cloud üzerinden kurmak için kullanılır — asla banka bilgisi istenmez. Adım isteğe bağlıdır: imzalamayı sonra kurmak veya kendi çözümünüzü kullanmak için atlayabilirsiniz. Doldurursanız kasa isteği arka planda gönderir — kurulum sırasında çevrimdışıysa bile kendi kendine yeniden dener ve tamamlanana ya da siz kapatana kadar Ayarlar durumun tam olarak nerede olduğunu gösterir (devam ediyor, hizmetin hazırlanması bekleniyor veya reddedildi ve nedeni). Açıkça söyleyelim: bu şekilde kurduğumuz bir imzalama hizmeti için cihazın yönetici kimlik bilgisini (PUK) siz değil Universal Till saklar ve hizmeti sizin adınıza yönetir; bu bilgi mağazanıza aittir ve istediğiniz zaman bizden talep edebilirsiniz. Ülkeniz için eklenti kataloğunda uygun bir vergi eklentisi varsa (bugün için Almanya), aynı adım bunu kurmayı da önerir: ülkenin orada yeme/paket servis KDV oranlarını uygular ve her satışı yapılandırdığınız TSE ile imzalar. Tamamen isteğe bağlıdır ve asla kendiliğinden kurulmaz — şimdi veya daha sonra eklenti kataloğundan kurabilirsiniz; kurulduktan sonra sihirbaz onu bir daha önermez.
9. Sihirbaz ayrıca başka bir kasa sisteminden geçip geçmediğinizi sorar. Sıfırdan başlamak için "Hayır"ı seçin, ya da dışa aktarma/yedek dosyanıza tam olarak sihirbazın içinden göz atmak için CSV/Excel'i seçin (Loyverse ve Square dışa aktarımları otomatik tanınır, speedy kasse / pepperm cashbox yedeği de doğrudan desteklenir) ve devam etmeden önce önizleyin — kurulum bitene kadar hiçbir şey içe aktarılmaz. Önceki bir adımda mağazanız için bir para birimi seçtiyseniz, kurulum tamamlandığında ürünleriniz genellikle zaten katalogda olur; aksi halde (veya dosyanın gözden geçirilmesi gerekiyorsa) cihaz sizi doğrudan içe aktarma ekranına götürür ve dosyanız yeniden yüklemeye gerek kalmadan hazır bekler. Henüz hazır değil misiniz? "Sonra sor"u seçin; kullanana veya kapatana kadar Ayarlar → Veriler altında "Başka bir kasadan içe aktar" istemi görünür.
10. Sihirbazın son ekranı ayrıca — bir kez ve siz işaretlemedikçe işaretsiz — bu kasayı Universal Till bulutuna hemen kaydetmek isteyip istemediğinizi sorar. İşaretlerseniz kurulum biter bitmez kasa; cihaz kimliğini, mağaza adınızı, mağaza bölgenizi ve yazılım sürümünü, destek, güncellemeler ve lisanslama için kullanılan Universal Till bulut pazaryerine gönderir — adresiniz gönderilmez. İşaretlemezseniz hiçbir şey gönderilmez — kasa yalnızca eklenti mağazasını ilk kullandığınızda veya Şimdi kaydol'a bastığınızda kaydolur. Her iki durumda da kurulum interneti beklemez: işaretlediğinizde çevrimdışıysanız kurulum yine de tamamlanır ve kasa, eklenti mağazasını açana, Şimdi kaydol'a basana veya çevrimiçi olduğunuzda Ayarlar → Kasa kaydı bölümünü kontrol edene kadar kayıtsız kalır. Kararınızı istediğiniz zaman oradan değiştirebilirsiniz; bkz. [Mağaza kaydı ve sahiplenme](/help/claim).
11. Bir kullanıcı oluşturmak, birinin PIN'ini ayarlamak veya bir hesabı etkinleştirmek/devre dışı bırakmak bir yönetici veya admin rolü gerektirir. Kasiyer olarak oturum açtıysanız ve bunlardan birini denerseniz, ekran sadece reddetmez: orada bir yönetici veya adminin onaylayabileceği anında bir PIN penceresi açar — kasadaki [diğer anında yönetici onayları](/help/elevation) gibi.
12. Tam ekran veya kiosk modundaki bir kasada oturum açma ekranında mı sıkıştınız? Oturum açma ekranında varsayılan olarak kapalı bir "Kilitli mi kaldınız? Masaüstüne dönün" bağlantısı vardır: açın ve kasadan çıkıp işletim sistemi masaüstüne dönmek için bir yönetici PIN'i girin — önce oturum açmanız gerekmez. Bu, Ayarlar → Görünüm'deki yönetici PIN'i korumalı "İşletim sistemi penceresine çık" eyleminin aynısıdır; her kasa türünde ne yaptığını görmek için [Diller ve görünüm](/help/display) konusuna bakın.
13. Yan menüdeki kendi adınız (👤) PIN değiştirme'yi açar; yanındaki Kilitle düğmesi sizi doğrudan PIN pad'ine çıkış yapar.
14. Tam ekran veya kiosk modunda bir kasada oturum açma ekranında mı sıkıştınız? Oturum açma ekranında varsayılan olarak kapalı "Kilitli mi kaldınız? Masaüstüne dönün" bağlantısı vardır: açın ve kasadan çıkıp işletim sistemi masaüstüne dönmek için bir yönetici PIN'i girin — önce oturum açmanız gerekmez. Bu, Ayarlar → Görünüm'deki yönetici PIN'i korumalı "İşletim sistemi penceresine çık" eyleminin aynısıdır; her kasa türünde ne yaptığını görmek için [Diller ve görünüm](/help/display) konusuna bakın.

## Kendi PIN'inizi değiştirme

Herkes kendi PIN'ini değiştirebilir, yönetici gerekmez — yönetici sadece *başkasının* PIN'ini Kullanıcılar'dan ayarlamak veya sıfırlamak için gereklidir.

1. Yan menüdeki adınıza (👤) dokunarak PIN değiştirme'yi açın.
2. Mevcut PIN'inizi, ardından yeni PIN'inizi iki kez girin.
3. Gönderin: oturumunuz kapatılır ve PIN pad'ine dönersiniz — yeni PIN ile tekrar giriş yapın.

Yanlış mevcut PIN, bu kasada başarısız bir oturum açma denemesi olarak sayılır, tıpkı giriş ekranındaki yanlış PIN gibi — yeterli yanlış deneme pad'i herkes için kısa bir süre kilitler, bu yüzden tekrar tekrar tahmin etmeyin. Bu kasada başka biri tarafından zaten kullanımda olan yeni bir PIN reddedilir; farklı bir tane seçin.

## Boşta otomatik kilitleme

Gözetimsiz, oturumu açık bir kasa gerçek bir risktir — yanından geçen herkes, son oturum açan kişi olarak satış yapabilir, iade alabilir veya ayarları açabilir. Kasa, bir süre dokunulmadan kaldıktan sonra kendini PIN pad'ine kilitler, hiçbir işlem veya işlem kaybı olmadan: sepette ne vardıysa, siz (veya izin verilen başka biri) tekrar giriş yaptığınızda tam olarak bıraktığınız gibi kalır.

1. Zaman aşımını Ayarlar → Otomatik kilitleme'den ayarlayın: kapalı, veya 2/5/10/15/30/60 dakika — başlangıç için 10 dakika, biri değiştirene kadar.
2. Kasadaki herhangi bir dokunuş, tuş basışı veya tarama geri sayımı sıfırlar — gerçekten boşta kaldıktan sonra tetiklenir.
3. Bu ayarı değiştirmek bir yönetici veya admin rolü gerektirir, diğer ayar değişikliklerindeki [yönetici onay penceresi](/help/elevation) ile aynı.
4. Zaman aşımını beklemek istemiyor musunuz? Yan menüdeki adınızın yanındaki Kilitle düğmesini kullanarak istediğiniz zaman kendiniz kilitleyin.

## İzin matrisi

Süper yönetici, kasiyer/müdür/yönetici rolü atamanın ötesine geçebilir — her rolün tam olarak neler yapabileceğini (iadeler, iptaller, fiyat geçersiz kılma, ayarlar, raporlar ve daha fazlası) Kullanıcılar → İzinler'den, işlem bazında verebilir veya geri alabilir. Her değişiklik, kimin yaptığıyla birlikte denetim kaydına işlenir. Asla yapamayacağınız tek şey, bir süper yöneticinin bu sayfaya kendi erişimini geri almaktır — bu izin her zaman kilitlidir, böylece kimse yanlışlıkla tüm süper yöneticileri dışarıda bırakamaz.

## Süper yönetici olmak

Yalnızca mevcut bir süper yönetici, bir başkasını süper yönetici yapabilir veya bu role terfi ettirebilir — Kullanıcılar'dan, ya yeni bir hesap için rol olarak "süper yönetici"yi seçin ya da mevcut bir kişinin adının yanındaki "Süper yöneticiye yükselt"i kullanın. Her ikisi de, diğer her izin-hassas değişiklik gibi denetim kaydına işlenir ve kişinin bir sonraki oturum açışında etkili olur.

Henüz süper yöneticisi olmayan bir dükkânın (bu rol bu kasa sürümünden önce yoktu) ilkini oluşturmak için tek seferlik bir kurulum adımına ihtiyacı vardır — Kullanıcılar'da süper yönetici seçeneği henüz yoksa destek ekibine sorun.

## Rolü değiştirme veya geri alma

Her kişinin satırında, adının yanında bir rol seçici ve bir "Rolü değiştir" düğmesi de bulunur — yalnızca yöneticiler ve süper yöneticiler görür — birini kasiyer, müdür, yönetici ve süper yönetici arasında, yalnızca yukarı değil, her iki yönde de taşımanın genel yolu. Bunu bir süper yöneticiyi yöneticiye geri almak için kullanın (hesabını devre dışı bırakmak yerine — bu, PIN'ini ve oturum açma geçmişini de düşürür) ya da yanlışlıkla atanan bir rolü düzeltmek için. Bir yönetici kişileri kasiyer/müdür/yönetici arasında özgürce taşıyabilir, ama kimsenin süper yönetici rolünü veremez veya geri alamaz — bunu yalnızca bir süper yönetici yapabilir, yeni hesap oluştururken geçerli olan kısıtlamanın aynısı. Bir müdür kimsenin rolünü değiştiremez. Kasayı hiçbir zaman yöneticisiz veya süper yöneticisiz bırakamazsınız — her ikisinin de sonuncusu, son süper yöneticinin devre dışı bırakmaya karşı korunduğu gibi korunur. Buradaki her rol değişikliği gibi denetim kaydına işlenir ve kişinin bir sonraki oturum açışında etkili olur.
