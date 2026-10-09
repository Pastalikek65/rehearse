# Türkçe hızlı başlangıç

Rehearse, kendi sunucunda kullandığın uygulamanın güncellemesini **sağladığın
yedekten oluşturulan ayrı ortamlarda** dener. Yayımlanan v0.2.0 beta iki sabit çifti
destekler: Miniflux 2.2.19 → 2.3.3 ve Forgejo 15.0.9 → 16.0.5; ikisi de
PostgreSQL 17.11 kullanır. Değişmez v0.1 paketi yalnız Miniflux içindir.
Windows ve Linux paketleri her iki sentetik uygulama provasını ve kesinti/
kurtarma senaryolarını geçti. Üretim v1 yayımlanmadı ve yeterliliği onaylanmadı.

Kaynak koddan Windows'ta Go 1.27 ile derlemek için:

Hazır paket için [v0.2.0 beta sürümünü](https://github.com/Pastalikek65/rehearse/releases/tag/v0.2.0) aç. [Windows ZIP](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/rehearse-0.2.0-windows-amd64.zip) veya [Linux tar.gz](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/rehearse-0.2.0-linux-amd64.tar.gz) dosyasını çıkar ve [SHA256SUMS](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/SHA256SUMS) ile özeti karşılaştır. Paketler imzasızdır. [verification.json](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/verification.json) her iki arşivin prova ve kabul kanıtını içerir. Gerçek sentetik [Forgejo HTML raporunu](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/forgejo-example-report.html) ve [Miniflux HTML raporunu](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/miniflux-example-report.html) inceleyebilirsin. Aşağıdaki `bin/` komutları kaynak koddan derleme içindir; hazır pakette Windows için `.\rehearse.exe`, Linux için `./rehearse` kullan.

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

`plan`, Miniflux için dosya başlığını; Forgejo için ZIP yapısını, özet
değerlerini ve veri TAR'ını inceler. PostgreSQL geri yüklemesini veya
uygulama davranışını doğrulamaz. `run`, geri yükleme, migration, veri
karşılaştırması, gerçek API işlemleri ve eski sürüme temiz yedekten kurtarmayı
dener. Başarısızlıkta sıfırdan farklı çıkış kodu verir.

Miniflux için kendi yedeğini kullanacaksan örnek JSON'u kopyala. PostgreSQL custom-format `.dump` dosyasını ve mevcut Miniflux hesabının ortam değişkeni referanslarını belirt. Parolayı JSON'a yazma. Yerel durum dizini özel yedek kopyası içerir; hesabına özel tut.

Forgejo'nun v0.2.0 beta sentetik örneği [hazır pakette](../examples/forgejo/README.md) bulunur. Kaynak koddan derlediysen Linux'ta şöyle çalıştır:

```sh
export REHEARSE_FORGEJO_EXAMPLE_TOKEN="$(cat examples/forgejo/fixture-read-token.txt)"
./bin/rehearse plan examples/forgejo/rehearse.json
./bin/rehearse run examples/forgejo/rehearse.json
```

Windows PowerShell'de şöyle çalıştır:

```powershell
$env:REHEARSE_FORGEJO_EXAMPLE_TOKEN = (Get-Content -Raw examples/forgejo/fixture-read-token.txt).Trim()
.\bin\rehearse.exe plan examples/forgejo/rehearse.json
.\bin\rehearse.exe run --wsl-distro MyRehearsalWSL examples/forgejo/rehearse.json
```

Token yalnızca sentetik örnek hesabı içindir. Gerçek sentetik [Forgejo HTML raporunu](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/forgejo-example-report.html), [JSON raporunu](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/forgejo-example-report.json) ve [arşiv doğrulama kanıtını](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/verification.json) incele.

Destek ve güvenli sınırlar için [support.md](support.md), kesinti ve temizlik için [state-storage.md](state-storage.md), Forgejo arşiv ve kontrol kapsamı için [Forgejo bağdaştırıcısı](forgejo-adapter.md), geçmiş ve kurtarma komutlarının sınırları için [history-recovery.md](history-recovery.md) sayfalarına bak. Forgejo için yalnızca bu belgelenen sabit sürüm çifti desteklenir; üretim v1 yeterliliği beklemededir. Üretim volume'larını araca verme.
