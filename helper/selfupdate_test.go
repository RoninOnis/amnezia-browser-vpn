package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Имена файлов должны совпадать с install/build.sh, иначе обновление молча
// не найдёт ассет и пользователь решит, что кнопка не работает.
func TestAssetNameFor(t *testing.T) {
	cases := map[[2]string]string{
		{"linux", "amd64"}:   "awgbv-linux-amd64",
		{"linux", "arm64"}:   "awgbv-linux-arm64",
		{"windows", "amd64"}: "awgbv-windows-amd64.exe",
		{"darwin", "arm64"}:  "awgbv-darwin-arm64",
	}
	for in, want := range cases {
		if got := assetNameFor(in[0], in[1]); got != want {
			t.Fatalf("assetNameFor(%q,%q) = %q, ожидалось %q", in[0], in[1], got, want)
		}
	}
}

// SHA256SUMS от sha256sum: два пробела, табуляция, звёздочка в бинарном режиме,
// CRLF после wget на Windows. Всё это должно читаться.
func TestParseSums(t *testing.T) {
	h := strings.Repeat("a", 64)
	body := strings.Join([]string{
		h + "  awgbv-linux-amd64",
		strings.Repeat("b", 64) + "\tawgbv-windows-amd64.exe",
		strings.Repeat("c", 64) + " *install-satellite.sh\r",
		"# комментарий",
		"мусор без хеша",
		strings.Repeat("d", 63) + "  короткий.хеш",
		"",
	}, "\n")
	sums := parseSums(body)
	if sums["awgbv-linux-amd64"] != h {
		t.Fatalf("linux-ассет не разобран: %v", sums)
	}
	if sums["awgbv-windows-amd64.exe"] != strings.Repeat("b", 64) {
		t.Fatal("windows-ассет с табуляцией не разобран")
	}
	if sums["install-satellite.sh"] != strings.Repeat("c", 64) {
		t.Fatal("звёздочка бинарного режима не снята")
	}
	if _, ok := sums["короткий.хеш"]; ok {
		t.Fatal("хеш неверной длины принят")
	}
	if len(sums) != 3 {
		t.Fatalf("лишние записи в сумме: %v", sums)
	}
}

// Стенд: поддельный релиз с настоящими URL на httptest, фейковый бинарь
// размером чуть больше мегабайта (проверка размера должна пропустить).
type stand struct {
	srv      *httptest.Server
	exe      string
	bin      []byte
	sums     string
	spawned  int
	quit     int
	newVer   string
	dropBin  bool // в релизе нет файла для этой платформы
	dtopSums bool // в релизе нет SHA256SUMS
}

func newStand(t *testing.T, mutate func(*stand)) *stand {
	t.Helper()
	s := &stand{newVer: "0.4.0"}
	s.bin = append([]byte("#!/bin/sh\necho "), []byte(strings.Repeat("x", 1<<20))...)
	sum := sha256.Sum256(s.bin)
	s.sums = fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), assetNameFor(runtime.GOOS, runtime.GOARCH))

	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		if mutate != nil {
			mutate(s)
		}
		base := "http://" + r.Host
		payload := releasePayload{TagName: "v0.4.0", PublishedAt: "2026-11-20T00:00:00Z", HTMLURL: base + "/rel"}
		if !s.dropBin {
			payload.Assets = append(payload.Assets,
				releaseAsset{Name: assetNameFor(runtime.GOOS, runtime.GOARCH), BrowserDownloadURL: base + "/bin"})
		}
		if !s.dtopSums {
			payload.Assets = append(payload.Assets, releaseAsset{Name: "SHA256SUMS", BrowserDownloadURL: base + "/sums"})
		}
		_ = json.NewEncoder(w).Encode(payload)
	})
	mux.HandleFunc("/bin", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(s.bin) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) {
		if s.sums == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(s.sums))
	})
	s.srv = httptest.NewServer(mux)

	dir := t.TempDir()
	s.exe = filepath.Join(dir, "awgbv")
	if err := os.WriteFile(s.exe, []byte("старый бинарь"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldAPI, oldExe, oldSpawn, oldCall, oldVerify := releasesAPI, selfUpdateExe, selfUpdateSpawn, selfUpdateCall, verifyByRunning
	oldTimeout := confirmTimeout
	t.Cleanup(func() {
		s.srv.Close()
		releasesAPI, selfUpdateExe, selfUpdateSpawn, selfUpdateCall, verifyByRunning = oldAPI, oldExe, oldSpawn, oldCall, oldVerify
		confirmTimeout = oldTimeout
	})
	releasesAPI = s.srv.URL + "/latest"
	selfUpdateExe = func() (string, error) { return s.exe, nil }
	verifyByRunning = false
	selfUpdateSpawn = func(string) error { s.spawned++; return nil }
	selfUpdateReadControl = func() (*controlFile, error) {
		return &controlFile{Port: 1, Token: "t", Version: s.newVer}, nil
	}
	confirmTimeout = 300 * time.Millisecond // в тестах не ждём секундами
	selfUpdateCall = func(req request) (*response, error) {
		switch req.Cmd {
		case "quit":
			s.quit++
			return &response{OK: true}, nil
		case "version":
			return &response{OK: true, Version: s.newVer}, nil
		case "ping":
			return nil, nil // «демона нет» — иначе waitDaemonDown ждёт таймаут впустую
		}
		return &response{OK: true}, nil
	}
	// кэш проверки обновлений не должен течь между тестами
	t.Setenv("HOME", t.TempDir())
	return s
}

func TestSelfUpdateHappyPath(t *testing.T) {
	s := newStand(t, nil)
	res := selfUpdate()
	if res.Error != "" {
		t.Fatalf("обновление не прошло: %s", res.Error)
	}
	if res.To != "0.4.0" || !res.Restarted {
		t.Fatalf("неожиданный результат: %+v", res)
	}
	if res.Bytes != int64(len(s.bin)) {
		t.Fatalf("размер скачанного %d, ожидался %d", res.Bytes, len(s.bin))
	}
	got, err := os.ReadFile(s.exe)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(s.bin) {
		t.Fatalf("бинарь не подменён: %d байт вместо %d", len(got), len(s.bin))
	}
	if _, err := os.Stat(s.exe + ".old"); !os.IsNotExist(err) {
		t.Fatal("после успешного обновления бэкап должен удаляться")
	}
	if s.quit == 0 {
		t.Fatal("демон не был остановлен перед подменой")
	}
	if s.spawned != 1 {
		t.Fatalf("демон запускался %d раз, ожидался 1", s.spawned)
	}
}

// Несовпадение хеша — самый важный отказ: подменять бинарь нельзя ни при каких
// обстоятельствах, старый файл должен остаться нетронутым.
func TestSelfUpdateRefusesBadHash(t *testing.T) {
	s := newStand(t, func(s *stand) {
		s.sums = strings.Repeat("0", 64) + "  " + assetNameFor(runtime.GOOS, runtime.GOARCH) + "\n"
	})
	res := selfUpdate()
	if !strings.Contains(res.Error, "sha256 не совпал") {
		t.Fatalf("ожидалась ошибка хеша, получено: %q", res.Error)
	}
	got, _ := os.ReadFile(s.exe)
	if string(got) != "старый бинарь" {
		t.Fatal("файл подменён несмотря на несовпадение хеша")
	}
	if s.spawned != 0 || s.quit != 0 {
		t.Fatal("демон трогали при неудачной проверке")
	}
}

func TestSelfUpdateRefusesWithoutSums(t *testing.T) {
	s := newStand(t, func(s *stand) { s.dtopSums = true })
	res := selfUpdate()
	if !strings.Contains(res.Error, "SHA256SUMS") {
		t.Fatalf("обновление без контрольных сумм должно отказывать: %q", res.Error)
	}
	got, _ := os.ReadFile(s.exe)
	if string(got) != "старый бинарь" {
		t.Fatal("файл подменён без проверки суммы")
	}
}

func TestSelfUpdateRefusesTinyFile(t *testing.T) {
	s := newStand(t, nil)
	s.bin = []byte("обрезок")
	sum := sha256.Sum256(s.bin)
	s.sums = fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), assetNameFor(runtime.GOOS, runtime.GOARCH))
	res := selfUpdate()
	if !strings.Contains(res.Error, "подозрительно маленький") {
		t.Fatalf("обрезок должен отвергаться: %q", res.Error)
	}
	got, _ := os.ReadFile(s.exe)
	if string(got) != "старый бинарь" {
		t.Fatal("файл подменён обрезком")
	}
}

// Демон не подтвердил новую версию — обязан вернуться прежний бинарь.
// Иначе пользователь остаётся с нерабочим хелпером и без VPN.
func TestSelfUpdateRollsBackWhenDaemonFails(t *testing.T) {
	s := newStand(t, nil)
	s.newVer = "0.0.1" // демон рапортует не ту версию
	res := selfUpdate()
	if res.Error == "" {
		t.Fatal("ожидалась ошибка подтверждения версии")
	}
	got, _ := os.ReadFile(s.exe)
	if string(got) != "старый бинарь" {
		t.Fatalf("откат не выполнен, в файле %d байт", len(got))
	}
}

// Нет ассета под текущую платформу — говорим прямо, что именно не нашлось.
func TestSelfUpdateReportsMissingAsset(t *testing.T) {
	newStand(t, func(s *stand) { s.dropBin = true })
	res := selfUpdate()
	if !strings.Contains(res.Error, "нет файла") {
		t.Fatalf("ожидалось сообщение об отсутствующем ассете: %q", res.Error)
	}
}
