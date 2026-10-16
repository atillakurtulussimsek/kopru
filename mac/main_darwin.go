//go:build darwin

package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/xjasonlyu/tun2socks/v2/core"
	"github.com/xjasonlyu/tun2socks/v2/core/device/tun"
	"github.com/xjasonlyu/tun2socks/v2/log"
	"github.com/xjasonlyu/tun2socks/v2/tunnel"
	"github.com/xjasonlyu/tun2socks/v2/tunnel/statistic"
)

var version = "0.1.0"

func main() {
	port := flag.Int("port", 28080, "telefondaki Köprü sunucusunun portu")
	dns := flag.String("dns", "1.1.1.1,8.8.8.8", "kullanılacak DNS sunucuları (virgülle)")
	serial := flag.String("serial", "", "birden fazla cihaz varsa adb seri numarası")
	adbPath := flag.String("adb", "", "adb yolu (boşsa otomatik bulunur)")
	mtu := flag.Int("mtu", 1500, "TUN arayüzü MTU")
	logLevel := flag.String("log", "warn", "günlük seviyesi: debug, info, warn, error, silent")
	noADB := flag.Bool("no-adb", false, "adb kullanma (port yönlendirmesi elle yapılmışsa)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Köprü %s — Android telefonun mobil verisini USB ile Mac'e paylaşır\n\n", version)
		fmt.Fprintf(os.Stderr, "Kullanım:\n  sudo kopru [seçenekler]\n  sudo kopru cleanup   (çökme sonrası ağ ayarlarını geri al)\n\nSeçenekler:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if os.Geteuid() != 0 {
		// TUN arayüzü ve rota ayarı root ister; kendimizi sudo ile yeniden çalıştır.
		self, _ := os.Executable()
		args := append([]string{"sudo", self}, os.Args[1:]...)
		fmt.Println("Yönetici yetkisi gerekiyor, sudo ile yeniden başlatılıyor…")
		if err := syscall.Exec("/usr/bin/sudo", args, os.Environ()); err != nil {
			fatal("sudo çalıştırılamadı: %v", err)
		}
	}

	if flag.Arg(0) == "cleanup" {
		if cleanupStale() {
			fmt.Println("Kalan ayarlar temizlendi.")
		} else {
			fmt.Println("Temizlenecek bir şey yok.")
		}
		return
	}
	if cleanupStale() {
		fmt.Println("Önceki oturumdan kalan ayarlar temizlendi.")
	}

	lvl, err := log.ParseLevel(*logLevel)
	if err != nil {
		fatal("geçersiz günlük seviyesi: %s", *logLevel)
	}
	if l, err := log.NewLeveled(lvl); err == nil {
		log.SetLogger(l)
	}

	dialer := &KopruDialer{Addr: fmt.Sprintf("127.0.0.1:%d", *port)}
	var adb *ADB

	// 1) Telefonu bul, port yönlendir, uygulamayı başlat.
	if !*noADB {
		p, err := findADB(*adbPath)
		if err != nil {
			fatal("%v", err)
		}
		adb = &ADB{Path: p, Serial: *serial}
		waitForDevice(adb)
		if err := adb.Forward(*port); err != nil {
			fatal("%v", err)
		}
		adb.StartApp(*port)
	}

	fmt.Print("Telefondaki Köprü sunucusu bekleniyor")
	deadline := time.Now().Add(30 * time.Second)
	hinted := false
	for {
		if err := dialer.Ping(2 * time.Second); err == nil {
			break
		}
		if time.Now().After(deadline) {
			fmt.Println()
			fatal("telefondaki sunucuya ulaşılamadı. Uygulamada \"Başlat\"a bastığınızdan ve portun %d olduğundan emin olun.", *port)
		}
		if !hinted && time.Now().After(deadline.Add(-25*time.Second)) {
			fmt.Print(" (telefonda uygulamayı açıp Başlat'a basın)")
			hinted = true
		}
		fmt.Print(".")
		time.Sleep(time.Second)
	}
	fmt.Println(" bağlandı ✓")

	// 2) TUN arayüzünü ve ağ yığınını kur.
	dev, err := tun.Open("utun", uint32(*mtu))
	if err != nil {
		fatal("TUN arayüzü açılamadı: %v", err)
	}
	tunnel.T().SetDialer(dialer)
	tunnel.T().SetUDPTimeout(60 * time.Second)
	stk, err := core.CreateStack(&core.Config{LinkEndpoint: dev, TransportHandler: tunnel.T()})
	if err != nil {
		dev.Close()
		fatal("ağ yığını oluşturulamadı: %v", err)
	}

	st := &netState{Iface: dev.Name(), Port: *port, DNS: splitList(*dns)}
	if adb != nil {
		st.ADB = adb.Path
	}
	shutdown := func() {
		teardownNetwork(st)
		stk.Close()
		dev.Close()
		if adb != nil {
			adb.RemoveForward(*port)
		}
	}
	if err := setupNetwork(st); err != nil {
		shutdown()
		fatal("ağ ayarlanamadı: %v", err)
	}
	fmt.Printf("İnternet telefon üzerinden yönlendiriliyor (%s). Çıkmak için Ctrl+C.\n", st.Iface)

	// 3) Çalışırken durumu izle.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	fails := 0
	for {
		select {
		case <-sig:
			fmt.Println("\nKapatılıyor, ağ ayarları geri alınıyor…")
			shutdown()
			return
		case <-tick.C:
			if err := dialer.Ping(2 * time.Second); err != nil {
				fails++
				if fails == 2 {
					fmt.Print("\r\033[K⚠ Telefon bağlantısı koptu, yeniden bağlanılıyor…")
				}
				if adb != nil {
					if _, state, _ := adb.DeviceState(); state == "device" {
						adb.Forward(*port)
					}
				}
				continue
			}
			if fails >= 2 {
				fmt.Print("\r\033[K✓ Bağlantı geri geldi\n")
			}
			fails = 0
			up, down := statistic.DefaultManager.Now()
			snap := statistic.DefaultManager.Snapshot()
			fmt.Printf("\r\033[K↑ %s/s  ↓ %s/s   toplam ↑ %s ↓ %s   bağlantı: %d",
				human(up), human(down), human(snap.UploadTotal), human(snap.DownloadTotal), len(snap.Connections))
		}
	}
}

func waitForDevice(adb *ADB) {
	last := ""
	for {
		serial, state, err := adb.DeviceState()
		if err != nil {
			fatal("%v", err)
		}
		if state == "device" {
			if adb.Serial == "" {
				adb.Serial = serial
			}
			fmt.Printf("Telefon bulundu: %s\n", serial)
			return
		}
		msg := map[string]string{
			"":             "Telefon bulunamadı. USB kablosunu takın ve Geliştirici seçeneklerinden \"USB hata ayıklama\"yı açın.",
			"unauthorized": "Telefon ekranındaki \"USB hata ayıklamaya izin ver\" sorusunu onaylayın.",
			"offline":      "Telefon çevrimdışı görünüyor; kabloyu çıkarıp yeniden takın.",
		}[state]
		if msg == "" {
			msg = "Cihaz durumu: " + state
		}
		if msg != last {
			fmt.Println(msg)
			last = msg
		}
		time.Sleep(time.Second)
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func human(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGT"[exp])
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "Hata: "+format+"\n", a...)
	os.Exit(1)
}
