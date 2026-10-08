# Türkçe hızlı başlangıç

Rehearse, kendi sunucunda kullandığın uygulamanın güncellemesini **sağladığın yedekten oluşturulan ayrı ortamlarda** dener. Şimdilik Miniflux 2.2.19 → 2.3.3 ve PostgreSQL 17.11 desteklenir. Geliştirme önizlemesidir; kararlı v1 sürümü henüz yayımlanmadı.

Go 1.27 ile Windows üzerinde derle:

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
