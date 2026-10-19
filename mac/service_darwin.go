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
	// Plist bilerek /Library/LaunchDaemons dışında: launchd açılışta kendiliğinden
	// yüklemez, servis yalnız "kopru on" ile çalışır.
	plistPath    = "/usr/local/share/kopru/" + serviceLabel + ".plist"
	oldPlistPath = "/Library/LaunchDaemons/" + serviceLabel + ".plist" // v0.2.x
	installBin   = "/usr/local/bin/kopru"
	logPath      = "/var/log/kopru.log"
	sudoersPath  = "/etc/sudoers.d/kopru"
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
	serviceOff()
	os.Remove(oldPlistPath)

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

	os.MkdirAll(filepath.Dir(plistPath), 0o755)
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		fatal("%s yazılamadı: %v", plistPath, err)
	}
	installSudoers()
	fmt.Println("✓ Köprü kuruldu.")
	serviceOn()
	fmt.Printf(`
  kopru on       Aç   (Dock'ta görünmez, arka planda çalışır)
  kopru off      Kapat
  kopru status   Durum
  Günlük:        %s
  Kaldırma:      sudo kopru uninstall

Mac yeniden başlayınca Köprü kapalı başlar; istediğinizde "kopru on" ile açın.
`, logPath)
}

// installSudoers, yönetici kullanıcıların yalnız "kopru on/off" komutlarını
// şifresiz çalıştırmasına izin verir.
func installSudoers() {
	rule := fmt.Sprintf("# Köprü: şifresiz aç/kapat\n%%admin ALL=(root) NOPASSWD: %[1]s on, %[1]s off\n", installBin)
	tmp := sudoersPath + ".tmp"
	if os.WriteFile(tmp, []byte(rule), 0o440) != nil {
		return
	}
	if exec.Command("visudo", "-cf", tmp).Run() != nil {
		os.Remove(tmp)
		return
	}
	os.Rename(tmp, sudoersPath)
}

func serviceRunning() bool {
	return exec.Command("launchctl", "print", "system/"+serviceLabel).Run() == nil
}

func serviceOn() {
	if !fileExists(plistPath) {
		fatal("Köprü kurulu değil. Önce: sudo ./kopru install")
	}
	if serviceRunning() {
		fmt.Println("Köprü zaten açık.")
		return
	}
	if out, err := exec.Command("launchctl", "bootstrap", "system", plistPath).CombinedOutput(); err != nil {
		fatal("servis başlatılamadı: %v %s", err, out)
	}
	fmt.Println("✓ Köprü açıldı. Wi-Fi yokken telefondaki uygulamada \"Başlat\"a basınca internet telefondan gelir.")
}

func serviceOff() bool {
	if !serviceRunning() {
		return false
	}
	// bootout SIGTERM gönderir; servis ağ ayarlarını geri alıp kapanır.
	exec.Command("launchctl", "bootout", "system/"+serviceLabel).Run()
	for i := 0; i < 20 && serviceRunning(); i++ {
		time.Sleep(250 * time.Millisecond)
	}
	cleanupStale()
	os.Remove(statusFile)
	return true
}

func uninstall() {
	serviceOff()
	exec.Command("launchctl", "bootout", "system/"+serviceLabel).Run()
	removed := false
	for _, p := range []string{plistPath, oldPlistPath, installBin, statusFile, sudoersPath} {
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
