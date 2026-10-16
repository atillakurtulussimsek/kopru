//go:build linux && itest

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/xjasonlyu/tun2socks/v2/core"
	"github.com/xjasonlyu/tun2socks/v2/core/device/tun"
	M "github.com/xjasonlyu/tun2socks/v2/metadata"
	"github.com/xjasonlyu/tun2socks/v2/tunnel"
)

// 198.19.0.0/24'e giden trafik TUN'dan geçer; test dialer'ı hedefi 127.0.0.1'e çevirir
// (döngüye girmemek için), gerisi gerçek Köprü protokolüyle telefondaki sunucuya gider.
type rewrite struct{ *KopruDialer }

func fix(m *M.Metadata) *M.Metadata {
	c := *m
	c.DstIP = netip.MustParseAddr("127.0.0.1")
	return &c
}
func (r rewrite) DialContext(ctx context.Context, m *M.Metadata) (net.Conn, error) {
	return r.KopruDialer.DialContext(ctx, fix(m))
}
func (r rewrite) DialUDP(m *M.Metadata) (net.PacketConn, error) {
	pc, err := r.KopruDialer.DialUDP(fix(m))
	if err != nil {
		return nil, err
	}
	pc.(*udpTunnelConn).remote = net.UDPAddrFromAddrPort(m.DestinationAddrPort())
	return pc, nil
}

func TestThroughTUN(t *testing.T) {
	dev, err := tun.Open("kt0", 1500)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	tunnel.T().SetDialer(rewrite{&KopruDialer{Addr: os.Getenv("KOPRU_SERVER")}})
	stk, err := core.CreateStack(&core.Config{LinkEndpoint: dev, TransportHandler: tunnel.T()})
	if err != nil {
		t.Fatal(err)
	}
	defer stk.Close()
	for _, c := range [][]string{{"addr", "add", "198.18.0.1/32", "dev", "kt0"}, {"link", "set", "kt0", "up"}, {"route", "add", "198.19.0.0/24", "dev", "kt0"}} {
		if out, err := exec.Command(os.Getenv("IPBIN"), c...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}

	// TCP: yerel echo sunucusu, TUN üzerinden 198.19.0.5:<port> ile erişilir.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	port := netip.MustParseAddrPort(ln.Addr().String()).Port()
	c, err := net.DialTimeout("tcp", netip.AddrPortFrom(netip.MustParseAddr("198.19.0.5"), port).String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 8<<20)
	rand.Read(data)
	start := time.Now()
	go func() { c.Write(data); c.(*net.TCPConn).CloseWrite() }()
	got, _ := io.ReadAll(c)
	if !bytes.Equal(got, data) {
		t.Fatalf("TCP veri uyuşmuyor %d", len(got))
	}
	t.Logf("TCP 8 MiB gidiş-dönüş: %v", time.Since(start))

	// UDP (DNS benzeri)
	pc, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer pc.Close()
	go func() {
		buf := make([]byte, 2000)
		for {
			n, a, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo(bytes.ToUpper(buf[:n]), a)
		}
	}()
	up := netip.MustParseAddrPort(pc.LocalAddr().String()).Port()
	uc, err := net.Dial("udp", netip.AddrPortFrom(netip.MustParseAddr("198.19.0.6"), up).String())
	if err != nil {
		t.Fatal(err)
	}
	uc.Write([]byte("merhaba"))
	uc.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 100)
	n, err := uc.Read(buf)
	if err != nil || string(buf[:n]) != "MERHABA" {
		t.Fatalf("UDP: %q %v", buf[:n], err)
	}
}
