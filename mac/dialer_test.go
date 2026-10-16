package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	M "github.com/xjasonlyu/tun2socks/v2/metadata"
)

// KOPRU_SERVER=127.0.0.1:PORT ile çalışan bir Köprü sunucusuna karşı uçtan uca test.
func server(t *testing.T) *KopruDialer {
	addr := os.Getenv("KOPRU_SERVER")
	if addr == "" {
		t.Skip("KOPRU_SERVER ayarlı değil")
	}
	return &KopruDialer{Addr: addr}
}

func TestPing(t *testing.T) {
	d := server(t)
	if err := d.Ping(2 * time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestTCPEcho(t *testing.T) {
	d := server(t)
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
	ap := netip.MustParseAddrPort(ln.Addr().String())

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ { // eşzamanlı bağlantılar
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := d.DialContext(context.Background(), &M.Metadata{Network: M.TCP, DstIP: ap.Addr(), DstPort: ap.Port()})
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			data := make([]byte, 2<<20)
			rand.Read(data)
			go func() { c.Write(data); c.(*net.TCPConn).CloseWrite() }()
			got, err := io.ReadAll(c)
			if err != nil || !bytes.Equal(got, data) {
				t.Errorf("veri uyuşmuyor: %d/%d %v", len(got), len(data), err)
			}
		}()
	}
	wg.Wait()
}

func TestTCPRefused(t *testing.T) {
	d := server(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ap := netip.MustParseAddrPort(ln.Addr().String())
	ln.Close()
	_, err := d.DialContext(context.Background(), &M.Metadata{Network: M.TCP, DstIP: ap.Addr(), DstPort: ap.Port()})
	if err == nil {
		t.Fatal("hata bekleniyordu")
	}
	t.Log("beklenen hata:", err)
}

func TestUDPTunnel(t *testing.T) {
	d := server(t)
	pc, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer pc.Close()
	go func() {
		buf := make([]byte, 65535)
		for {
			n, a, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo(append([]byte("echo:"), buf[:n]...), a)
		}
	}()
	ap := netip.MustParseAddrPort(pc.LocalAddr().String())
	u, err := d.DialUDP(&M.Metadata{Network: M.UDP, DstIP: ap.Addr(), DstPort: ap.Port()})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	for _, size := range []int{1, 512, 1400, 9000} {
		msg := bytes.Repeat([]byte{'x'}, size)
		if _, err := u.WriteTo(msg, nil); err != nil {
			t.Fatal(err)
		}
		u.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 65535)
		n, from, err := u.ReadFrom(buf)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(buf[:n], append([]byte("echo:"), msg...)) {
			t.Fatalf("boyut %d: yanlış yanıt", size)
		}
		if from.String() != ap.String() {
			t.Fatalf("kaynak adres %s, beklenen %s", from, ap)
		}
	}
}
