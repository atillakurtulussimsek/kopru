package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	M "github.com/xjasonlyu/tun2socks/v2/metadata"
)

// Köprü protokolü: standart SOCKS5 el sıkışması + iki özel komut.
//
//	0x01 CONNECT     – standart SOCKS5 TCP bağlantısı
//	0x80 UDP_TUNNEL  – tek hedefe UDP; TCP akışı içinde [2 bayt uzunluk][veri] çerçeveleri
//	0x81 PING        – sunucu canlı mı? (yanıt sonrası bağlantı kapanır)
const (
	socksVer     = 0x05
	cmdConnect   = 0x01
	cmdUDPTunnel = 0x80
	cmdPing      = 0x81
)

var socksReplyErrors = map[byte]string{
	0x01: "genel sunucu hatası",
	0x02: "izin verilmedi",
	0x03: "ağa ulaşılamıyor",
	0x04: "hosta ulaşılamıyor",
	0x05: "bağlantı reddedildi",
	0x06: "TTL süresi doldu",
	0x07: "komut desteklenmiyor",
	0x08: "adres tipi desteklenmiyor",
}

// KopruDialer, tun2socks'un proxy.Dialer arayüzünü uygular.
type KopruDialer struct {
	Addr string // genelde 127.0.0.1:<adb forward portu>
}

func (d *KopruDialer) handshake(ctx context.Context, cmd byte, dst netip.AddrPort) (net.Conn, error) {
	var nd net.Dialer
	c, err := nd.DialContext(ctx, "tcp", d.Addr)
	if err != nil {
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	} else {
		c.SetDeadline(time.Now().Add(10 * time.Second))
	}

	req := []byte{socksVer, 1, 0x00} // greeting: yalnız "kimlik doğrulama yok"
	req = append(req, socksVer, cmd, 0x00)
	ip := dst.Addr().Unmap()
	if ip.Is4() {
		req = append(req, 0x01)
	} else {
		req = append(req, 0x04)
	}
	req = append(req, ip.AsSlice()...)
	req = binary.BigEndian.AppendUint16(req, dst.Port())
	if _, err := c.Write(req); err != nil {
		c.Close()
		return nil, err
	}

	var g [2]byte
	if _, err := io.ReadFull(c, g[:]); err != nil {
		c.Close()
		return nil, err
	}
	if g[0] != socksVer || g[1] != 0x00 {
		c.Close()
		return nil, errors.New("geçersiz SOCKS el sıkışması")
	}
	var h [4]byte
	if _, err := io.ReadFull(c, h[:]); err != nil {
		c.Close()
		return nil, err
	}
	if h[1] != 0x00 {
		c.Close()
		msg := socksReplyErrors[h[1]]
		if msg == "" {
			msg = fmt.Sprintf("kod %d", h[1])
		}
		return nil, errors.New(msg)
	}
	var skip int
	switch h[3] {
	case 0x01:
		skip = 4 + 2
	case 0x04:
		skip = 16 + 2
	case 0x03:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			c.Close()
			return nil, err
		}
		skip = int(l[0]) + 2
	default:
		c.Close()
		return nil, errors.New("geçersiz adres tipi")
	}
	if _, err := io.CopyN(io.Discard, c, int64(skip)); err != nil {
		c.Close()
		return nil, err
	}
	c.SetDeadline(time.Time{})
	return c, nil
}

func (d *KopruDialer) DialContext(ctx context.Context, m *M.Metadata) (net.Conn, error) {
	return d.handshake(ctx, cmdConnect, m.DestinationAddrPort())
}

func (d *KopruDialer) DialUDP(m *M.Metadata) (net.PacketConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := d.handshake(ctx, cmdUDPTunnel, m.DestinationAddrPort())
	if err != nil {
		return nil, err
	}
	return &udpTunnelConn{Conn: c, remote: net.UDPAddrFromAddrPort(m.DestinationAddrPort())}, nil
}

// Ping, telefondaki sunucunun ayakta olup olmadığını kontrol eder.
func (d *KopruDialer) Ping(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c, err := d.handshake(ctx, cmdPing, netip.AddrPortFrom(netip.IPv4Unspecified(), 0))
	if err != nil {
		return err
	}
	return c.Close()
}

// udpTunnelConn, tek bir UDP akışını TCP üzerinde çerçeveleyerek taşır.
type udpTunnelConn struct {
	net.Conn
	remote *net.UDPAddr
	wmu    sync.Mutex
	rmu    sync.Mutex
}

func (u *udpTunnelConn) ReadFrom(p []byte) (int, net.Addr, error) {
	u.rmu.Lock()
	defer u.rmu.Unlock()
	var hdr [2]byte
	if _, err := io.ReadFull(u.Conn, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n <= len(p) {
		if _, err := io.ReadFull(u.Conn, p[:n]); err != nil {
			return 0, nil, err
		}
		return n, u.remote, nil
	}
	// Tampondan büyük datagram: kırp (UDP semantiği).
	if _, err := io.ReadFull(u.Conn, p); err != nil {
		return 0, nil, err
	}
	if _, err := io.CopyN(io.Discard, u.Conn, int64(n-len(p))); err != nil {
		return 0, nil, err
	}
	return len(p), u.remote, nil
}

func (u *udpTunnelConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	if len(p) > 0xFFFF {
		return 0, errors.New("datagram çok büyük")
	}
	buf := make([]byte, 2+len(p))
	binary.BigEndian.PutUint16(buf, uint16(len(p)))
	copy(buf[2:], p)
	u.wmu.Lock()
	defer u.wmu.Unlock()
	if _, err := u.Conn.Write(buf); err != nil {
		return 0, err
	}
	return len(p), nil
}
