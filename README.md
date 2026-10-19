# Köprü

Android telefonun mobil verisini USB kablosuyla Mac'e paylaşan, PdaNet benzeri bir araç. Root gerekmez.

```
Mac uygulamaları ──► utun (tun2socks) ──► 127.0.0.1:28080 ──adb forward/USB──► Telefon: Köprü sunucusu ──► mobil veri
```

- **Telefon (`android/`)**: Ön planda çalışan bir servis, `127.0.0.1:28080` üzerinde SOCKS5 tabanlı Köprü sunucusunu açar. Dış bağlantılar Wi-Fi açık olsa bile hücresel ağa bağlanır (ayardan kapatılabilir). Trafik operatöre normal telefon trafiği olarak görünür.
- **Mac (`mac/`)**: Tek dosyalık Go programı. `adb forward` ile USB tünelini kurar, telefondaki uygulamayı otomatik başlatır, bir `utun` arayüzü açar, tüm IPv4 trafiğini ve DNS'i bu arayüze yönlendirir. Çıkışta her şeyi geri alır.

## Protokol

Standart SOCKS5 el sıkışması ve iki özel komut kullanılır:

| Komut | Anlamı |
|---|---|
| `0x01` CONNECT | Standart TCP bağlantısı |
| `0x80` UDP_TUNNEL | Tek hedefe UDP. Veriler TCP içinde `[2 bayt uzunluk][veri]` çerçeveleriyle taşınır. `adb forward` yalnız TCP taşıdığı için gerekli; DNS, QUIC ve oyunlar bununla çalışır. |
| `0x81` PING | Canlılık kontrolü |

## Kurulum

### 1. Telefon
1. Ayarlar › Telefon hakkında › **Yapı numarası**na 7 kez dokunun. Geliştirici seçeneklerinden **USB hata ayıklama**yı açın.
2. `android/` klasörünü Android Studio ile açıp telefona yükleyin. Komut satırından yüklemek için: `./gradlew installDebug`.

### 2. Mac
```bash
brew install android-platform-tools      # adb
xattr -d com.apple.quarantine ./kopru    # indirilen dosyanın karantinasını kaldır
sudo ./kopru
```
İlk bağlantıda telefonda çıkan **"USB hata ayıklamaya izin ver"** sorusunu onaylayın ("Her zaman izin ver" işaretli olsun). Program telefondaki uygulamayı kendisi açar ve başlatır.

### 3. Arka plan servisi (önerilen)
Terminal açık tutmadan, Dock'ta görünmeden çalışması için:
```bash
sudo ./kopru install
```
Servis Mac açıldığında kendiliğinden başlar. **Wi-Fi (ya da başka bir bağlantı) varken devreye girmez.** Wi-Fi yokken:
- Köprü uygulamasında **Başlat** → Mac internete telefondan çıkar.
- **Durdur**, kabloyu çıkarmak ya da Wi-Fi'nin geri gelmesi → Mac birkaç saniye içinde kendi ağına döner.

Wi-Fi bağlıyken de telefonu kullanmak isterseniz: `sudo kopru install -always`.

Telefonu sadece şarj için taktığınızda mobil veri harcanmaz; paylaşım yalnız uygulamada Başlat'a basınca açılır.

`kopru status` durumu gösterir, günlük `/var/log/kopru.log` dosyasındadır, `sudo kopru uninstall` servisi kaldırır.

Terminal modunda kapatmak için `Ctrl+C`. Program beklenmedik biçimde kapanırsa `sudo ./kopru cleanup` rota ve DNS ayarlarını geri alır. Bir sonraki çalıştırmada bu temizlik otomatik olarak da yapılır.

Seçenekler: `-port`, `-dns 1.1.1.1,8.8.8.8`, `-serial` (birden fazla telefon bağlıysa), `-log debug`, `-no-adb`.

## Kaynaktan derleme

```bash
cd mac && make            # dist/kopru (Intel + Apple Silicon)
cd android && ./gradlew assembleRelease
```

Not: `mac/go.mod` içindeki `replace` satırları, golang.org proxy'sine erişilemeyen ortamlar için eklenmiş GitHub aynalarıdır. Normal bir Mac'te bu satırları silip `go mod tidy` çalıştırabilirsiniz.

## Bilinen sınırlar (v0.1)
- Yalnız IPv4 tünellenir. Mac'in başka bir IPv6 bağlantısı varsa IPv6 trafiği o bağlantıdan gider.
- Wi-Fi modu henüz yok, yalnız USB destekleniyor.
- Tethering'in hat sözleşmesine göre kısıtlı olup olmadığını kontrol etmek kullanıcının sorumluluğundadır.
