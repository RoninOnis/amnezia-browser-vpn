package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// flexiblePort принимает порт так, как его присылает живой интерфейс: числом,
// строкой («22»), пустой строкой (поле очистили) или null. Раньше строка или
// пустое значение ломали разбор всего кадра, и пользователь вместо подсказки
// видел «bad json».
type flexiblePort int

func (p *flexiblePort) UnmarshalJSON(raw []byte) error {
	text := strings.TrimSpace(string(raw))
	text = strings.Trim(text, `"`)
	if text == "" || text == "null" {
		*p = 0
		return nil
	}
	n, err := strconv.Atoi(text)
	if err != nil {
		return fmt.Errorf("порт SSH должен быть числом от 1 до 65535")
	}
	if n < 0 || n > 65535 {
		// иначе пользователь видел «dial tcp: address 70000: invalid port»
		return fmt.Errorf("порт SSH должен быть от 1 до 65535, а не %d", n)
	}
	*p = flexiblePort(n)
	return nil
}

func (p flexiblePort) Int() int { return int(p) }

// normalizeHost приводит адрес к тому виду, который понимает ssh.
//
// Зачем: люди вставляют адрес из браузера и из мессенджера — «https://vpn.ru/»,
// «vpn.ru:2222», с пробелами по краям. Без нормализации такой ввод уходил в
// ssh как есть, и человек получал «не отвечает, порт 22 закрыт или адрес
// неверный» — то есть подсказку не про свою ошибку.
//
// Возвращает host без порта и порт, если он был указан прямо в строке.
func normalizeHost(raw string) (string, int) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", 0
	}
	// схема и путь: https://host/path, ssh://user@host
	if idx := strings.Index(s, "://"); idx >= 0 {
		s = s[idx+3:]
	}
	if idx := strings.IndexAny(s, "/?#"); idx >= 0 {
		s = s[:idx]
	}
	// учётные данные из ссылки ssh://user@host — пользователь берётся из поля
	if idx := strings.LastIndex(s, "@"); idx >= 0 {
		s = s[idx+1:]
	}
	// порт в строке адреса
	if host, portText, err := net.SplitHostPort(s); err == nil {
		if n, convErr := strconv.Atoi(portText); convErr == nil && n > 0 && n <= 65535 {
			return host, n
		}
		return host, 0
	}
	return strings.Trim(s, "[]"), 0
}

// effectivePort — явный порт из поля важнее того, что случайно попал в адрес.
func effectivePort(field flexiblePort, hostPort int) int {
	if n := field.Int(); n > 0 {
		return n
	}
	if hostPort > 0 {
		return hostPort
	}
	return 22
}
