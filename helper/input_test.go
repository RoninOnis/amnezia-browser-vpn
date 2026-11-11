package main

import "testing"

// Адрес сервера вводят люди, а не API-клиенты: вставляют из браузера целиком,
// с путём, с портом внутри строки, с пробелами. Всё это должно превращаться в
// рабочий host:port, иначе человек получает подсказку не про свою ошибку.
func TestNormalizeHost(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantPort int
	}{
		{"vpn.example.com", "vpn.example.com", 0},
		{"  vpn.example.com  ", "vpn.example.com", 0},
		{"https://vpn.example.com", "vpn.example.com", 0},
		{"http://vpn.example.com/", "vpn.example.com", 0},
		{"vpn.example.com/", "vpn.example.com", 0},
		{"https://vpn.example.com/admin?x=1", "vpn.example.com", 0},
		{"ssh://root@vpn.example.com:2222", "vpn.example.com", 2222},
		{"vpn.example.com:2222", "vpn.example.com", 2222},
		{"213.165.34.238", "213.165.34.238", 0},
		{"213.165.34.238:22", "213.165.34.238", 22},
		{"[2a01::1]", "2a01::1", 0},
		{"[2a01::1]:2222", "2a01::1", 2222},
		{"", "", 0},
		{"   ", "", 0},
	}
	for _, c := range cases {
		host, port := normalizeHost(c.in)
		if host != c.wantHost || port != c.wantPort {
			t.Errorf("normalizeHost(%q) = %q,%d; хотим %q,%d", c.in, host, port, c.wantHost, c.wantPort)
		}
	}
}

// Поле порта может приехать пустым или строкой — это пользователь стёр значение
// или вставил его из заметок. Разбор не должен падать.
func TestFlexiblePort(t *testing.T) {
	good := map[string]int{`22`: 22, `"22"`: 22, `""`: 0, `null`: 0, `0`: 0, `65535`: 65535}
	for raw, want := range good {
		var p flexiblePort
		if err := p.UnmarshalJSON([]byte(raw)); err != nil {
			t.Fatalf("flexiblePort(%s) неожиданная ошибка: %v", raw, err)
		}
		if p.Int() != want {
			t.Errorf("flexiblePort(%s) = %d, хотим %d", raw, p.Int(), want)
		}
	}
	for _, raw := range []string{`"двадцать два"`, `"22abc"`} {
		var p flexiblePort
		if err := p.UnmarshalJSON([]byte(raw)); err == nil {
			t.Errorf("flexiblePort(%s) должен был отказать", raw)
		}
	}
}

// Порт, случайно попавший в строку адреса, не должен молча теряться, а явный
// порт из поля — не перебиваться им.
func TestEffectivePort(t *testing.T) {
	if got := effectivePort(0, 2222); got != 2222 {
		t.Errorf("порт из адреса: получили %d, хотим 2222", got)
	}
	if got := effectivePort(22, 2222); got != 22 {
		t.Errorf("явный порт должен побеждать: получили %d, хотим 22", got)
	}
	if got := effectivePort(0, 0); got != 22 {
		t.Errorf("по умолчанию 22, получили %d", got)
	}
}

// «bad json» ничего не объяснял пользователю.
func TestHumanParseError(t *testing.T) {
	if got := humanParseError(errWrap("json: cannot unmarshal string into Go struct field x")); got == "" ||
		got == "bad json" {
		t.Errorf("нечеловеческий текст: %q", got)
	}
	if got := humanParseError(errWrap("порт SSH должен быть числом от 1 до 65535")); got !=
		"порт SSH должен быть числом от 1 до 65535" {
		t.Errorf("внятная ошибка должна дойти как есть, получили %q", got)
	}
}

type simpleErr string

func (e simpleErr) Error() string { return string(e) }

func errWrap(text string) error { return simpleErr(text) }

// Нормализация ввода вызывается один раз для всех команд панели.
func TestNormalizePanelInput(t *testing.T) {
	msg := nmMessage{Cmd: "peer_list", Host: " https://vpn.example.com/ ", SSHPort: 0}
	normalizePanelInput(&msg)
	// порт по умолчанию подставляет adminConfig; здесь важно, что адрес очищен
	if msg.Host != "vpn.example.com" || msg.SSHPort.Int() != 0 {
		t.Errorf("host=%q port=%d", msg.Host, msg.SSHPort.Int())
	}

	msg = nmMessage{Cmd: "peer_list", Host: "vpn.example.com:2222", SSHPort: 0}
	normalizePanelInput(&msg)
	if msg.Host != "vpn.example.com" || msg.SSHPort.Int() != 2222 {
		t.Errorf("порт из адреса потерялся: host=%q port=%d", msg.Host, msg.SSHPort.Int())
	}

	msg = nmMessage{Cmd: "peer_list", Host: "vpn.example.com", SSHPort: flexiblePort(2022)}
	normalizePanelInput(&msg)
	if msg.SSHPort.Int() != 2022 {
		t.Errorf("явный порт перебит: %d", msg.SSHPort.Int())
	}
}
