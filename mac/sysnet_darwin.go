//go:build darwin

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	tunAddr   = "198.18.0.1"
	scutilKey = "State:/Network/Service/net.vd.kopru/DNS"
	scutilIP4 = "State:/Network/Service/net.vd.kopru/IPv4"
	stateFile = "/var/run/kopru.json"
)

var splitRoutes = []string{"0.0.0.0/1", "128.0.0.0/1"}

type netState struct {
	Iface string   `json:"iface"`
	Port  int      `json:"port"`
	ADB   string   `json:"adb"`
	DNS   []string `json:"dns"`
}

func sh(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return nil
}

func setupNetwork(st *netState) error {
	if err := sh("ifconfig", st.Iface, tunAddr, tunAddr, "netmask", "255.255.255.255", "up"); err != nil {
		return err
	}
	for _, r := range splitRoutes {
		// Önceki çalışmadan kalmış olabilir; önce sil.
		sh("route", "-n", "delete", "-net", r)
		if err := sh("route", "-n", "add", "-net", r, "-interface", st.Iface); err != nil {
			return err
		}
	}
	if err := setDNS(st.Iface, st.DNS); err != nil {
		return err
	}
	return saveState(st)
}

// setDNS, Köprü'yü macOS'a bir ağ servisi olarak tanıtır (IPv4 + DNS).
// IPv4 kaydı olmadan, Wi-Fi kapalıyken macOS "ağ yok" sanar ve Firefox/Zen
// gibi tarayıcılar kendini çevrimdışı moda alır.
func setDNS(iface string, servers []string) error {
	script := fmt.Sprintf(`d.init
d.add Addresses * %[1]s
d.add DestAddresses * %[1]s
d.add SubnetMasks * 255.255.255.255
d.add InterfaceName %[2]s
d.add ConfirmedInterfaceName %[2]s
d.add Router %[1]s
set %[3]s
d.init
d.add ServerAddresses * %[4]s
d.add SupplementalMatchDomains * ""
set %[5]s
quit
`, tunAddr, iface, scutilIP4, strings.Join(servers, " "), scutilKey)
	cmd := exec.Command("scutil")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("scutil: %v: %s", err, out)
	}
	flushDNS()
	return nil
}

func flushDNS() {
	sh("dscacheutil", "-flushcache")
	sh("killall", "-HUP", "mDNSResponder")
}

func teardownNetwork(st *netState) {
	for _, r := range splitRoutes {
		sh("route", "-n", "delete", "-net", r)
	}
	cmd := exec.Command("scutil")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("remove %s\nremove %s\nquit\n", scutilKey, scutilIP4))
	cmd.Run()
	flushDNS()
	os.Remove(stateFile)
}

func saveState(st *netState) error {
	b, _ := json.Marshal(st)
	return os.WriteFile(stateFile, b, 0o644)
}

// cleanupStale, çökme sonrası kalan rota/DNS/adb ayarlarını temizler.
func cleanupStale() bool {
	b, err := os.ReadFile(stateFile)
	if err != nil {
		return false
	}
	var st netState
	if json.Unmarshal(b, &st) != nil {
		os.Remove(stateFile)
		return false
	}
	teardownNetwork(&st)
	if st.ADB != "" {
		(&ADB{Path: st.ADB}).RemoveForward(st.Port)
	}
	return true
}
