package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const androidComponent = "net.vd.kopru/.MainActivity"

type ADB struct {
	Path   string
	Serial string // boşsa tek bağlı cihaz kullanılır
}

func findADB(custom string) (string, error) {
	if custom != "" {
		return custom, nil
	}
	if p, err := exec.LookPath("adb"); err == nil {
		return p, nil
	}
	home := os.Getenv("HOME")
	if su := os.Getenv("SUDO_USER"); su != "" {
		home = filepath.Join("/Users", su)
	}
	candidates := []string{
		"/opt/homebrew/bin/adb",
		"/usr/local/bin/adb",
		filepath.Join(home, "Library/Android/sdk/platform-tools/adb"),
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("adb bulunamadı. Kurmak için: brew install android-platform-tools")
}

func (a *ADB) run(args ...string) (string, error) {
	full := args
	if a.Serial != "" {
		full = append([]string{"-s", a.Serial}, args...)
	}
	cmd := a.adbCmd(full...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

// DeviceState: "device", "unauthorized", "offline" ya da "" (yok).
func (a *ADB) DeviceState() (serial, state string, err error) {
	cmd := a.adbCmd("devices")
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("adb devices: %w", err)
	}
	var found [][2]string
	for _, line := range strings.Split(string(out), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) >= 2 {
			found = append(found, [2]string{f[0], f[1]})
		}
	}
	if a.Serial != "" {
		for _, d := range found {
			if d[0] == a.Serial {
				return d[0], d[1], nil
			}
		}
		return "", "", nil
	}
	switch len(found) {
	case 0:
		return "", "", nil
	case 1:
		return found[0][0], found[0][1], nil
	default:
		return "", "", errors.New("birden fazla Android cihaz bağlı; --serial ile birini seçin")
	}
}

func (a *ADB) Forward(port int) error {
	spec := fmt.Sprintf("tcp:%d", port)
	out, err := a.run("forward", spec, spec)
	if err != nil {
		return fmt.Errorf("adb forward: %v %s", err, out)
	}
	return nil
}

func (a *ADB) RemoveForward(port int) {
	a.run("forward", "--remove", fmt.Sprintf("tcp:%d", port))
}

// StartApp, telefondaki uygulamayı açar ve sunucuyu otomatik başlatmasını ister.
func (a *ADB) StartApp(port int) {
	a.run("shell", "am", "start", "-n", androidComponent,
		"--ez", "autostart", "true", "--ei", "port", fmt.Sprint(port))
}

// adbCmd, sudo ile çalışırken adb'yi asıl kullanıcı adına çalıştırır;
// aksi halde adb sunucusu root anahtarıyla açılır ve telefon yeniden yetki ister.
func (a *ADB) adbCmd(args ...string) *exec.Cmd {
	if os.Geteuid() == 0 {
		if u := os.Getenv("SUDO_USER"); u != "" && u != "root" {
			return exec.Command("sudo", append([]string{"-u", u, "-H", a.Path}, args...)...)
		}
	}
	return exec.Command(a.Path, args...)
}
