package main

import (
	"net"
	"testing"
	"time"
)

func TestRelayProtocolRoundtrip(t *testing.T) {
	in := relayRequest{Token: "secret-token", Target: "203.0.113.10:36853", Payload: []byte("hello")}
	out, err := decodeRelayRequest(encodeRelayRequest(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Token != in.Token || out.Target != in.Target || string(out.Payload) != string(in.Payload) {
		t.Fatalf("roundtrip: %+v", out)
	}

	code, payload, err := decodeRelayReply(encodeRelayReply(relayNotAllowed, []byte("x")))
	if err != nil || code != relayNotAllowed || string(payload) != "x" {
		t.Fatalf("reply roundtrip: %v %v %q", code, err, payload)
	}

	if _, err := decodeRelayRequest([]byte("garbage")); err == nil {
		t.Fatal("мусорный пакет должен отбрасываться")
	}
	if _, _, err := decodeRelayReply([]byte("garbage")); err == nil {
		t.Fatal("чужой ответ должен отбрасываться")
	}
}

// echo-сервер на loopback, имитирующий AWG-сервер клиента.
func startEcho(t *testing.T) (*net.UDPConn, string) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			conn.WriteToUDP(buf[:n], addr)
		}
	}()
	return conn, conn.LocalAddr().String()
}

// Полный путь: клиент релея -> релей -> «сервер» и обратно.
func TestRelayEndToEnd(t *testing.T) {
	echo, echoAddr := startEcho(t)
	defer echo.Close()

	srv := &RelayServer{Addr: "127.0.0.1:0", Token: "tok", Allowed: map[string]bool{echoAddr: true}}
	if err := srv.Start(); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	defer srv.Close()
	relayAddr := srv.conn.LocalAddr().String()

	client, err := StartRelayClient(relayAddr, "tok", echoAddr)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer client.Close()

	// девайс шлёт пакет в локальный порт релея
	dev, err := net.Dial("udp", client.LocalAddr())
	if err != nil {
		t.Fatalf("dev dial: %v", err)
	}
	defer dev.Close()

	want := []byte("awg-handshake")
	if _, err := dev.Write(want); err != nil {
		t.Fatalf("dev write: %v", err)
	}
	buf := make([]byte, 1500)
	dev.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := dev.Read(buf)
	if err != nil {
		t.Fatalf("ответ через релей не пришёл: %v", err)
	}
	if string(buf[:n]) != string(want) {
		t.Fatalf("получено %q, ожидалось %q", buf[:n], want)
	}
	if !client.Established() {
		t.Fatal("сессия не помечена установленной")
	}
}

func TestRelayRejectsWrongToken(t *testing.T) {
	echo, echoAddr := startEcho(t)
	defer echo.Close()

	srv := &RelayServer{Addr: "127.0.0.1:0", Token: "right", Allowed: map[string]bool{echoAddr: true}}
	if err := srv.Start(); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	defer srv.Close()

	client, err := StartRelayClient(srv.conn.LocalAddr().String(), "wrong", echoAddr)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer client.Close()

	dev, _ := net.Dial("udp", client.LocalAddr())
	defer dev.Close()
	dev.Write([]byte("x"))
	buf := make([]byte, 64)
	dev.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	if _, err := dev.Read(buf); err == nil {
		t.Fatal("с неверным токеном ответа быть не должно")
	}
	if client.Established() {
		t.Fatal("сессия не должна считаться установленной")
	}
}

func TestRelayRejectsTargetOutsideAllowlist(t *testing.T) {
	srv := &RelayServer{Addr: "127.0.0.1:0", Token: "tok", Allowed: map[string]bool{"203.0.113.99:1234": true}}
	if err := srv.Start(); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	defer srv.Close()

	if srv.allowed("203.0.113.10:36853") {
		t.Fatal("адрес вне белого списка должен быть запрещён")
	}
	if !srv.allowed("203.0.113.99:1234") {
		t.Fatal("адрес из белого списка должен быть разрешён")
	}
	if srv.allowed("") {
		t.Fatal("пустой адрес не должен быть разрешён")
	}
}

// Релей без белого списка не должен ничего пересылать.
func TestRelayEmptyAllowlistDeniesAll(t *testing.T) {
	srv := &RelayServer{Addr: "127.0.0.1:0", Token: "tok"}
	if err := srv.Start(); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	defer srv.Close()
	if srv.allowed("203.0.113.10:36853") {
		t.Fatal("пустой белый список должен запрещать всё")
	}
}
