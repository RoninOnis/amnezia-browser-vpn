package main

import (
	"net/http"
	"time"
)

// httpClient — единственная точка создания HTTP-клиента для сети.
//
// Зачем отдельная функция: весь сетевой код (проверка версий, самообновление,
// скачивание серверных скриптов) должен одинаково понимать прокси. На Linux
// достаточно переменных окружения, а на Windows прокси настраивают в «Свойствах
// обозревателя» (WinINET), и переменные HTTP_PROXY почти никогда не выставлены —
// без этого за корпоративным прокси обновления и панель «не работают», хотя
// Firefox качает те же файлы спокойно.
func httpClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout, Transport: httpTransport()}
}
