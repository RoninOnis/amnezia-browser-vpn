package main

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// 32-байтные ключи: b64ToHexKey принимает только такую длину.
var (
	testHeaderKeyB64 = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x27}, 32))
	testPrivKeyB64   = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x11}, 32))
	testPubKeyB64    = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x22}, 32))
	testPSKKeyB64    = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x33}, 32))
)

// vpnLinkFromDoc собирает ссылку vpn:// так же, как это делает Amnezia:
// base64url(uint32be(len) || zlib(json)).
func vpnLinkFromDoc(t *testing.T, doc map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var buf bytes.Buffer
	w, err := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	if err != nil {
		t.Fatalf("zlib: %v", err)
	}
	if _, err := w.Write(raw); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	out := make([]byte, 4, 4+buf.Len())
	binary.BigEndian.PutUint32(out, uint32(len(raw)))
	out = append(out, buf.Bytes()...)
	return "vpn://" + base64.RawURLEncoding.EncodeToString(out)
}

// Сервер на AmneziaWG 3.x: обфускация приходит строками в last_config и числами
// в самом контейнере, плюс защита заголовков и тайминги. Раньше всё это
// терялось, handshake молча не проходил — «ключ не работает».
func TestParseVpnLinkAWG3(t *testing.T) {
	last, _ := json.Marshal(map[string]any{
		"hostName":               "vpn.example.net",
		"port":                   "31060",
		"client_priv_key":        testPrivKeyB64,
		"server_pub_key":         testPubKeyB64,
		"psk_key":                testPSKKeyB64,
		"client_ip":              "10.8.1.6",
		"mtu":                    "1376",
		"Jc":                     "6",
		"Jmin":                   "10",
		"Jmax":                   "50",
		"S1":                     "12",
		"S2":                     "12",
		"S3":                     "12",
		"S4":                     "12",
		"H1":                     "1",
		"H2":                     "2",
		"H3":                     "3",
		"H4":                     "4",
		"HeaderProtectionKey":    testHeaderKeyB64,
		"RekeyAfterTime":         "100-120",
		"RekeyTimeout":           "3-7",
		"RejectAfterTime":        "150-180",
		"KeepaliveTimeout":       "5-15",
		"MaxHandshakeAttempts":   "15-20",
		"RandomTrailers":         "on",
		"DisableCookies":         "on",
		"ContentPaddingAddition": "5-15",
	})
	doc := map[string]any{
		"description":      "Poland",
		"hostName":         "vpn.example.net",
		"defaultContainer": "amnezia-awg2",
		"containers": []any{map[string]any{
			"container": "amnezia-awg2",
			"awg": map[string]any{
				// как в реальной ссылке Amnezia — числа, а не строки
				"Jc":                  float64(6),
				"Jmin":                float64(10),
				"Jmax":                float64(50),
				"S1":                  float64(12),
				"S2":                  float64(12),
				"S3":                  float64(12),
				"S4":                  float64(12),
				"H1":                  float64(1),
				"H2":                  float64(2),
				"H3":                  float64(3),
				"H4":                  float64(4),
				"HeaderProtectionKey": testHeaderKeyB64,
				"port":                float64(31060),
				"last_config":         string(last),
			},
		}},
	}

	p, err := ParseVpnLink(vpnLinkFromDoc(t, doc))
	if err != nil {
		t.Fatalf("ParseVpnLink: %v", err)
	}
	if p.AWG.HeaderProtectionKey != testHeaderKeyB64 {
		t.Errorf("HeaderProtectionKey = %q", p.AWG.HeaderProtectionKey)
	}
	if p.AWG.RekeyAfterTime != "100-120" || p.AWG.RejectAfterTime != "150-180" {
		t.Errorf("тайминги не разобраны: %+v", p.AWG)
	}
	if p.AWG.KeepaliveTimeout != "5-15" || p.AWG.MaxHandshakeAttempts != "15-20" {
		t.Errorf("keepalive/max handshake не разобраны: %+v", p.AWG)
	}
	if p.AWG.RandomTrailers != "on" || p.AWG.DisableCookies != "on" {
		t.Errorf("флаги не разобраны: %+v", p.AWG)
	}
	if p.AWG.ContentPaddingAddition != "5-15" {
		t.Errorf("ContentPaddingAddition = %q", p.AWG.ContentPaddingAddition)
	}
	if p.Port != 31060 || p.Host != "vpn.example.net" || p.ClientIP != "10.8.1.6" {
		t.Errorf("базовые поля: %+v", p)
	}

	// и то же самое должно уехать в демон
	ipc, err := p.ipcSet()
	if err != nil {
		t.Fatalf("ipcSet: %v", err)
	}
	hexWant, err := b64ToHexKey(testHeaderKeyB64)
	if err != nil {
		t.Fatalf("b64ToHexKey: %v", err)
	}
	for _, line := range []string{
		"header_protection_key=" + hexWant,
		"rekey_after_time=100-120",
		"rekey_timeout=3-7",
		"reject_after_time=150-180",
		"keepalive_timeout=5-15",
		"max_handshake_attempts=15-20",
		"random_trailers=true",
		"disable_cookies=true",
		"content_padding_addition=5-15",
		"jc=6",
		"s1=12",
	} {
		if !strings.Contains(ipc, line) {
			t.Errorf("в ipcSet нет %q\n---\n%s", line, ipc)
		}
	}
	if strings.Contains(ipc, "random_trailers=on") || strings.Contains(ipc, "random_trailers=off") {
		t.Errorf("on/off не переведены в bool:\n%s", ipc)
	}
}

// Конфиг без AWG 3 не должен обзаводиться лишними строками.
func TestIpcSetAWG1NoAWG3Lines(t *testing.T) {
	p := &Profile{
		Host: "vpn.example.net", Port: 31060,
		PrivateKey:    testPrivKeyB64,
		PeerPublicKey: testPubKeyB64,
		ClientIP:      "10.8.1.6",
		AWG:           AWGParams{Jc: "6", S1: "12"},
	}
	ipc, err := p.ipcSet()
	if err != nil {
		t.Fatalf("ipcSet: %v", err)
	}
	for _, bad := range []string{"header_protection_key=", "rekey_after_time=", "random_trailers="} {
		if strings.Contains(ipc, bad) {
			t.Errorf("в AWG 1 появилась строка %q:\n%s", bad, ipc)
		}
	}
}

// Битый ключ защиты заголовков — громкая ошибка, а не молчаливое подключение
// без защиты (иначе снова «ключ не работает» и поиск вслепую).
func TestAWG3BadHeaderKeyFails(t *testing.T) {
	p := &Profile{
		Host: "vpn.example.net", Port: 31060,
		PrivateKey:    testPrivKeyB64,
		PeerPublicKey: testPubKeyB64,
		ClientIP:      "10.8.1.6",
		AWG:           AWGParams{HeaderProtectionKey: "не-base64"},
	}
	if _, err := p.ipcSet(); err == nil {
		t.Fatal("ожидалась ошибка про header protection key")
	}
}

func TestParseConfAWG3(t *testing.T) {
	conf := fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = 10.8.1.6/32
DNS = 1.1.1.1
Jc = 6
Jmin = 10
Jmax = 50
S1 = 12
S2 = 12
S3 = 12
S4 = 12
H1 = 1
H2 = 2
H3 = 3
H4 = 4
HeaderProtectionKey = %s
RekeyAfterTime = 100-120
RekeyTimeout = 3-7
RejectAfterTime = 150-180
KeepaliveTimeout = 5-15
MaxHandshakeAttempts = 15-20
RandomTrailers = on
DisableCookies = on

[Peer]
PublicKey = %s
PresharedKey = %s
Endpoint = vpn.example.net:31060
AllowedIPs = 0.0.0.0/0
`, testPrivKeyB64, testHeaderKeyB64, testPubKeyB64, testPSKKeyB64)

	p, err := ParseConf(conf)
	if err != nil {
		t.Fatalf("ParseConf: %v", err)
	}
	if p.AWG.HeaderProtectionKey != testHeaderKeyB64 {
		t.Errorf("HeaderProtectionKey = %q", p.AWG.HeaderProtectionKey)
	}
	if p.AWG.RekeyAfterTime != "100-120" || p.AWG.DisableCookies != "on" || p.AWG.MaxHandshakeAttempts != "15-20" {
		t.Errorf("AWG 3 из .conf разобран неверно: %+v", p.AWG)
	}
	ipc, err := p.ipcSet()
	if err != nil {
		t.Fatalf("ipcSet: %v", err)
	}
	if !strings.Contains(ipc, "header_protection_key=") || !strings.Contains(ipc, "disable_cookies=true") {
		t.Errorf("AWG 3 из .conf не доехал до демона:\n%s", ipc)
	}
}

func TestOnOffValue(t *testing.T) {
	cases := map[string]string{"on": "true", "off": "false", "ON": "true", "true": "true", "0": "false", "  off  ": "false"}
	for in, want := range cases {
		got, ok := onOffValue(in)
		if !ok || got != want {
			t.Errorf("onOffValue(%q) = %q, %v; ожидалось %q", in, got, ok, want)
		}
	}
	if _, ok := onOffValue("может быть"); ok {
		t.Error("мусор не должен превращаться в bool")
	}
}
