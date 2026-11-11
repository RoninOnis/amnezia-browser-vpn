package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Обновление хелпера в один клик.
//
// Хелпер — единственный бинарь и живёт в пользовательском каталоге, поэтому
// обновление сводится к замене файла. Опасных мест три, и все закрыты:
//  1. скачали не то — сверяем sha256 с SHA256SUMS из того же релиза;
//  2. скачали битое — запускаем новый бинарь с --version до подмены;
//  3. подменили и остались без VPN — старый файл переименовываем, а не удаляем,
//     и возвращаем его обратно, если новый демон не поднялся.
type SelfUpdateResult struct {
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Asset     string `json:"asset,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	Restarted bool   `json:"restarted,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Переменные для тестов: подменяем путь к «своему» бинарю, запуск демона и
// control-сокет, чтобы проверять сценарии без реальной подмены тестового файла.
var (
	verifyByRunning       = true
	selfUpdateExe         = func() (string, error) { return os.Executable() }
	selfUpdateSpawn       = spawnDetachedAt // запуск по явному пути: os.Executable() после подмены смотрит на .old
	selfUpdateCall        = call
	selfUpdateReadControl = readControl
	// таймаут подтверждения версии; в тестах уменьшается, чтобы не ждать секундами
	confirmTimeout = 4 * time.Second
)

// assetNameFor повторяет имена из install/build.sh: awgbv-linux-amd64,
// awgbv-windows-amd64.exe.
func assetNameFor(goos, goarch string) string {
	name := fmt.Sprintf("awgbv-%s-%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// parseSums разбирает SHA256SUMS от sha256sum: "<hash>  <file>", допускаются
// пробел/два пробела/табуляция, звёздочка в бинарном режиме и CRLF.
func parseSums(body string) map[string]string {
	sums := make(map[string]string)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		hash := strings.ToLower(fields[0])
		if len(hash) != 64 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		sums[filepath.Base(name)] = hash
	}
	return sums
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// downloadTo качает url в dst с ограничением размера: 200 МБ с запасом,
// чтобы кривой ответ не забил диск.
func downloadTo(url, dst string) (int64, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return 0, fmt.Errorf("скачивание: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("скачивание: сервер ответил %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, 200<<20))
	closeErr := f.Close()
	if err != nil {
		return n, fmt.Errorf("запись файла: %w", err)
	}
	if closeErr != nil {
		return n, closeErr
	}
	return n, nil
}

// runVersion спрашивает у бинаря его версию. Так мы убеждаемся, что файл
// запускается на этой машине и той же архитектуры, до подмены рабочего.
func runVersion(path string) (string, error) {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// selfUpdate скачивает последний релиз и подменяет текущий бинарь.
func selfUpdate() SelfUpdateResult {
	res := SelfUpdateResult{From: version}

	exe, err := selfUpdateExe()
	if err != nil {
		res.Error = "не удалось определить путь к хелперу: " + err.Error()
		return res
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	asset := assetNameFor(runtime.GOOS, runtime.GOARCH)
	res.Asset = asset

	release, err := fetchRelease()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.To = strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")

	var binURL, sumsURL string
	for _, a := range release.Assets {
		switch a.Name {
		case asset:
			binURL = a.BrowserDownloadURL
		case "SHA256SUMS":
			sumsURL = a.BrowserDownloadURL
		}
	}
	if binURL == "" {
		res.Error = fmt.Sprintf("в релизе %s нет файла %s", res.To, asset)
		return res
	}
	if sumsURL == "" {
		res.Error = fmt.Sprintf("в релизе %s нет SHA256SUMS — обновление без проверки не делаем", res.To)
		return res
	}

	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".awgbv-update-*")
	if err != nil {
		res.Error = "нет прав на запись рядом с хелпером: " + err.Error()
		return res
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	if _, err := downloadTo(binURL, tmpPath); err != nil {
		res.Error = err.Error()
		return res
	}
	info, err := os.Stat(tmpPath)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Bytes = info.Size()
	// права выставляем ДО проверочного запуска: CreateTemp создаёт файл 0600,
	// и попытка выполнить его даёт «permission denied» (проверено живым прогоном)
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		res.Error = err.Error()
		return res
	}
	if res.Bytes < 1<<20 {
		res.Error = fmt.Sprintf("файл подозрительно маленький (%d байт) — обновление отменено", res.Bytes)
		return res
	}

	// sha256
	sumsTmp := tmpPath + ".sums"
	if _, err := downloadTo(sumsURL, sumsTmp); err != nil {
		res.Error = err.Error()
		return res
	}
	sumsBody, err := os.ReadFile(sumsTmp)
	_ = os.Remove(sumsTmp)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	want, ok := parseSums(string(sumsBody))[asset]
	if !ok {
		res.Error = fmt.Sprintf("в SHA256SUMS нет записи для %s", asset)
		return res
	}
	got, err := fileSHA256(tmpPath)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if got != want {
		res.Error = fmt.Sprintf("sha256 не совпал: ожидался %s, получен %s", want[:12], got[:12])
		return res
	}

	// запускается ли он вообще
	if verifyByRunning {
		v, err := runVersion(tmpPath)
		if err != nil {
			res.Error = "скачанный бинарь не запускается: " + err.Error()
			return res
		}
		if !strings.Contains(v, res.To) {
			res.Error = fmt.Sprintf("скачанный бинарь сообщает версию %q вместо %q", v, res.To)
			return res
		}
	}
	// Останавливаем демон и ДОЖИДАЕМСЯ его выхода. Демон держит flock, и новый
	// процесс молча умирает, если старый ещё не отпустил замок — на этом мы уже
	// обожглись: подмена проходила, новый демон не поднимался, шёл откат.
	_, _ = selfUpdateCall(request{Cmd: "quit"})
	if !waitLockFree(8 * time.Second) {
		// Осиротевший демон (например, запущенный со старого пути) держит замок
		// и не отвечает. Менять бинарь в этот момент нельзя — получим два демона.
		res.Error = "демон не остановился и не отвечает: закройте браузер или выполните awgbv --stop, затем повторите"
		return res
	}

	backup := exe + ".old"
	_ = os.Remove(backup)
	if err := os.Rename(exe, backup); err != nil {
		res.Error = "не удалось отодвинуть текущий хелпер (занят?): " + err.Error()
		return res
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		// возвращаем старый на место — без рабочего хелпера пользователь остаётся без VPN
		_ = os.Rename(backup, exe)
		res.Error = "не удалось положить новый хелпер: " + err.Error()
		return res
	}

	// Поднимаем демон новым бинарём. Три попытки: если замок ещё не отпущен,
	// вторая попытка проходит, и пользователь не видит ложного отката.
	// control.json удаляем ДО запуска: иначе мы бы стёрли файл, который новый
	// демон только что создал, и проверка версии никогда бы не прошла.
	removeControl()
	for attempt := 1; attempt <= 4; attempt++ {
		if err := selfUpdateSpawn(exe); err == nil && confirmVersion(res.To, confirmTimeout) {
			res.Restarted = true
			_ = os.Remove(backup)
			return res
		}
		// пауза обязательна: без неё все попытки проходят, пока старый процесс
		// ещё держит flock, и все четыре проваливаются мгновенно
		time.Sleep(500 * time.Millisecond)
	}

	// не поднялся — откат
	_, _ = selfUpdateCall(request{Cmd: "quit"})
	waitLockFree(5 * time.Second)
	_ = os.Rename(exe, tmpPath)
	if err := os.Rename(backup, exe); err == nil {
		_ = selfUpdateSpawn(exe)
	}
	res.Error = "новый демон не подтвердил версию, вернул прежний бинарь"
	return res
}

// waitLockFree ждёт, пока освободится flock демона. Проверяем именно замок, а
// не control-сокет: сокет исчезает раньше, чем процесс отпускает файл, и новый
// демон молча умирает, не сумев взять замок.
func waitLockFree(timeout time.Duration) bool {
	// каталог состояния может ещё не существовать (первый запуск после установки),
	// а tryLock без него просто не сможет создать файл замка
	_ = os.MkdirAll(stateDir(), 0o700)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if release, ok := tryLock(lockPath()); ok {
			release() // сразу отпускаем: замок нужен новому демону
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

// waitDaemonDown ждёт, пока демон перестанет отвечать: только после этого
// можно запускать новый, иначе он не возьмёт flock.
func waitDaemonDown(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := selfUpdateCall(request{Cmd: "ping"})
		if err != nil || resp == nil {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// confirmVersion проверяет версию демона по control.json, который новый процесс
// пишет сам при старте.
//
// Через команду «version» проверять НЕЛЬЗЯ: обновляемся мы всегда на релиз,
// который старше нашего кода, и такой команды в нём может просто не быть.
// Проверено на релизе 0.3.6.2 — подтверждение версии падало, и обновление
// откатывалось при живом новом демоне.
func confirmVersion(want string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cf, err := selfUpdateReadControl(); err == nil && cf != nil &&
			strings.TrimSpace(cf.Version) == want {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
