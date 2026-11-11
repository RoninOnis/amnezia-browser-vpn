//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// registerManifest регистрирует манифест в HKCU: установка не требует админа.
// Ключ HKCU\Software\Mozilla\NativeMessagingHosts\awgbv -> путь к JSON.
func registerManifest(path string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER,
		`Software\Mozilla\NativeMessagingHosts\`+nativeHostName, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("не удалось открыть ветку реестра native messaging: %w", err)
	}
	defer key.Close()
	return key.SetStringValue("", path)
}

// canRegisterManifest — в этом билде регистрация есть.
func canRegisterManifest() bool { return true }
