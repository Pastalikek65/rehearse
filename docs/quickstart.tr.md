# Türkçe hızlı başlangıç

Rehearse, kendi sunucunda kullandığın uygulamanın güncellemesini **sağladığın yedekten oluşturulan ayrı ortamlarda** dener. Yayımlanmış değişmez v0.1 paketleri yalnız Miniflux 2.2.19 → 2.3.3 ve PostgreSQL 17.11 sürümlerini destekler. Geliştirme kaynak kodunda Forgejo bağdaştırıcısı ile `archive`, `history` ve `recover` komutları da vardır; Forgejo tam sentetik-fixture doğrulaması beklemededir, v0.1 paketinde değildir ve hazır Forgejo fixture'ı sunulduğu anlamına gelmez. Kararlı v1 henüz yayımlanmadı.

Go 1.27 ile Windows üzerinde derle:

Hazır paket için [v0.1 önizleme sürümünü](https://github.com/Pastalikek65/rehearse/releases/tag/v0.1.0) aç. Windows ZIP veya Linux tar.gz dosyasını çıkar; `SHA256SUMS` ile dosyanın özetini karşılaştır. Paketler imzasızdır. Aşağıdaki `bin/` komutları kaynak koddan derleme içindir; hazır pakette Windows için `.\rehearse.exe`, Linux için `./rehearse` kullan. Sürümdeki `verification.json` tam o arşivin kabul kanıtını, örnek HTML/JSON raporları gerçek sentetik prova sonucunu gösterir.

```powershell
New-Item -ItemType Directory -Force bin | Out-Null
go build -trimpath -o bin/rehearse.exe ./cmd/rehearse
```

WSL2 dağıtımının içinde Docker Engine 28+ ve Docker Compose kurulu olmalıdır. Komutta dağıtımı açıkça seçersin; araç Docker'ı kendisi kurmaz veya başlatmaz.

Örnek yedek tamamen sentetiktir. İlk denemeyi bu dosyayla yap:

```powershell
$env:REHEARSE_EXAMPLE_USER = 'rehearse-fixture'
$env:REHEARSE_EXAMPLE_PASSWORD = 'synthetic-fixture-password'
.\bin\rehearse.exe plan examples/miniflux/rehearse.json
.\bin\rehearse.exe run --wsl-distro MyRehearsalWSL examples/miniflux/rehearse.json
```

`MyRehearsalWSL` yerine hazırladığın dağıtımın adını yaz. İlk çalıştırma sabit image'ları indirir. En az 4 GiB kullanılabilir bellek; image'lar, yedek kopyası ve üç veritabanı için disk alanı gerekir.

Linux üzerinde önce `mkdir -p bin` çalıştır, sonra `go build -trimpath -o bin/rehearse ./cmd/rehearse` ile derle. Ortam değişkenlerini `export` ile tanımla; `./bin/rehearse run examples/miniflux/rehearse.json` komutunu kullan. Linux doğrudan yerel Docker Unix socket'ine bağlanır.

Çıktıdaki run ID'yi aşağıdaki `RUN_ID` yerine koy. Windows'ta:

```powershell
.\bin\rehearse.exe report --format json RUN_ID
.\bin\rehearse.exe report --format html RUN_ID
```

Linux'ta aynı komutları `./bin/rehearse` ile çalıştır. HTML çıktısını bir dosyaya yönlendirip tarayıcıda açabilirsin.

`plan` yalnız dosya başlığını inceler; tam yedek doğrulaması değildir. `run`, geri yükleme, migration, veri karşılaştırması, gerçek API işlemleri ve eski sürüme temiz yedekten kurtarmayı dener. Başarısızlıkta sıfırdan farklı çıkış kodu verir.

Kendi yedeğin için örnek JSON'u kopyala. PostgreSQL custom-format `.dump` dosyasını ve mevcut Miniflux hesabının ortam değişkeni referanslarını belirt. Parolayı JSON'a yazma. Yerel durum dizini özel yedek kopyası içerir; hesabına özel tut.

Destek sınırları için [support.md](support.md), kesinti ve temizlik için [state-storage.md](state-storage.md) dosyasını oku. Üretim volume'larını araca verme.

Geliştirme kaynak kodundaki Forgejo yapılandırma/arsiv sözleşmesi için [Forgejo bağdaştırıcısı](forgejo-adapter.md), geçmiş ve kurtarma komutlarının sınırları için [history-recovery.md](history-recovery.md) sayfalarına bak. Bu bilgiler Forgejo'nun v0.1'de desteklendiği anlamına gelmez.
