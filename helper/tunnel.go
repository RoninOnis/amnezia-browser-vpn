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
	portAuto bool
	relay    *RelayClient

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
	PortAuto      bool   `json:"portAuto,omitempty"`
	Relay         bool   `json:"relay,omitempty"`
	Err           string `json:"error,omitempty"`
}

// handshakeWait — сколько ждём первый handshake на каждом шаге. Живой handshake
// приходит за ~0.5 s, так что 4 s — с большим запасом, зато перебор
// «порт × MTU» целиком укладывается в таймаут запроса из расширения.
const handshakeWait = 4 * time.Second

// TunnelOptions — то, что приходит из расширения поверх профиля.
type TunnelOptions struct {
	MTUOverride  int    // >0 заменяет MTU профиля
	PortOverride int    // >0 заменяет порт профиля
	Auto         bool   // перебирать MTU вниз
	PortFallback bool   // пробовать 443, если штатный порт не ответил
	RelayAddr    string // адрес нашего релея ("ip:443"); пусто — релей не нужен
	RelayToken   string
	RelayOnly    bool // не пробовать прямое подключение (для проверки релея)
}

// PortMtuAttempt — одна попытка подключения: куда и с каким MTU.
type PortMtuAttempt struct {
	Port int
	MTU  int
}

// AttemptPlan строит порядок попыток. Логика из живых замеров: UDP наружу часто
// разрешён только на 443 (QUIC), а корпоративный туннель заодно режет MTU до 1400.
// Поэтому сначала штатный порт с MTU профиля — на нормальной сети это
// единственная попытка, — затем понижение MTU, затем тот же перебор на 443.
// fallbackMTU — потолок MTU для попытки на 443. Если штатный порт молчит и мы уходим
// на 443, то почти наверняка в тракте корпоративный VPN (AnyConnect и подобные) с MTU
// 1400, а то и спутник сверху. Рукопожатие при этом проходит с любым MTU — оно мелкое,
// и подбор MTU по handshake ничего не замечает. Ломаются уже данные: 1376 внутренних
// плюс обфускация AmneziaWG вылезают за 1400 и молча теряются.
const fallbackMTU = 1280

func AttemptPlan(port, mtu int, ladder, portFallback bool) []PortMtuAttempt {
	if mtu <= 0 {
		mtu = 1420
	}
	if port <= 0 {
		port = 443
	}
	var out []PortMtuAttempt
	seen := map[[2]int]bool{}
	add := func(p, m int) {
		if p <= 0 || m <= 0 || seen[[2]int{p, m}] {
			return
		}
		seen[[2]int{p, m}] = true
		out = append(out, PortMtuAttempt{Port: p, MTU: m})
	}
	lower := 0
	for _, v := range []int{1280, 1240, 1200} {
		if v < mtu {
			lower = v
			break
		}
	}
	// Порядок важен: 443 идёт ВТОРЫМ шагом, а не после всей лестницы MTU.
	// Иначе в сети, где штатный порт закрыт, до 443 дело доходит через 16 с —
	// а расширение успевает сдаться и перезапустить подключение раньше.
	add(port, mtu)
	if portFallback && port != 443 {
		m := mtu
		if m > fallbackMTU {
			m = fallbackMTU // запасной путь почти всегда уже с чьим-то VPN в тракте
		}
		add(443, m)
	}
	if ladder && lower > 0 {
		add(port, lower)
		if portFallback && port != 443 {
			add(443, lower)
		}
	}
	return out
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

// awg3Lines — параметры AmneziaWG 3 (protocol_version 3.x). Имена и форматы
// взяты из UAPI amneziawg-go v3: header_protection_key ждёт HEX (в конфиге и в
// vpn:// он в base64), тайминги — диапазон "lo-hi", random_trailers и
// disable_cookies — bool, хотя в конфиге пишут on/off.
//
// Без этих строк сервер на AWG 3.x молча выбрасывает handshake: заголовки
// пакетов не защищены так, как он ждёт, и в логе клиента нет даже ошибки —
// выглядит как «ключ не работает».
func awg3Lines(p *Profile) ([]string, error) {
	a := p.AWG
	var out []string
	if v := strings.TrimSpace(a.HeaderProtectionKey); v != "" && v != "0" {
		hexKey, err := b64ToHexKey(v)
		if err != nil {
			return nil, fmt.Errorf("header protection key: %w", err)
		}
		out = append(out, "header_protection_key="+hexKey+"\n")
	}
	ranges := [][2]string{
		{"content_padding_addition", a.ContentPaddingAddition},
		{"rekey_after_time", a.RekeyAfterTime},
		{"rekey_timeout", a.RekeyTimeout},
		{"reject_after_time", a.RejectAfterTime},
		{"keepalive_timeout", a.KeepaliveTimeout},
		{"max_handshake_attempts", a.MaxHandshakeAttempts},
	}
	for _, kv := range ranges {
		v := strings.TrimSpace(kv[1])
		if v == "" || v == "0" {
			continue
		}
		out = append(out, kv[0]+"="+v+"\n")
	}
	flags := [][2]string{
		{"random_trailers", a.RandomTrailers},
		{"disable_cookies", a.DisableCookies},
	}
	for _, kv := range flags {
		v, ok := onOffValue(kv[1])
		if !ok {
			continue
		}
		out = append(out, kv[0]+"="+v+"\n")
	}
	return out, nil
}

// onOffValue приводит on/off из конфига Amnezia к bool, который ждёт UAPI.
func onOffValue(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes", "1":
		return "true", true
	case "off", "false", "no", "0":
		return "false", true
	}
	return "", false
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
	awg3, err := awg3Lines(p)
	if err != nil {
		return "", err
	}
	for _, l := range awg3 {
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

// StartTunnelAuto поднимает туннель, при auto перебирая пары (порт, MTU), пока не
// появится handshake. portOverride > 0 заменяет порт из профиля.
func StartTunnelAuto(p *Profile, listen string, opts TunnelOptions) (*Tunnel, error) {
	prof := *p
	if opts.MTUOverride > 0 {
		prof.MTU = opts.MTUOverride
	}
	if opts.PortOverride > 0 {
		prof.Port = opts.PortOverride
	}
	if !opts.Auto && !opts.PortFallback {
		return StartTunnel(&prof, listen)
	}
	basePort, baseMTU := prof.Port, prof.MTU
	var lastErr error
	if opts.RelayOnly && opts.RelayAddr != "" {
		return startViaRelay(&prof, listen, opts)
	}
	plan := AttemptPlan(basePort, baseMTU, opts.Auto, opts.PortFallback)
	for i, a := range plan {
		if i > 0 {
			log.Printf("trying port %d mtu %d (попытка %d из %d)", a.Port, a.MTU, i+1, len(plan))
		}
		attempt := prof
		attempt.Port = a.Port
		attempt.MTU = a.MTU
		tun, err := StartTunnel(&attempt, listen)
		if err != nil {
			lastErr = fmt.Errorf("port %d mtu %d: %w", a.Port, a.MTU, err)
			continue
		}
		if tun.WaitHandshake(handshakeWait) {
			tun.mtuAuto = true
			tun.portAuto = a.Port != basePort
			if a.Port != basePort || a.MTU != baseMTU {
				log.Printf("tunnel works with port %d mtu %d (profile had port %d mtu %d)",
					a.Port, a.MTU, basePort, baseMTU)
			}
			return tun, nil
		}
		log.Printf("no handshake on port %d mtu %d in %s, next attempt", a.Port, a.MTU, handshakeWait)
		tun.Close()
		lastErr = fmt.Errorf("no handshake on port %d mtu %d (UDP may be blocked)", a.Port, a.MTU)
	}

	// Последняя попытка — через наш релей на 443/udp: сети, где разрешён только
	// QUIC, иначе не пускают вообще ничего.
	if opts.RelayAddr != "" {
		tun, err := startViaRelay(&prof, listen, opts)
		if err != nil {
			log.Printf("relay attempt failed: %v", err)
			lastErr = err
		} else {
			return tun, nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no handshake on any port/MTU combination")
	}
	return nil, lastErr
}

// startViaRelay поднимает локальный порт релея и запускает туннель в него.
func startViaRelay(prof *Profile, listen string, opts TunnelOptions) (*Tunnel, error) {
	target := net.JoinHostPort(prof.Host, strconv.Itoa(prof.Port))
	client, err := StartRelayClient(opts.RelayAddr, opts.RelayToken, target)
	if err != nil {
		return nil, err
	}
	rc := *prof
	rc.Host, rc.Port = "127.0.0.1", client.conn.LocalAddr().(*net.UDPAddr).Port
	// через релей идёт ещё один хоп, поэтому MTU берём с запасом
	if rc.MTU > 1360 {
		rc.MTU = 1280
	}
	log.Printf("trying relay %s -> %s (mtu %d)", opts.RelayAddr, target, rc.MTU)
	tun, err := StartTunnel(&rc, listen)
	if err != nil {
		client.Close()
		return nil, err
	}
	if !tun.WaitHandshake(handshakeWait) {
		tun.Close()
		client.Close()
		return nil, fmt.Errorf("no handshake through relay %s", opts.RelayAddr)
	}
	tun.relay = client
	tun.mtuAuto = true
	log.Printf("tunnel works through relay %s -> %s (mtu %d)", opts.RelayAddr, target, rc.MTU)
	return tun, nil
}

func (t *Tunnel) Close() {
	t.closeOnce.Do(func() {
		close(t.closing)
		if t.relay != nil {
			t.relay.Close()
		}
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
		PortAuto:    t.portAuto,
		Relay:       t.relay != nil,
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
