package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Манифест native messaging для Windows.
//
// Почему это делает бинарь, а не установщик: batch-скрипт пишет файл в кодировке
// консоли (для русского Windows это OEM 866), а %LOCALAPPDATA% содержит имя
// пользователя. У «C:\Users\Иван\…» путь в JSON оказывался в cp866, Firefox
// читает манифест как UTF-8 и отвечает «No such native application awgbv» —
// расширение показывает мастер установки, будто хелпера нет. Здесь JSON пишется
// encoding/json, то есть всегда UTF-8, а путь — из самого процесса.

const nativeHostName = "awgbv"

// manifestPath — в state-каталоге: на Windows это %LOCALAPPDATA%\AwgBrowserVpn,
// ровно там, где манифест ищет установщик.
func manifestPath() string {
	return filepath.Join(stateDir(), "awgbv.json")
}

type nativeManifest struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Path          string   `json:"path"`
	Type          string   `json:"type"`
	AllowedExtIDs []string `json:"allowed_extensions"`
}

func buildManifest(exe string) nativeManifest {
	return nativeManifest{
		Name:          nativeHostName,
		Description:   "Amnezia Browser VPN helper",
		Path:          exe,
		Type:          "stdio",
		AllowedExtIDs: []string{"awgbv@0xoni.ru"},
	}
}

// installManifest пишет манифест и (на Windows) регистрирует его в HKCU.
// Возвращает путь к манифесту и текст для пользователя.
func installManifest() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	path := manifestPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	raw, err := json.MarshalIndent(buildManifest(exe), "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return "", err
	}
	if !canRegisterManifest() {
		// На Linux манифест кладут установщики в каталоги Firefox; здесь важно
		// только что JSON валиден и лежит по известному пути.
		return path, nil
	}
	if err := registerManifest(path); err != nil {
		return path, fmt.Errorf("манифест записан, но регистрация не удалась: %w", err)
	}
	return path, nil
}
