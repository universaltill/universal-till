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
6. Sihirbazın dil ve ülke adımları bu cihazın kendi sistem diline ve saat dilimine göre önceden doldurulur — internet araması yapılmaz, bu yüzden kasa henüz çevrimiçi olmadan da çalışır — ve ikisi de tek dokunuşla değiştirilebilir kalır. Algılanan dil henüz hazır değilse sihirbaz bunu belirtir ve İngilizce ile birlikte bugün hazır olan dilleri gösterir.
7. Sihirbaz ayrıca başka bir kasa sisteminden geçip geçmediğinizi sorar. Sıfırdan başlamak için "Hayır"ı seçin, ya da doğrudan katalog içe aktarıcısına gitmek için CSV/Excel'i seçin. Henüz hazır değil misiniz? "Sonra sor"u seçin; kullanana veya kapatana kadar Ayarlar → Veriler altında "Başka bir kasadan içe aktar" istemi görünür.

## İzin matrisi

Süper yönetici, kasiyer/müdür/yönetici rolü atamanın ötesine geçebilir — her rolün tam olarak neler yapabileceğini (iadeler, iptaller, fiyat geçersiz kılma, ayarlar, raporlar ve daha fazlası) Kullanıcılar → İzinler'den, işlem bazında verebilir veya geri alabilir. Her değişiklik, kimin yaptığıyla birlikte denetim kaydına işlenir. Asla yapamayacağınız tek şey, bir süper yöneticinin bu sayfaya kendi erişimini geri almaktır — bu izin her zaman kilitlidir, böylece kimse yanlışlıkla tüm süper yöneticileri dışarıda bırakamaz.

## Süper yönetici olmak

Yalnızca mevcut bir süper yönetici, bir başkasını süper yönetici yapabilir veya bu role terfi ettirebilir — Kullanıcılar'dan, ya yeni bir hesap için rol olarak "süper yönetici"yi seçin ya da mevcut bir kişinin adının yanındaki "Süper yöneticiye yükselt"i kullanın. Her ikisi de, diğer her izin-hassas değişiklik gibi denetim kaydına işlenir ve kişinin bir sonraki oturum açışında etkili olur.

Henüz süper yöneticisi olmayan bir dükkânın (bu rol bu kasa sürümünden önce yoktu) ilkini oluşturmak için tek seferlik bir kurulum adımına ihtiyacı vardır — Kullanıcılar'da süper yönetici seçeneği henüz yoksa destek ekibine sorun.
