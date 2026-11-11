package main

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ключ пира должен быть зажат так же, как `wg genkey`: иначе base64 не совпадёт
// с выводом awg и профиль не примут привычные инструменты.
func TestNewPeerKeysClamped(t *testing.T) {
	keys, err := newPeerKeys()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(keys.PrivateKey)
	if err != nil {
		t.Fatalf("приватный ключ не base64: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("приватный ключ %d байт вместо 32", len(raw))
	}
	if raw[0]&7 != 0 {
		t.Fatalf("младшие 3 бита не зажаты: %08b", raw[0])
	}
	if raw[31]&128 != 0 {
		t.Fatalf("старший бит не зажат: %08b", raw[31])
	}
	if raw[31]&64 != 64 {
		t.Fatalf("бит 6 не выставлен: %08b", raw[31])
	}
	pub, err := base64.StdEncoding.DecodeString(keys.PublicKey)
	if err != nil || len(pub) != 32 {
		t.Fatalf("публичный ключ должен быть 32 байта base64: %v", err)
	}
	psk, err := base64.StdEncoding.DecodeString(keys.Preshared)
	if err != nil || len(psk) != 32 {
		t.Fatalf("PSK должен быть 32 байта base64: %v", err)
	}
	if keys.PrivateKey == keys.Preshared {
		t.Fatal("PSK не должен совпадать с приватным ключом")
	}
}

func decodeVPNLink(t *testing.T, link string) map[string]any {
	t.Helper()
	if !strings.HasPrefix(link, "vpn://") {
		t.Fatalf("ссылка не начинается с vpn://: %s", link[:20])
	}
	body, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(link, "vpn://"))
	if err != nil {
		t.Fatalf("base64url: %v", err)
	}
	if len(body) < 5 {
		t.Fatal("слишком короткая ссылка")
	}
	wantLen := binary.BigEndian.Uint32(body[:4])
	r, err := zlib.NewReader(bytes.NewReader(body[4:]))
	if err != nil {
		t.Fatalf("zlib: %v", err)
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("распаковка: %v", err)
	}
	if uint32(len(raw)) != wantLen {
		t.Fatalf("длина в заголовке %d, распаковано %d", wantLen, len(raw))
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	return doc
}

func sampleList() *PeerList {
	return &PeerList{
		Container:    "amnezia-awg2",
		Config:       "awg0.conf",
		ListenPort:   36853,
		Subnet:       "10.8.1.0/24",
		ServerKey:    "REREREREREREREREREREREREREREREREREREREREREQ=",
		Params:       []string{"Jc=5", "Jmin=50", "Jmax=1000", "S1=40", "S2=60", "H1=1234567", "H2=2345678"},
		EndpointHost: "vpn.example.com",
	}
}

// Главная проверка: ссылка, собранная панелью, разбирается обратно и содержит
// ровно то, без чего клиент не подключится.
func TestBuildVPNLinkRoundTrip(t *testing.T) {
	keys := PeerKeys{
		PrivateKey: "CLIENTPRIV0000000000000000000000000000000=",
		PublicKey:  "CLIENTPUB00000000000000000000000000000000=",
		Preshared:  "CLIENTPSK00000000000000000000000000000000=",
	}
	info := &PeerInfo{PublicKey: keys.PublicKey, AllowedIPs: "10.8.1.7/32", Name: "ноутбук"}
	link, err := buildVPNLink(sampleList(), keys, info)
	if err != nil {
		t.Fatalf("сборка ссылки: %v", err)
	}
	doc := decodeVPNLink(t, link)

	containers, _ := doc["containers"].([]any)
	if len(containers) != 1 {
		t.Fatalf("ожидался один контейнер: %v", doc["containers"])
	}
	awgEntry, _ := containers[0].(map[string]any)
	awg, _ := awgEntry["awg"].(map[string]any)
	lastStr, _ := awg["last_config"].(string)
	if lastStr == "" {
		t.Fatal("нет вложенного last_config")
	}
	var last map[string]any
	if err := json.Unmarshal([]byte(lastStr), &last); err != nil {
		t.Fatalf("last_config не JSON: %v", err)
	}
	checks := map[string]string{
		"hostName":        "vpn.example.com",
		"port":            "36853",
		"client_priv_key": keys.PrivateKey,
		"server_pub_key":  "REREREREREREREREREREREREREREREREREREREREREQ=",
		"psk_key":         keys.Preshared,
		"client_ip":       "10.8.1.7",
		"Jc":              "5",
		"H1":              "1234567",
	}
	for k, want := range checks {
		if got, _ := last[k].(string); got != want {
			t.Fatalf("%s = %q, ожидалось %q", k, got, want)
		}
	}
	if dns1, _ := doc["dns1"].(string); dns1 == "" {
		t.Fatal("нет DNS в ссылке")
	}
}

// Без публичного ключа сервера клиент не подключится — лучше отказать явно.
func TestBuildVPNLinkRefusesWithoutServerKey(t *testing.T) {
	list := sampleList()
	list.ServerKey = ""
	if _, err := buildVPNLink(list, PeerKeys{PrivateKey: "a", Preshared: "b"}, &PeerInfo{AllowedIPs: "10.8.1.7/32"}); err == nil {
		t.Fatal("ссылка без ключа сервера не должна собираться")
	}
}

// Без выданного адреса собирать нечего.
func TestBuildVPNLinkRefusesWithoutClientIP(t *testing.T) {
	if _, err := buildVPNLink(sampleList(), PeerKeys{PrivateKey: "a", Preshared: "b"}, &PeerInfo{}); err == nil {
		t.Fatal("ссылка без client_ip не должна собираться")
	}
}

// Нулевые и отключённые параметры обфускации не должны попадать в профиль:
// иначе клиент попытается применить «выключенную» обфускацию и handshake не пройдёт.
func TestBuildVPNLinkSkipsDisabledParams(t *testing.T) {
	list := sampleList()
	list.Params = append(list.Params, "I1=0-0", "I2=0")
	link, err := buildVPNLink(list, PeerKeys{PrivateKey: "K=", Preshared: "P="}, &PeerInfo{AllowedIPs: "10.8.1.7/32"})
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeVPNLink(t, link)
	containers := doc["containers"].([]any)
	awg := containers[0].(map[string]any)["awg"].(map[string]any)
	var last map[string]any
	if err := json.Unmarshal([]byte(awg["last_config"].(string)), &last); err != nil {
		t.Fatal(err)
	}
	if _, ok := last["I1"]; ok {
		t.Fatal("отключённый I1=0-0 попал в профиль")
	}
	if _, ok := last["I2"]; ok {
		t.Fatal("нулевой I2 попал в профиль")
	}
}

// Фикстура для node-теста: расширение обязано разбирать ссылку, собранную
// панелью. Держим её в репозитории, чтобы формат не разъехался незаметно.
func TestWritePanelLinkFixture(t *testing.T) {
	// настоящие по форме ключи: 32 байта в base64, иначе фикстура проверяла бы
	// строку, которую реальный клиент никогда не получит
	keys := PeerKeys{
		PrivateKey: "ERERERERERERERERERERERERERERERERERERERERERE=",
		PublicKey:  "IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiI=",
		Preshared:  "MzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzM=",
	}
	link, err := buildVPNLink(sampleList(), keys, &PeerInfo{AllowedIPs: "10.8.1.9/32", Name: "панель"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("..", "extension", "tests", "fixtures")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Skipf("каталог фикстур недоступен: %v", err)
	}
	path := filepath.Join(dir, "panel-link.txt")
	if err := os.WriteFile(path, []byte(link+"\n"), 0o644); err != nil {
		t.Fatalf("запись фикстуры: %v", err)
	}
}
