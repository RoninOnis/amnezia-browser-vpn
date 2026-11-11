//go:build windows

package main

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// httpTransport на Windows учитывает системный прокси (WinINET), если переменные
// окружения не заданы: за корпоративным прокси иначе не работают ни проверка
// версий, ни самообновление, ни скачивание серверных скриптов — при том что
// Firefox те же файлы качает нормально.
//
// PAC (AutoConfigURL) без отдельного движка не разбираем: в этом случае остаётся
// прямое соединение, и в ошибке видно подсказку про HTTPS_PROXY.
func httpTransport() *http.Transport {
	if os.Getenv("HTTPS_PROXY")+os.Getenv("HTTP_PROXY")+
		os.Getenv("https_proxy")+os.Getenv("http_proxy") != "" {
		return &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	if proxy := windowsSystemProxy(); proxy != "" {
		if u, err := url.Parse(proxy); err == nil {
			return &http.Transport{Proxy: http.ProxyURL(u)}
		}
	}
	return &http.Transport{Proxy: http.ProxyFromEnvironment}
}

// windowsSystemProxy читает настройки прокси из HKCU (то же, что «Свойства
// обозревателя» → «Подключения» → «Настройка сети»).
func windowsSystemProxy() string {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()

	enabled, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return ""
	}
	server, _, err := key.GetStringValue("ProxyServer")
	if err != nil || strings.TrimSpace(server) == "" {
		return ""
	}
	// Форматы значения: "host:port", "http=host:port;https=host:port", "socks=host:port"
	if !strings.Contains(server, "=") {
		return "http://" + strings.TrimSpace(server)
	}
	for _, part := range strings.Split(server, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		if name == "https" || name == "http" {
			return "http://" + strings.TrimSpace(value)
		}
	}
	return ""
}
