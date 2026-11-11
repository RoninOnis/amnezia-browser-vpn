package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/netstack"
)

// Tunnel is a running userspace AmneziaWG client with a local SOCKS5 listener.
// No TUN device, no root, no system route changes.
type Tunnel struct {
	Profile   Profile
	SocksHost string
	SocksPort int

	dev      *device.Device
	tnet     *netstack.Net
	listener net.Listener
	started  time.Time
	mtuAuto  bool

	closeOnce sync.Once
	closing   chan struct{}
}

type TunnelStatus struct {
	Running       bool   `json:"running"`
	State         string `json:"state"` // stopped | connecting | connected | error
	SocksHost     string `json:"socksHost,omitempty"`
	SocksPort     int    `json:"socksPort,omitempty"`
	ProfileName   string `json:"profileName,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	Interface     string `json:"interface,omitempty"`
	LastHandshake int64  `json:"lastHandshakeUnix,omitempty"`
	RxBytes       int64  `json:"rxBytes"`
	TxBytes       int64  `json:"txBytes"`
	UptimeSec     int64  `json:"uptimeSec"`
	MTU           int    `json:"mtu,omitempty"`
	MTUAuto       bool   `json:"mtuAuto,omitempty"`
	Err           string `json:"error,omitempty"`
}

// handshakeWait — сколько ждём первый handshake на каждом шаге лестницы MTU.
const handshakeWait = 8 * time.Second

// MTULadder — значения MTU, которые пробуем по порядку. Корпоративные туннели
// (AnyConnect держит 1400) молча роняют наши пакеты, если MTU профиля больше
// полезной нагрузки, поэтому спускаемся вниз до первого handshake.
func MTULadder(base int) []int {
	if base <= 0 {
		base = 1420
	}
	ladder := []int{base}
	for _, v := range []int{1280, 1240, 1200} {
		if v < base {
			ladder = append(ladder, v)
		}
	}
	return ladder
}

func b64ToHexKey(s string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("base64: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("key must be 32 bytes, got %d", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

func obfLines(p *Profile) []string {
	a := p.AWG
	pairs := [][2]string{
		{"jc", a.Jc}, {"jmin", a.Jmin}, {"jmax", a.Jmax},
		{"s1", a.S1}, {"s2", a.S2}, {"s3", a.S3}, {"s4", a.S4},
		{"h1", a.H1}, {"h2", a.H2}, {"h3", a.H3}, {"h4", a.H4},
		{"i1", a.I1}, {"i2", a.I2}, {"i3", a.I3}, {"i4", a.I4}, {"i5", a.I5},
	}
	var out []string
	for _, kv := range pairs {
		v := strings.TrimSpace(kv[1])
		if v == "" || v == "0" {
			continue
		}
		// 0-0 ranges mean "obfuscation disabled" on some servers.
		if (kv[0][0] == 'h') && v == "0-0" {
			continue
		}
		out = append(out, fmt.Sprintf("%s=%s\n", kv[0], v))
	}
	return out
}

func (p *Profile) ipcSet() (string, error) {
	var b strings.Builder
	privHex, err := b64ToHexKey(p.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("privateKey: %w", err)
	}
	fmt.Fprintf(&b, "private_key=%s\n", privHex)
	for _, l := range obfLines(p) {
		b.WriteString(l)
	}

	pubHex, err := b64ToHexKey(p.PeerPublicKey)
	if err != nil {
		return "", fmt.Errorf("peerPublicKey: %w", err)
	}
	fmt.Fprintf(&b, "public_key=%s\n", pubHex)
	if p.PresharedKey != "" {
		pskHex, err := b64ToHexKey(p.PresharedKey)
		if err != nil {
			return "", fmt.Errorf("presharedKey: %w", err)
		}
		fmt.Fprintf(&b, "preshared_key=%s\n", pskHex)
	}

	endpoint := p.Endpoint()
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", fmt.Errorf("endpoint %q: %w", endpoint, err)
	}
	if net.ParseIP(host) == nil {
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return "", fmt.Errorf("resolve endpoint %s: %v", host, err)
		}
		var pick net.IP
		for _, ip := range ips {
			if ip.To4() != nil {
				pick = ip
				break
			}
		}
		if pick == nil {
			pick = ips[0]
		}
		host = pick.String()
	}
	fmt.Fprintf(&b, "endpoint=%s\n", net.JoinHostPort(host, port))
	fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", p.Keepalive)
	fmt.Fprintf(&b, "allowed_ip=0.0.0.0/0\n")
	fmt.Fprintf(&b, "allowed_ip=::/0\n")
	return b.String(), nil
}

// StartTunnel brings the AmneziaWG device up and starts the SOCKS5 listener.
// listen is like "127.0.0.1:0" (0 = ephemeral port).
func StartTunnel(p *Profile, listen string) (*Tunnel, error) {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return nil, fmt.Errorf("listen %q: %w", listen, err)
	}
	if host != "127.0.0.1" && host != "::1" && host != "localhost" {
		return nil, fmt.Errorf("SOCKS listener must bind loopback, got %q", host)
	}

	clientIP, err := netip.ParseAddr(strings.TrimSpace(p.ClientIP))
	if err != nil {
		return nil, fmt.Errorf("clientIp %q: %w", p.ClientIP, err)
	}
	var dns []netip.Addr
	for _, d := range p.DNS {
		if a, err := netip.ParseAddr(strings.TrimSpace(d)); err == nil {
			dns = append(dns, a)
		}
	}

	ipc, err := p.ipcSet()
	if err != nil {
		return nil, err
	}

	tunDev, tnet, err := netstack.CreateNetTUN([]netip.Addr{clientIP}, dns, p.MTU)
	if err != nil {
		return nil, fmt.Errorf("netstack: %w", err)
	}

	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), device.NewLogger(device.LogLevelError, "(awgbv) "))
	if err := dev.IpcSet(ipc); err != nil {
		return nil, fmt.Errorf("device ipc: %w", err)
	}
	if err := dev.Up(); err != nil {
		return nil, fmt.Errorf("device up: %w", err)
	}

	ln, err := newSocksListener(tnet, listen)
	if err != nil {
		dev.Close()
		return nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port

	t := &Tunnel{
		Profile:   *p,
		SocksHost: "127.0.0.1",
		SocksPort: port,
		dev:       dev,
		tnet:      tnet,
		listener:  ln,
		started:   time.Now(),
		mtuAuto:   false,
		closing:   make(chan struct{}),
	}
	go func() {
		<-t.closing
		ln.Close()
		dev.Close()
	}()
	log.Printf("tunnel up: %s -> %s, mtu %d, socks5 127.0.0.1:%d", p.ClientIP, p.Endpoint(), p.MTU, port)
	go t.watchHandshake()
	return t, nil
}

// hasHandshake reports whether the peer has completed at least one handshake.
func (t *Tunnel) hasHandshake() bool {
	raw, err := t.dev.IpcGet()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(raw, "\n") {
		if v, ok := strings.CutPrefix(line, "last_handshake_time_sec="); ok {
			if n, _ := strconv.ParseInt(v, 10, 64); n > 0 {
				return true
			}
		}
	}
	return false
}

// WaitHandshake blocks until the first handshake arrives or the timeout passes.
func (t *Tunnel) WaitHandshake(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if t.hasHandshake() {
			return true
		}
		select {
		case <-t.closing:
			return false
		case <-time.After(250 * time.Millisecond):
		}
	}
	return false
}

// watchHandshake writes the outcome of the first handshake to the log. Раньше
// лог молчал об этом, и по нему нельзя было отличить «туннель поднялся» от
// «пакеты уходят в никуда».
func (t *Tunnel) watchHandshake() {
	start := time.Now()
	warned := false
	for {
		select {
		case <-t.closing:
			return
		default:
		}
		if t.hasHandshake() {
			log.Printf("handshake ok after %s (mtu %d, endpoint %s)",
				time.Since(start).Round(time.Millisecond), t.Profile.MTU, t.Profile.Endpoint())
			return
		}
		elapsed := time.Since(start)
		st := t.Status()
		if !warned && elapsed > 15*time.Second {
			warned = true
			log.Printf("WARNING: no handshake in 15s (endpoint %s, mtu %d, tx=%d rx=%d) — "+
				"похоже, UDP не проходит или MTU слишком большой",
				t.Profile.Endpoint(), t.Profile.MTU, st.TxBytes, st.RxBytes)
		}
		if elapsed > 60*time.Second {
			log.Printf("WARNING: still no handshake after 60s (endpoint %s, mtu %d)",
				t.Profile.Endpoint(), t.Profile.MTU)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// StartTunnelAuto поднимает туннель, при auto=true перебирая MTU вниз, пока не
// появится handshake. Возвращает туннель и MTU, на котором получилось.
func StartTunnelAuto(p *Profile, listen string, mtuOverride int, auto bool) (*Tunnel, error) {
	prof := *p
	if mtuOverride > 0 {
		prof.MTU = mtuOverride
	}
	if !auto {
		return StartTunnel(&prof, listen)
	}
	var lastErr error
	for _, m := range MTULadder(prof.MTU) {
		attempt := prof
		attempt.MTU = m
		tun, err := StartTunnel(&attempt, listen)
		if err != nil {
			lastErr = fmt.Errorf("mtu %d: %w", m, err)
			continue
		}
		if tun.WaitHandshake(handshakeWait) {
			tun.mtuAuto = true
			if m != prof.MTU {
				log.Printf("tunnel works with mtu %d instead of %d", m, prof.MTU)
			}
			return tun, nil
		}
		log.Printf("no handshake with mtu %d in %s, trying lower", m, handshakeWait)
		tun.Close()
		lastErr = fmt.Errorf("no handshake with mtu %d (UDP may be blocked)", m)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no handshake on any MTU value")
	}
	return nil, lastErr
}

func (t *Tunnel) Close() {
	t.closeOnce.Do(func() {
		close(t.closing)
	})
}

// Status reports live device statistics (handshake, counters).
func (t *Tunnel) Status() TunnelStatus {
	st := TunnelStatus{
		Running:     true,
		State:       "connected",
		SocksHost:   t.SocksHost,
		SocksPort:   t.SocksPort,
		ProfileName: t.Profile.Name,
		Endpoint:    t.Profile.Endpoint(),
		Interface:   t.Profile.ClientIP,
		UptimeSec:   int64(time.Since(t.started).Seconds()),
		MTU:         t.Profile.MTU,
		MTUAuto:     t.mtuAuto,
	}
	raw, err := t.dev.IpcGet()
	if err != nil {
		st.State, st.Err = "error", err.Error()
		return st
	}
	seenHandshake := false
	for _, line := range strings.Split(raw, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "last_handshake_time_sec":
			n, _ := strconv.ParseInt(v, 10, 64)
			if n > 0 {
				st.LastHandshake = n
				seenHandshake = true
			}
		case "rx_bytes":
			n, _ := strconv.ParseInt(v, 10, 64)
			st.RxBytes += n
		case "tx_bytes":
			n, _ := strconv.ParseInt(v, 10, 64)
			st.TxBytes += n
		case "endpoint":
			if v != "" && v != "(none)" {
				st.Endpoint = v
			}
		}
	}
	if !seenHandshake {
		st.State = "connecting"
	}
	return st
}

// dialThroughTunnel is the dialer used by the SOCKS5 server: connections are
// terminated inside the gVisor stack, so they egress through the AWG peer.
func (t *Tunnel) dialThroughTunnel(ctx context.Context, network, addr string) (net.Conn, error) {
	return t.tnet.DialContext(ctx, network, addr)
}
