//go:build darwin

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	serviceLabel = "net.vd.kopru"
	plistPath    = "/Library/LaunchDaemons/" + serviceLabel + ".plist"
	installBin   = "/usr/local/bin/kopru"
	logPath      = "/var/log/kopru.log"
)

// install, Köprü'yü launchd servisi olarak kurar: açılışta başlar, Dock'ta
// ya da menüde görünmez, çökerse launchd yeniden başlatır.
func install(o *options) {
	adbPath, err := findADB(o.adbPath)
	if err != nil {
		fatal("%v", err)
	}
	self, err := os.Executable()
	if err != nil {
		fatal("%v", err)
	}
	self, _ = filepath.EvalSymlinks(self)

	// Eski servis varsa durdur (ikili dosyayı değiştirebilmek için).
	exec.Command("launchctl", "bootout", "system/"+serviceLabel).Run()
	time.Sleep(500 * time.Millisecond)

	if self != installBin {
		if err := copyFile(self, installBin); err != nil {
			fatal("%s kopyalanamadı: %v", installBin, err)
		}
	}

	args := []string{installBin, "daemon", "-port", fmt.Sprint(o.port),
		"-dns", strings.Join(o.dns, ","), "-adb", adbPath, "-log", o.logLevel}
	if o.serial != "" {
		args = append(args, "-serial", o.serial)
	}
	if o.always {
		args = append(args, "-always")
	}
	var argXML strings.Builder
	for _, a := range args {
		fmt.Fprintf(&argXML, "\t\t<string>%s</string>\n", xmlEscape(a))
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
%s	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>5</integer>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
	</dict>
</dict>
</plist>
`, serviceLabel, argXML.String(), logPath, logPath)

	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		fatal("%s yazılamadı: %v", plistPath, err)
	}
	if out, err := exec.Command("launchctl", "bootstrap", "system", plistPath).CombinedOutput(); err != nil {
		fatal("servis başlatılamadı: %v %s", err, out)
	}
	fmt.Printf(`✓ Köprü arka plan servisi kuruldu ve çalışıyor.

  • Mac açıldığında kendiliğinden başlar, Dock'ta görünmez.
  • Mac Wi-Fi'ye bağlıyken Köprü devreye girmez. Wi-Fi yoksa ve telefondaki
    Köprü uygulamasında "Başlat"a basılmışsa internet telefondan gelir.
  • Wi-Fi geri gelince, "Durdur"a basınca ya da kablo çıkınca Mac kendi ağına döner.
  • Durum:    kopru status
  • Günlük:   %s
  • Kaldırma: sudo kopru uninstall
`, logPath)
}

func uninstall() {
	exec.Command("launchctl", "bootout", "system/"+serviceLabel).Run()
	time.Sleep(time.Second)
	removed := false
	for _, p := range []string{plistPath, installBin, statusFile} {
		if os.Remove(p) == nil {
			removed = true
		}
	}
	cleanupStale()
	if removed {
		fmt.Println("✓ Köprü servisi kaldırıldı, ağ ayarları geri alındı.")
	} else {
		fmt.Println("Kurulu bir Köprü servisi bulunamadı.")
	}
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst) // yeni inode: çalışan eski sürümün imza önbelleğiyle çakışmaz
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
