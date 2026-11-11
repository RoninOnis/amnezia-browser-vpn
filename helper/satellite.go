package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Установка спутника сервера — того самого скрипта, который перенаправляет
// UDP/443 в контейнер AmneziaWG.
//
// Почему так, а не «одной кнопкой без пароля»: спутник ставится системной
// службой от root, то есть это уже не управление ключами. Наш ограниченный ключ
// панели (restrict,command=awgbv-peer) намеренно НЕ умеет такого: иначе утечка
// ключа из браузера означала бы произвольный root на сервере. Поэтому установка
// спутника — отдельное действие с полным SSH-доступом, и пароль по-прежнему
// используется один раз и не сохраняется.
type SatelliteRequest struct {
	Host     string `json:"host"`
	Port     int    `json:"port,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	KeyPath  string `json:"keyPath,omitempty"`
	// DryRun — только показать план, ничего не менять (по умолчанию именно он).
	DryRun       bool `json:"dryRun"`
	OpenFirewall bool `json:"openFirewall,omitempty"`
}

type SatelliteResult struct {
	DryRun   bool   `json:"dryRun"`
	Finished bool   `json:"finished"`
	Output   string `json:"output"`
}

// satelliteScriptSource берёт установщик из релиза (с проверкой sha256, если
// опубликован). Локальный путь — для живых проверок до сборки релиза.
func satelliteScriptSource() (string, error) {
	if local := os.Getenv("AWGBV_SATELLITE_SCRIPT"); local != "" {
		body, err := os.ReadFile(local)
		if err != nil {
			return "", fmt.Errorf("AWGBV_SATELLITE_SCRIPT: %w", err)
		}
		return string(body), nil
	}
	if cached := os.Getenv("AWGBV_SATELLITE_CACHE"); cached != "" {
		if body, err := os.ReadFile(cached); err == nil && len(body) > 100 {
			return string(body), nil
		}
	}
	release, err := fetchRelease()
	if err != nil {
		return "", err
	}
	var url, sumsURL string
	for _, a := range release.Assets {
		switch a.Name {
		case "install-satellite.sh":
			url = a.BrowserDownloadURL
		case "SHA256SUMS":
			sumsURL = a.BrowserDownloadURL
		}
	}
	if url == "" {
		return "", errors.New("в релизе нет install-satellite.sh — обновите хелпер")
	}
	tmp, err := os.CreateTemp("", "awgbv-satellite-*.sh")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(path)
	if _, err := downloadTo(url, path); err != nil {
		return "", err
	}
	if sumsURL != "" {
		sumsPath := path + ".sums"
		if _, err := downloadTo(sumsURL, sumsPath); err == nil {
			raw, _ := os.ReadFile(sumsPath)
			_ = os.Remove(sumsPath)
			if want, ok := parseSums(string(raw))["install-satellite.sh"]; ok {
				got, err := fileSHA256(path)
				if err != nil {
					return "", err
				}
				if got != want {
					return "", fmt.Errorf("sha256 install-satellite.sh не совпал: ожидался %.12s, получен %.12s", want, got)
				}
			}
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !strings.Contains(string(body), "awgbv-satellite") {
		return "", errors.New("скачанный install-satellite.sh не похож на наш скрипт")
	}
	return string(body), nil
}

// SatelliteRun выполняет установщик на сервере.
//
// Ошибку выполнения не превращаем в «всё плохо»: скрипт печатает понятный план и
// отказы (занятый порт, чужой контейнер), и этот текст нужен пользователю целиком.
func SatelliteRun(req SatelliteRequest) (*SatelliteResult, error) {
	if strings.TrimSpace(req.Host) == "" {
		return nil, errors.New("не указан адрес сервера")
	}
	script, err := satelliteScriptSource()
	if err != nil {
		return nil, err
	}
	user := req.User
	if user == "" {
		user = "root"
	}
	port := req.Port
	if port == 0 {
		port = 22
	}
	cfg := SSHConfig{
		Addr:     fmt.Sprintf("%s:%d", req.Host, port),
		User:     user,
		Password: req.Password,
		KeyPath:  req.KeyPath,
	}
	cl, err := DialSSH(cfg)
	if err != nil {
		return nil, err
	}
	defer cl.Close()

	args := []string{}
	if req.DryRun {
		args = append(args, "--dry-run")
	}
	if req.OpenFirewall && !req.DryRun {
		args = append(args, "--open-firewall")
	}
	out, err := cl.RunScriptArgs(script, args)
	res := &SatelliteResult{DryRun: req.DryRun, Output: strings.TrimSpace(out)}
	if err != nil {
		// текст скрипта важнее кода возврата: там причина отказа
		msg := strings.TrimSpace(out)
		if msg == "" {
			return res, err
		}
		return res, fmt.Errorf("%s", firstLines(msg, 8))
	}
	res.Finished = true
	return res, nil
}
