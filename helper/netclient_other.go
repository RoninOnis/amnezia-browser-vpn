//go:build !windows

package main

import "net/http"

// httpTransport на Unix: прокси из переменных окружения, как принято.
func httpTransport() *http.Transport {
	return &http.Transport{Proxy: http.ProxyFromEnvironment}
}
