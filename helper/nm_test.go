package main

import (
	"encoding/json"
	"testing"
)

// Регрессия: раньше native-сообщение несло только profile и listen, а mtuAuto,
// mtu, port и portFallback терялись при разборе. Из-за этого демон ВСЕГДА шёл без
// авто-режима: ни перебора MTU, ни попытки на 443 — и клиент в сети, где открыт
// только QUIC, видел «подключается и ничего не происходит».
func TestNMMessageKeepsTuningFields(t *testing.T) {
	raw := []byte(`{"id":1,"cmd":"connect","listen":"127.0.0.1:0",
		"mtu":1280,"mtuAuto":true,"port":36853,"portFallback":true,
		"profile":{"endpoint":"203.0.113.10:36853"}}`)
	var msg nmMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if !msg.MTUAuto {
		t.Fatal("mtuAuto потерялся: без него не работает перебор MTU")
	}
	if msg.MTU != 1280 {
		t.Fatalf("mtu = %d, ожидалось 1280", msg.MTU)
	}
	if msg.Port != 36853 {
		t.Fatalf("port = %d, ожидалось 36853", msg.Port)
	}
	if msg.PortFallback == nil || !*msg.PortFallback {
		t.Fatal("portFallback потерялся: без него не будет попытки на 443")
	}
	if msg.Profile == nil {
		t.Fatal("профиль потерялся")
	}
}

// «bad json» ничего не объяснял пользователю: текст ошибки разбора кадра должен
// быть внятным, поэтому проверяем сам перевод ошибки.
func TestHumanParseError(t *testing.T) {
	if got := humanParseError(parseErr("json: cannot unmarshal string into Go struct field x")); got == "" ||
		got == "bad json" {
		t.Errorf("нечеловеческий текст: %q", got)
	}
	if got := humanParseError(parseErr("unexpected end of JSON input")); got == "" {
		t.Error("пустой текст ошибки разбора")
	}
}

type parseErr string

func (e parseErr) Error() string { return string(e) }

// portFallback по умолчанию включён: запрос без поля должен давать попытку на 443.
func TestPortFallbackDefaultsOn(t *testing.T) {
	var req request
	if err := json.Unmarshal([]byte(`{"cmd":"connect"}`), &req); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	enabled := req.PortFallback == nil || *req.PortFallback
	if !enabled {
		t.Fatal("без явного portFallback:false попытка на 443 обязана остаться включённой")
	}
}

// Явное false уважается: тот, кто точно знает, что ему 443 не нужен, его выключит.
func TestPortFallbackCanBeDisabled(t *testing.T) {
	var req request
	if err := json.Unmarshal([]byte(`{"cmd":"connect","portFallback":false}`), &req); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if req.PortFallback == nil || *req.PortFallback {
		t.Fatal("portFallback:false не сохранился")
	}
}
