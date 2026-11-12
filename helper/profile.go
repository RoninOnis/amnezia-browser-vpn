package main

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// AWGParams holds AmneziaWG obfuscation parameters.
//
// AWG 1.x: Jc/Jmin/Jmax, S1-S4, H1-H4, I1-I5.
// AWG 3 (protocol_version 3.x): плюс HeaderProtectionKey (защита заголовков),
// ContentPaddingAddition, диапазоны переигровок и таймаутов, RandomTrailers,
// DisableCookies. Без них современный сервер (amnezia-awg2 с AWG 3.x) молча
// выбрасывает handshake, и это выглядит как «ключ не работает».
type AWGParams struct {
	Jc   string `json:"jc,omitempty"`
	Jmin string `json:"jmin,omitempty"`
	Jmax string `json:"jmax,omitempty"`
	S1   string `json:"s1,omitempty"`
	S2   string `json:"s2,omitempty"`
	S3   string `json:"s3,omitempty"`
	S4   string `json:"s4,omitempty"`
	H1   string `json:"h1,omitempty"`
	H2   string `json:"h2,omitempty"`
	H3   string `json:"h3,omitempty"`
	H4   string `json:"h4,omitempty"`
	I1   string `json:"i1,omitempty"`
	I2   string `json:"i2,omitempty"`
	I3   string `json:"i3,omitempty"`
	I4   string `json:"i4,omitempty"`
	I5   string `json:"i5,omitempty"`

	// AWG 3+
	HeaderProtectionKey    string `json:"headerProtectionKey,omitempty"`
	ContentPaddingAddition string `json:"contentPaddingAddition,omitempty"`
	RekeyAfterTime         string `json:"rekeyAfterTime,omitempty"`
	RekeyTimeout           string `json:"rekeyTimeout,omitempty"`
	RejectAfterTime        string `json:"rejectAfterTime,omitempty"`
	KeepaliveTimeout       string `json:"keepaliveTimeout,omitempty"`
	MaxHandshakeAttempts   string `json:"maxHandshakeAttempts,omitempty"`
	RandomTrailers         string `json:"randomTrailers,omitempty"`
	DisableCookies         string `json:"disableCookies,omitempty"`
}

// Profile is everything needed to bring the tunnel up. Sent by the extension
// as JSON; also produced from a vpn:// link or an .conf file.
type Profile struct {
	ID            string    `json:"id,omitempty"`
	Name          string    `json:"name,omitempty"`
	Host          string    `json:"host"`
	Port          int       `json:"port"`
	PrivateKey    string    `json:"privateKey"`
	PeerPublicKey string    `json:"peerPublicKey"`
	PresharedKey  string    `json:"presharedKey,omitempty"`
	ClientIP      string    `json:"clientIp"`
	DNS           []string  `json:"dns,omitempty"`
	MTU           int       `json:"mtu,omitempty"`
	Keepalive     int       `json:"keepalive,omitempty"`
	Container     string    `json:"container,omitempty"`
	AWG           AWGParams `json:"awg,omitempty"`
}

func (p *Profile) normalize() {
	if p.Endpoint() == "" {
		p.Host = strings.TrimSpace(p.Host)
	}
	if p.MTU == 0 {
		p.MTU = 1420
	}
	if p.Keepalive == 0 {
		p.Keepalive = 25
	}
	if len(p.DNS) == 0 {
		p.DNS = []string{"1.1.1.1", "1.0.0.1"}
	}
	if p.Name == "" {
		p.Name = p.Endpoint()
	}
}

// Endpoint returns host:port for the peer.
func (p *Profile) Endpoint() string {
	if p.Port == 0 {
		return ""
	}
	return net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
}

// ParseVpnLink decodes Amnezia's vpn:// share string:
// base64url( uint32be(len(uncompressed)) || zlib(json) ).
func ParseVpnLink(link string) (*Profile, error) {
	s := strings.TrimSpace(link)
	s = strings.TrimPrefix(s, "vpn://")
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty vpn link")
	}
	raw, err := b64Any(s)
	if err != nil {
		return nil, fmt.Errorf("base64: %w", err)
	}
	plain, err := inflateAny(raw)
	if err != nil {
		return nil, fmt.Errorf("inflate: %w", err)
	}
	var doc struct {
		Containers  []map[string]json.RawMessage `json:"containers"`
		Default     string                       `json:"defaultContainer"`
		HostName    string                       `json:"hostName"`
		DNS1        string                       `json:"dns1"`
		DNS2        string                       `json:"dns2"`
		Description string                       `json:"description"`
	}
	if err := json.Unmarshal(plain, &doc); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	if len(doc.Containers) == 0 {
		return nil, fmt.Errorf("no containers in vpn link")
	}

	var awg map[string]any
	container := ""
	for _, c := range doc.Containers {
		if v, ok := c["awg"]; ok {
			var m map[string]any
			if err := json.Unmarshal(v, &m); err == nil {
				awg = m
				if raw, ok := c["container"]; ok {
					_ = json.Unmarshal(raw, &container)
				}
				break
			}
		}
	}
	if awg == nil {
		return nil, fmt.Errorf("vpn link has no amneziawg container (only amneziawg is supported)")
	}

	// last_config carries the concrete client config as a JSON string.
	var lc map[string]any
	if raw, ok := awg["last_config"].(string); ok && raw != "" {
		_ = json.Unmarshal([]byte(raw), &lc)
	}
	// В last_config значения лежат строками, а в самом контейнере те же поля
	// Amnezia пишет числами (Jc: 6, S1: 12). Без числовой ветки обфускация
	// молча терялась бы.
	pick := func(key string) string {
		from := func(v any) string {
			switch t := v.(type) {
			case string:
				return t
			case float64:
				return strconv.FormatFloat(t, 'f', -1, 64)
			case bool:
				if t {
					return "on"
				}
				return "off"
			}
			return ""
		}
		if lc != nil {
			if v, ok := lc[key]; ok {
				if s := from(v); s != "" {
					return s
				}
			}
		}
		if v, ok := awg[key]; ok {
			return from(v)
		}
		return ""
	}

	p := &Profile{
		Name:          doc.Description,
		Host:          pick("hostName"),
		PrivateKey:    pick("client_priv_key"),
		PeerPublicKey: pick("server_pub_key"),
		PresharedKey:  pick("psk_key"),
		ClientIP:      pick("client_ip"),
		Container:     container,
		AWG: AWGParams{
			Jc: pick("Jc"), Jmin: pick("Jmin"), Jmax: pick("Jmax"),
			S1: pick("S1"), S2: pick("S2"), S3: pick("S3"), S4: pick("S4"),
			H1: pick("H1"), H2: pick("H2"), H3: pick("H3"), H4: pick("H4"),
			I1: pick("I1"), I2: pick("I2"), I3: pick("I3"), I4: pick("I4"), I5: pick("I5"),

			HeaderProtectionKey:    pick("HeaderProtectionKey"),
			ContentPaddingAddition: pick("ContentPaddingAddition"),
			RekeyAfterTime:         pick("RekeyAfterTime"),
			RekeyTimeout:           pick("RekeyTimeout"),
			RejectAfterTime:        pick("RejectAfterTime"),
			KeepaliveTimeout:       pick("KeepaliveTimeout"),
			MaxHandshakeAttempts:   pick("MaxHandshakeAttempts"),
			RandomTrailers:         pick("RandomTrailers"),
			DisableCookies:         pick("DisableCookies"),
		},
	}
	if p.Host == "" {
		p.Host = doc.HostName
	}

	// port may arrive as number or string
	portStr := pick("port")
	if portStr == "" {
		if v, ok := awg["port"].(string); ok {
			portStr = v
		}
	}
	if portStr != "" {
		p.Port, _ = strconv.Atoi(strings.TrimSpace(portStr))
	}
	if mtuStr := pick("mtu"); mtuStr != "" {
		p.MTU, _ = strconv.Atoi(mtuStr)
	}
	if kaStr := pick("persistent_keep_alive"); kaStr != "" {
		p.Keepalive, _ = strconv.Atoi(kaStr)
	}
	if doc.DNS1 != "" {
		p.DNS = append(p.DNS, doc.DNS1)
	}
	if doc.DNS2 != "" {
		p.DNS = append(p.DNS, doc.DNS2)
	}
	if p.PrivateKey == "" || p.PeerPublicKey == "" || p.Host == "" || p.Port == 0 || p.ClientIP == "" {
		return nil, fmt.Errorf("vpn link incomplete (missing privateKey/peerPublicKey/host/port/clientIp)")

	}
	p.normalize()
	return p, nil
}

// ParseConf parses a plain WireGuard/AmneziaWG .conf text.
func ParseConf(text string) (*Profile, error) {
	p := &Profile{}
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if section == "interface" {
			switch k {
			case "PrivateKey":
				p.PrivateKey = v
			case "Address":
				addr := strings.Split(v, ",")[0]
				addr = strings.TrimSpace(strings.Split(addr, "/")[0])
				p.ClientIP = addr
			case "DNS":
				for _, d := range strings.Split(v, ",") {
					if d = strings.TrimSpace(d); d != "" && !strings.HasPrefix(d, "$") {
						p.DNS = append(p.DNS, d)
					}
				}
			case "MTU":
				p.MTU, _ = strconv.Atoi(v)
			case "Jc":
				p.AWG.Jc = v
			case "Jmin":
				p.AWG.Jmin = v
			case "Jmax":
				p.AWG.Jmax = v
			case "S1":
				p.AWG.S1 = v
			case "S2":
				p.AWG.S2 = v
			case "S3":
				p.AWG.S3 = v
			case "S4":
				p.AWG.S4 = v
			case "H1":
				p.AWG.H1 = v
			case "H2":
				p.AWG.H2 = v
			case "H3":
				p.AWG.H3 = v
			case "H4":
				p.AWG.H4 = v
			case "I1":
				p.AWG.I1 = v
			case "I2":
				p.AWG.I2 = v
			case "I3":
				p.AWG.I3 = v
			case "I4":
				p.AWG.I4 = v
			case "I5":
				p.AWG.I5 = v
			case "HeaderProtectionKey":
				p.AWG.HeaderProtectionKey = v
			case "ContentPaddingAddition":
				p.AWG.ContentPaddingAddition = v
			case "RekeyAfterTime":
				p.AWG.RekeyAfterTime = v
			case "RekeyTimeout":
				p.AWG.RekeyTimeout = v
			case "RejectAfterTime":
				p.AWG.RejectAfterTime = v
			case "KeepaliveTimeout":
				p.AWG.KeepaliveTimeout = v
			case "MaxHandshakeAttempts":
				p.AWG.MaxHandshakeAttempts = v
			case "RandomTrailers":
				p.AWG.RandomTrailers = v
			case "DisableCookies":
				p.AWG.DisableCookies = v
			}
		} else if section == "peer" {
			switch k {
			case "PublicKey":
				p.PeerPublicKey = v
			case "PresharedKey":
				p.PresharedKey = v
			case "Endpoint":
				host, port, err := net.SplitHostPort(v)
				if err == nil {
					p.Host = host
					p.Port, _ = strconv.Atoi(port)
				}
			case "PersistentKeepalive":
				p.Keepalive, _ = strconv.Atoi(v)
			}
		}
	}
	if p.PrivateKey == "" || p.PeerPublicKey == "" || p.Host == "" || p.Port == 0 || p.ClientIP == "" {
		return nil, fmt.Errorf("conf incomplete")
	}
	p.normalize()
	return p, nil
}

// LoadProfile accepts a vpn:// link, a .conf text, or a JSON profile.
func LoadProfile(raw string) (*Profile, error) {
	t := strings.TrimSpace(raw)
	if strings.HasPrefix(t, "vpn://") || looksLikeVpnBlob(t) {
		if p, err := ParseVpnLink(t); err == nil {
			return p, nil
		} else if !strings.Contains(t, "\n") {
			return nil, err
		}
	}
	if strings.Contains(t, "[Interface]") {
		return ParseConf(t)
	}
	var p Profile
	if err := json.Unmarshal([]byte(t), &p); err != nil {
		return nil, fmt.Errorf("unrecognised config format: %w", err)
	}
	if p.Host == "" || p.PrivateKey == "" {
		return nil, fmt.Errorf("profile missing host/privateKey")
	}
	p.normalize()
	return &p, nil
}

func looksLikeVpnBlob(s string) bool {
	if strings.ContainsAny(s, " \n") || len(s) < 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '=':
		default:
			return false
		}
	}
	return true
}

func b64Any(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

func inflateAny(raw []byte) ([]byte, error) {
	try := func(b []byte) ([]byte, bool) {
		zr, err := zlib.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, false
		}
		defer zr.Close()
		out, err := io.ReadAll(zr)
		if err != nil {
			return nil, false
		}
		return out, true
	}
	if len(raw) > 4 {
		if want := binary.BigEndian.Uint32(raw[:4]); int(want) > 0 {
			if out, ok := try(raw[4:]); ok && len(out) == int(want) {
				return out, nil
			} else if ok {
				return out, nil
			}
		}
	}
	if out, ok := try(raw); ok {
		return out, nil
	}
	if out, ok := try(raw[4:]); ok {
		return out, nil
	}
	return nil, fmt.Errorf("not zlib data")
}
