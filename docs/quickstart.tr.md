# Türkçe hızlı başlangıç

Rehearse, kendi yedek dosyanı kullanarak uygulama güncellemesini **ayrı ve yeni
ortamlarda** dener. Desteklenen sabit çiftler Miniflux 2.2.19 → 2.3.3 ve
Forgejo 15.0.9 → 16.0.5'tir; ikisi de PostgreSQL 17.11 kullanır. Başka sürüm
çiftleri desteklenmez. Değişmez v0.1.0 paketi yalnız Miniflux içindir.

Hazır paketleri [GitHub Releases](https://github.com/Pastalikek65/rehearse/releases) sayfasından indir. Arşivi çıkar ve o sürümün `SHA256SUMS` dosyasıyla özetini karşılaştır. Paketler imzasızdır. [v1.0.0 verification.json](https://github.com/Pastalikek65/rehearse/releases/download/v1.0.0/verification.json) yalnızca tam olarak eşleşen arşiv ve test ortamı için kabul kanıtını gösterir; kaynak kod veya `BUILD.json` tek başına paket kabulü anlamına gelmez. Aşağıdaki `bin/` komutları kaynak koddan derleme içindir; hazır pakette Windows için `.\rehearse.exe`, Linux için `./rehearse` kullan.

Kaynak koddan Windows'ta Go 1.27 ile derlemek için:

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

Forgejo'nun sentetik örneği [hazır pakette](../examples/forgejo/README.md) bulunur. Kaynak koddan derlediysen Linux'ta şöyle çalıştır:

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

Token yalnızca sentetik örnek hesabı içindir. Arşivlenmiş v0.2.0 sentetik [Forgejo HTML raporunu](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/forgejo-example-report.html), [JSON raporunu](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/forgejo-example-report.json) ve o pakete ait [tarihsel doğrulama kaydını](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/verification.json) inceleyebilirsin. Bunlar yalnızca ilgili eski paketi açıklar; güncel paket kabulü için seçilen sürümün `verification.json` kaydını kullan.

Destek ve güvenli sınırlar için [support.md](support.md), kesinti ve temizlik için [state-storage.md](state-storage.md), Forgejo arşiv ve kontrol kapsamı için [Forgejo bağdaştırıcısı](forgejo-adapter.md), geçmiş ve kurtarma komutlarının sınırları için [history-recovery.md](history-recovery.md) sayfalarına bak. Herhangi bir sürümün paket kabul durumu için o sürümün `verification.json` kaydını incele. Üretim Docker volume'larını araca verme.
