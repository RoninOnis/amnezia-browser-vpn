package main

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

// testKey produces a fake but structurally valid 32-byte WireGuard key.
func testKey(b byte) string {
	raw := bytes.Repeat([]byte{b}, 32)
	return base64.StdEncoding.EncodeToString(raw)
}

// buildVpnLink assembles an Amnezia-style vpn:// payload:
// base64url( uint32be(len(uncompressed)) || zlib(json) ).
func buildVpnLink(t *testing.T, doc map[string]any) string {
	t.Helper()
	plain, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var zbuf bytes.Buffer
	zw := zlib.NewWriter(&zbuf)
	if _, err := zw.Write(plain); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	var out bytes.Buffer
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(plain)))
	out.Write(hdr[:])
	out.Write(zbuf.Bytes())
	return "vpn://" + base64.RawURLEncoding.EncodeToString(out.Bytes())
}

func fixtureDoc() map[string]any {
	last := map[string]any{
		"H1":                    "111111111-222222222",
		"H2":                    "333333333-444444444",
		"H3":                    "555555555-666666666",
		"H4":                    "777777777-888888888",
		"I1":                    "<r 2><b 0x0102>",
		"Jc":                    "6",
		"Jmin":                  "10",
		"Jmax":                  "50",
		"S1":                    "144",
		"S2":                    "66",
		"S3":                    "46",
		"S4":                    "16",
		"client_priv_key":       testKey(1),
		"server_pub_key":        testKey(2),
		"psk_key":               testKey(3),
		"client_ip":             "10.99.7.14",
		"hostName":              "203.0.113.10",
		"port":                  float64(40000),
		"mtu":                   "1376",
		"persistent_keep_alive": "25",
	}
	lastJSON, _ := json.Marshal(last)
	return map[string]any{
		"containers": []any{
			map[string]any{
				"container": "amnezia-awg2",
				"awg": map[string]any{
					"H1":              "111111111-222222222",
					"Jc":              "6",
					"last_config":     string(lastJSON),
					"port":            "40000",
					"transport_proto": "udp",
				},
			},
		},
		"description":      "test-profile",
		"dns1":             "1.1.1.1",
		"dns2":             "1.0.0.1",
		"hostName":         "203.0.113.10",
		"defaultContainer": "amnezia-awg2",
	}
}

func TestParseVpnLink(t *testing.T) {
	link := buildVpnLink(t, fixtureDoc())
	p, err := ParseVpnLink(link)
	if err != nil {
		t.Fatalf("ParseVpnLink: %v", err)
	}
	if p.Host != "203.0.113.10" || p.Port != 40000 {
		t.Fatalf("endpoint = %s:%d", p.Host, p.Port)
	}
	if p.PrivateKey != testKey(1) || p.PeerPublicKey != testKey(2) || p.PresharedKey != testKey(3) {
		t.Fatalf("keys not mapped")
	}
	if p.ClientIP != "10.99.7.14" {
		t.Fatalf("client ip = %q", p.ClientIP)
	}
	if p.MTU != 1376 || p.Keepalive != 25 {
		t.Fatalf("mtu/keepalive = %d/%d", p.MTU, p.Keepalive)
	}
	if len(p.DNS) != 2 || p.DNS[0] != "1.1.1.1" {
		t.Fatalf("dns = %v", p.DNS)
	}
	if p.Container != "amnezia-awg2" {
		t.Fatalf("container = %q", p.Container)
	}
	if p.AWG.Jc != "6" || p.AWG.S3 != "46" || p.AWG.H4 != "777777777-888888888" || p.AWG.I1 != "<r 2><b 0x0102>" {
		t.Fatalf("awg params not mapped: %+v", p.AWG)
	}
	if p.Endpoint() != "203.0.113.10:40000" {
		t.Fatalf("Endpoint() = %q", p.Endpoint())
	}
}

func TestParseVpnLinkRejectsNonAWG(t *testing.T) {
	doc := map[string]any{
		"containers":  []any{map[string]any{"container": "amnezia-openvpn", "openvpn": map[string]any{}}},
		"description": "ovpn",
	}
	if _, err := ParseVpnLink(buildVpnLink(t, doc)); err == nil {
		t.Fatal("expected error for non-AWG container")
	}
}

func TestIpcSetIncludesObfuscation(t *testing.T) {
	p, err := ParseVpnLink(buildVpnLink(t, fixtureDoc()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ipc, err := p.ipcSet()
	if err != nil {
		t.Fatalf("ipcSet: %v", err)
	}
	for _, want := range []string{
		"private_key=", "jc=6", "jmin=10", "jmax=50",
		"s1=144", "s2=66", "s3=46", "s4=16",
		"h1=111111111-222222222", "i1=<r 2><b 0x0102>",
		"public_key=", "preshared_key=",
		"endpoint=203.0.113.10:40000",
		"persistent_keepalive_interval=25",
		"allowed_ip=0.0.0.0/0",
	} {
		if !strings.Contains(ipc, want) {
			t.Errorf("ipc missing %q\n%s", want, ipc)
		}
	}
}

func TestObfLinesSkipsDisabled(t *testing.T) {
	p := &Profile{AWG: AWGParams{Jc: "0", S1: "", H1: "0-0", H2: "5-10"}}
	got := strings.Join(obfLines(p), "")
	if strings.Contains(got, "jc=") || strings.Contains(got, "s1=") || strings.Contains(got, "h1=") {
		t.Fatalf("disabled params leaked into ipc: %q", got)
	}
	if !strings.Contains(got, "h2=5-10") {
		t.Fatalf("h2 missing: %q", got)
	}
}

func TestParseConf(t *testing.T) {
	conf := `[Interface]
Address = 10.99.7.14/32
DNS = 1.1.1.1, 1.0.0.1
PrivateKey = ` + testKey(4) + `
Jc = 5
S1 = 10
H1 = 1-2

[Peer]
PublicKey = ` + testKey(5) + `
PresharedKey = ` + testKey(6) + `
AllowedIPs = 0.0.0.0/0
Endpoint = 203.0.113.11:51820
PersistentKeepalive = 25
`
	p, err := ParseConf(conf)
	if err != nil {
		t.Fatalf("ParseConf: %v", err)
	}
	if p.ClientIP != "10.99.7.14" || p.Host != "203.0.113.11" || p.Port != 51820 {
		t.Fatalf("conf mapping wrong: %+v", p)
	}
	if p.AWG.Jc != "5" || p.AWG.H1 != "1-2" {
		t.Fatalf("awg params: %+v", p.AWG)
	}
}
