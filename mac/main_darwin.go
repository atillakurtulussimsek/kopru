//go:build darwin

package main

import (
	"context"
	"encoding/json"
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

var version = "0.2.0"

type options struct {
	port     int
	dns      []string
	serial   string
	adbPath  string
	mtu      int
	logLevel string
	noADB    bool
	daemon   bool
}

func usage() {
	fmt.Fprintf(os.Stderr, `Köprü %s — Android telefonun mobil verisini USB ile Mac'e paylaşır

Kullanım:
  sudo kopru              Terminalde çalıştır (Ctrl+C ile çık)
  sudo kopru install      Arka plan servisi olarak kur (açılışta başlar, Dock'ta görünmez)
  sudo kopru uninstall    Arka plan servisini kaldır
  kopru status            Servisin durumunu göster
  sudo kopru cleanup      Çökme sonrası kalan ağ ayarlarını geri al

Seçenekler:
`, version)
}

func parseOptions(args []string) *options {
	fs := flag.NewFlagSet("kopru", flag.ExitOnError)
	o := &options{}
	dns := ""
	fs.IntVar(&o.port, "port", 28080, "telefondaki Köprü sunucusunun portu")
	fs.StringVar(&dns, "dns", "1.1.1.1,8.8.8.8", "kullanılacak DNS sunucuları (virgülle)")
	fs.StringVar(&o.serial, "serial", "", "birden fazla cihaz varsa adb seri numarası")
	fs.StringVar(&o.adbPath, "adb", "", "adb yolu (boşsa otomatik bulunur)")
	fs.IntVar(&o.mtu, "mtu", 1500, "TUN arayüzü MTU")
	fs.StringVar(&o.logLevel, "log", "warn", "günlük seviyesi: debug, info, warn, error, silent")
	fs.BoolVar(&o.noADB, "no-adb", false, "adb kullanma (port yönlendirmesi elle yapılmışsa)")
	fs.Usage = func() { usage(); fs.PrintDefaults() }
	fs.Parse(args)
	o.dns = splitList(dns)
	return o
}

func main() {
	cmd, args := "", os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	o := parseOptions(args)

	switch cmd {
	case "status":
		printStatus()
		return
	case "", "daemon", "install", "uninstall", "cleanup":
	default:
		usage()
		os.Exit(2)
	}

	requireRoot()

	switch cmd {
	case "cleanup":
		if cleanupStale() {
			fmt.Println("Kalan ayarlar temizlendi.")
		} else {
			fmt.Println("Temizlenecek bir şey yok.")
		}
	case "install":
		install(o)
	case "uninstall":
		uninstall()
	case "daemon":
		o.daemon = true
		run(o)
	default:
		run(o)
	}
}

func requireRoot() {
	if os.Geteuid() == 0 {
		return
	}
	// TUN arayüzü ve rota ayarı root ister; kendimizi sudo ile yeniden çalıştır.
	self, _ := os.Executable()
	args := append([]string{"sudo", self}, os.Args[1:]...)
	fmt.Println("Yönetici yetkisi gerekiyor, sudo ile yeniden başlatılıyor…")
	if err := syscall.Exec("/usr/bin/sudo", args, os.Environ()); err != nil {
		fatal("sudo çalıştırılamadı: %v", err)
	}
}

// say, terminal modunda kullanıcıya, servis modunda günlüğe yazar.
func (o *options) say(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if o.daemon {
		fmt.Printf("%s %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
	} else {
		fmt.Printf("\r\033[K%s\n", msg)
	}
}

// run, ana döngüdür: telefon sunucusu hazır olunca tüneli kurar, sunucu
// kaybolunca (kablo çıktı, uygulamada "Durdur"a basıldı) her şeyi geri alır,
// böylece Mac kendi Wi-Fi'sine döner. Sonra yeniden bekler.
func run(o *options) {
	if cleanupStale() {
		o.say("Önceki oturumdan kalan ayarlar temizlendi.")
	}
	lvl, err := log.ParseLevel(o.logLevel)
	if err != nil {
		fatal("geçersiz günlük seviyesi: %s", o.logLevel)
	}
	if l, err := log.NewLeveled(lvl); err == nil {
		log.SetLogger(l)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()

	dialer := &KopruDialer{Addr: fmt.Sprintf("127.0.0.1:%d", o.port)}
	var adb *ADB
	if !o.noADB {
		p, err := findADB(o.adbPath)
		if err != nil {
			if o.daemon {
				writeStatus("hata", "", err.Error())
			}
			fatal("%v", err)
		}
		adb = &ADB{Path: p, Serial: o.serial}
	}

	firstRound := true
	for ctx.Err() == nil {
		writeStatus("bekleniyor", "", "")
		if !waitForServer(ctx, o, adb, dialer, firstRound) {
			break
		}
		firstRound = false
		if err := session(ctx, o, adb, dialer); err != nil {
			o.say("Hata: %v", err)
			writeStatus("hata", "", err.Error())
			if !o.daemon {
				os.Exit(1)
			}
			sleepCtx(ctx, 5*time.Second)
		}
	}
	writeStatus("durdu", "", "")
	if adb != nil {
		adb.RemoveForward(o.port)
	}
	if !o.daemon {
		fmt.Println("\nÇıkıldı, ağ ayarları geri alındı.")
	}
}

// waitForServer, telefon bağlanıp Köprü sunucusu PING'e yanıt verene kadar bekler.
func waitForServer(ctx context.Context, o *options, adb *ADB, d *KopruDialer, first bool) bool {
	lastMsg := ""
	tell := func(msg string) {
		if msg != lastMsg {
			o.say("%s", msg)
			lastMsg = msg
		}
	}
	startedApp := false
	for ctx.Err() == nil {
		if adb != nil && os.Geteuid() == 0 && invokingUser() == "" {
			// Açılışta henüz kimse giriş yapmadı: adb'yi root olarak başlatırsak
			// telefon farklı bir anahtar görür ve yeniden yetki ister. Girişi bekle.
			tell("Kullanıcı girişi bekleniyor…")
			sleepCtx(ctx, 5*time.Second)
			continue
		}
		if adb != nil {
			serial, state, err := adb.DeviceState()
			switch {
			case err != nil:
				tell(err.Error())
			case state == "":
				tell("Telefon bekleniyor (USB kablosu takılı ve USB hata ayıklama açık olmalı)…")
			case state == "unauthorized":
				tell("Telefon ekranındaki \"USB hata ayıklamaya izin ver\" sorusunu onaylayın.")
			case state != "device":
				tell("Telefon durumu: " + state)
			default:
				if o.serial == "" {
					adb.Serial = serial
				}
				adb.Forward(o.port)
				// Terminal modunda ilk seferde uygulamayı biz açarız. Serviste açmayız:
				// telefon şarj için takıldığında mobil veri izinsiz harcanmasın;
				// paylaşımı telefondaki "Başlat" düğmesi başlatır.
				if !o.daemon && first && !startedApp {
					adb.StartApp(o.port)
					startedApp = true
				}
				if d.Ping(2*time.Second) == nil {
					return true
				}
				if o.daemon {
					tell("Telefon bağlı; paylaşım için telefondaki Köprü uygulamasında \"Başlat\"a basın.")
				} else {
					tell("Telefondaki Köprü sunucusu bekleniyor (uygulamada \"Başlat\"a basın)…")
				}
			}
		} else if d.Ping(2*time.Second) == nil {
			return true
		} else {
			tell(fmt.Sprintf("127.0.0.1:%d üzerinde sunucu bekleniyor…", o.port))
		}
		sleepCtx(ctx, 2*time.Second)
	}
	return false
}

// session, tüneli kurar ve sunucu kaybolana ya da çıkış istenene kadar çalışır.
func session(ctx context.Context, o *options, adb *ADB, d *KopruDialer) error {
	dev, err := tun.Open("utun", uint32(o.mtu))
	if err != nil {
		return fmt.Errorf("TUN arayüzü açılamadı: %w", err)
	}
	tunnel.T().SetDialer(d)
	tunnel.T().SetUDPTimeout(60 * time.Second)
	stk, err := core.CreateStack(&core.Config{LinkEndpoint: dev, TransportHandler: tunnel.T()})
	if err != nil {
		dev.Close()
		return fmt.Errorf("ağ yığını oluşturulamadı: %w", err)
	}
	st := &netState{Iface: dev.Name(), Port: o.port, DNS: o.dns}
	if adb != nil {
		st.ADB = adb.Path
	}
	defer func() {
		teardownNetwork(st)
		stk.Close()
		dev.Close()
	}()
	if err := setupNetwork(st); err != nil {
		return fmt.Errorf("ağ ayarlanamadı: %w", err)
	}
	statistic.DefaultManager.ResetStatistic()
	since := time.Now()
	if o.daemon {
		o.say("Bağlandı: internet telefon üzerinden (%s)", st.Iface)
	} else {
		o.say("✓ İnternet telefon üzerinden yönlendiriliyor (%s). Çıkmak için Ctrl+C.", st.Iface)
	}
	writeStatus("bağlı", st.Iface, "")

	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	var lostAt time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		if err := d.Ping(2 * time.Second); err != nil {
			if lostAt.IsZero() {
				lostAt = time.Now()
				if !o.daemon {
					fmt.Print("\r\033[K⚠ Telefon yanıt vermiyor…")
				}
			}
			if adb != nil {
				if _, state, _ := adb.DeviceState(); state == "device" {
					adb.Forward(o.port)
				}
			}
			// Kısa kopmalarda tüneli koru; uzun sürerse Mac'i kendi ağına bırak.
			if time.Since(lostAt) > 8*time.Second {
				o.say("Telefon bağlantısı kesildi; ağ ayarları geri alındı, yeniden bekleniyor.")
				return nil
			}
			continue
		}
		if !lostAt.IsZero() {
			o.say("✓ Bağlantı geri geldi")
			lostAt = time.Time{}
		}
		snap := statistic.DefaultManager.Snapshot()
		writeStatusFull(statusInfo{
			State: "bağlı", Iface: st.Iface, Since: since,
			Up: snap.UploadTotal, Down: snap.DownloadTotal, Conns: len(snap.Connections),
		})
		if !o.daemon {
			up, down := statistic.DefaultManager.Now()
			fmt.Printf("\r\033[K↑ %s/s  ↓ %s/s   toplam ↑ %s ↓ %s   bağlantı: %d",
				human(up), human(down), human(snap.UploadTotal), human(snap.DownloadTotal), len(snap.Connections))
		}
	}
}

// --- Durum dosyası (kopru status) ---

const statusFile = "/var/run/kopru.status"

type statusInfo struct {
	State   string    `json:"state"`
	Iface   string    `json:"iface,omitempty"`
	Error   string    `json:"error,omitempty"`
	Since   time.Time `json:"since,omitempty"`
	Up      int64     `json:"up"`
	Down    int64     `json:"down"`
	Conns   int       `json:"conns"`
	Updated time.Time `json:"updated"`
}

func writeStatus(state, iface, errMsg string) {
	writeStatusFull(statusInfo{State: state, Iface: iface, Error: errMsg})
}

func writeStatusFull(s statusInfo) {
	s.Updated = time.Now()
	b, _ := json.Marshal(s)
	os.WriteFile(statusFile, b, 0o644)
}

func printStatus() {
	installed := fileExists(plistPath)
	b, err := os.ReadFile(statusFile)
	var s statusInfo
	if err != nil || json.Unmarshal(b, &s) != nil || time.Since(s.Updated) > 2*time.Minute && s.State == "bağlı" {
		s = statusInfo{State: "çalışmıyor"}
	}
	fmt.Printf("Servis: %s\n", map[bool]string{true: "kurulu", false: "kurulu değil"}[installed])
	fmt.Printf("Durum:  %s\n", s.State)
	if s.State == "bağlı" {
		fmt.Printf("Arayüz: %s, %s süredir\n", s.Iface, time.Since(s.Since).Round(time.Second))
		fmt.Printf("Trafik: ↑ %s  ↓ %s  (%d bağlantı)\n", human(s.Up), human(s.Down), s.Conns)
	}
	if s.Error != "" {
		fmt.Printf("Hata:   %s\n", s.Error)
	}
	if installed {
		fmt.Printf("Günlük: %s\n", logPath)
	}
}

// --- yardımcılar ---

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

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
