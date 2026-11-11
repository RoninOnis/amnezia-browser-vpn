package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// Сравнение версий — самая опасная часть проверки обновлений: ошибка здесь либо
// пугает пользователя зря, либо скрывает настоящее обновление.
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.3.6", "0.3.7", -1},
		{"0.3.7", "0.3.6", 1},
		{"0.3.6", "0.3.6", 0},
		{"0.3.6.1", "0.3.6", 1},  // четвёртый компонент важнее
		{"0.3.6", "0.3.6.0", 0},  // хвостовые нули не считаются
		{"0.3.6.1", "0.3.7", -1}, // 0.3.6.1 всё ещё старше 0.3.7
		{"v0.3.7", "0.3.7", 0},   // префикс v допускается
		{"1.0", "0.99.99", 1},
		{"0.10.0", "0.9.9", 1}, // сравниваем числами, а не строками
	}
	for _, c := range cases {
		got, err := compareVersions(c.a, c.b)
		if err != nil {
			t.Fatalf("compareVersions(%q, %q): %v", c.a, c.b, err)
		}
		if got != c.want {
			t.Fatalf("compareVersions(%q, %q) = %d, ожидалось %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCompareVersionsRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "abc", "0.x.1", "0.3."} {
		if _, err := compareVersions(bad, "0.3.7"); err == nil {
			t.Fatalf("версия %q должна считаться некорректной", bad)
		}
	}
}

// Неизвестная версия не должна выглядеть как «требуется обновление»: молчим,
// а не пугаем. Это ровно тот случай, когда хелпер старый и не сообщает версию.
func TestIsOutdated(t *testing.T) {
	if !isOutdated("0.3.6", "0.3.7") {
		t.Fatal("0.3.6 старше 0.3.7 — ожидалось outdated")
	}
	if isOutdated("0.3.7", "0.3.7") {
		t.Fatal("равные версии не outdated")
	}
	if isOutdated("", "0.3.7") {
		t.Fatal("пустая версия не должна считаться устаревшей")
	}
	if isOutdated("0.3.7", "") {
		t.Fatal("без известной последней версии сравнивать нечего")
	}
	if isOutdated("мусор", "0.3.7") {
		t.Fatal("некорректная версия не должна давать outdated")
	}
}

// Разбор настоящего ответа GitHub: берём реальную форму релиза, включая assets.
func TestFetchLatestReleaseParsesGitHubPayload(t *testing.T) {
	body := `{
	  "tag_name": "v0.3.7",
	  "published_at": "2026-11-20T10:00:00Z",
	  "html_url": "https://github.com/RoninOnis/amnezia-browser-vpn/releases/tag/v0.3.7",
	  "prerelease": false,
	  "draft": false,
	  "assets": [
	    {"name": "awgbv-linux-amd64", "browser_download_url": "https://example/awgbv-linux-amd64"},
	    {"name": "amnezia-browser-vpn-0.3.7-signed.xpi", "browser_download_url": "https://example/x.xpi"}
	  ]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("запрос без User-Agent: GitHub такие отклоняет")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	old := releasesAPI
	releasesAPI = srv.URL
	defer func() { releasesAPI = old }()

	info, err := fetchLatestRelease()
	if err != nil {
		t.Fatalf("fetchLatestRelease: %v", err)
	}
	if !info.Checked {
		t.Fatal("Checked должен быть true при успешном ответе")
	}
	if info.Latest != "0.3.7" {
		t.Fatalf("Latest = %q, ожидалось 0.3.7", info.Latest)
	}
	if info.HelperLatest != "0.3.7" {
		t.Fatalf("HelperLatest = %q: версия хелпера живёт в том же релизе", info.HelperLatest)
	}
	if info.PageURL == "" || info.PublishedAt == "" {
		t.Fatal("ссылка на релиз и дата должны заполняться")
	}
}

// Черновик и пре-релиз не считаются «последней версией»: иначе пользователю
// предложат обновиться на недоделанный релиз.
func TestFetchLatestReleaseSkipsPreRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.4.0","prerelease":true}`))
	}))
	defer srv.Close()
	old := releasesAPI
	releasesAPI = srv.URL
	defer func() { releasesAPI = old }()

	if _, err := fetchLatestRelease(); err == nil {
		t.Fatal("пре-релиз должен давать ошибку, а не версию")
	}
}

// Ошибка сети и ответ 500 не должны превращаться в «всё актуально»: расширение
// обязано показать «не удалось проверить».
func TestCheckUpdatesReportsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	old := releasesAPI
	releasesAPI = srv.URL
	defer func() { releasesAPI = old }()

	// изолируем кэш, иначе тест увидит файл с прошлого прогона
	oldHome := os.Getenv("HOME")
	t.Setenv("HOME", t.TempDir())
	defer os.Setenv("HOME", oldHome)

	info := checkUpdates(true)
	if info.Checked {
		t.Fatal("Checked не должен быть true при ошибке")
	}
	if info.Error == "" {
		t.Fatal("ошибка должна быть описана текстом для пользователя")
	}
	if info.Latest != "" {
		t.Fatal("при ошибке нельзя выдавать какую-либо версию")
	}
}
