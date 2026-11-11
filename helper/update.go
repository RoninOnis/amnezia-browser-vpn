package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Проверка актуальности версий. Делается в хелпере, а не в расширении, по двум
// причинам: расширению не нужны новые host-permissions (иначе Firefox просит
// подтвердить их при обновлении), и сразу видно расхождение «аддон обновился,
// хелпер нет» — самая частая жалоба.
const (
	updateCacheOK = 6 * time.Hour
	updateTimeout = 8 * time.Second
)

// releasesAPI — переменная, а не константа: тесты подменяют её на httptest-сервер,
// чтобы проверять разбор настоящего ответа GitHub, а не выдуманную структуру.
var releasesAPI = "https://api.github.com/repos/RoninOnis/amnezia-browser-vpn/releases/latest"

// UpdateInfo — то, что уходит в расширение.
type UpdateInfo struct {
	Checked      bool   `json:"checked"`
	Latest       string `json:"latest,omitempty"`
	PublishedAt  string `json:"publishedAt,omitempty"`
	PageURL      string `json:"pageUrl,omitempty"`
	HelperLatest string `json:"helperLatest,omitempty"`
	Error        string `json:"error,omitempty"`
	Cached       bool   `json:"cached,omitempty"`
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type releasePayload struct {
	TagName     string         `json:"tag_name"`
	PublishedAt string         `json:"published_at"`
	HTMLURL     string         `json:"html_url"`
	Assets      []releaseAsset `json:"assets"`
	Prerelease  bool           `json:"prerelease"`
	Draft       bool           `json:"draft"`
}

func updateCachePath() string {
	return filepath.Join(stateDir(), "update.json")
}

// parseVersion режет версию на числа. Любое число компонентов: 0.3.6, 0.3.6.1, 1.2.3.4.5.
func parseVersion(v string) ([]int, error) {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	if v == "" {
		return nil, errors.New("пустая версия")
	}
	// отрезаем суффикс вроде -beta1
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("не число в версии: %q", p)
		}
		out = append(out, n)
	}
	return out, nil
}

// compareVersions возвращает -1, 0 или 1. Недостающие компоненты считаются нулями:
// 0.3.6 == 0.3.6.0, и при этом 0.3.6.1 > 0.3.6.
func compareVersions(a, b string) (int, error) {
	pa, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	pb, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1, nil
			}
			return 1, nil
		}
	}
	return 0, nil
}

// isOutdated сообщает, что текущая версия старше последней. Неизвестную версию
// не считаем устаревшей: лучше промолчать, чем пугать пользователя зря.
func isOutdated(current, latest string) bool {
	if current == "" || latest == "" {
		return false
	}
	cmp, err := compareVersions(current, latest)
	if err != nil {
		return false
	}
	return cmp < 0
}

func readUpdateCache() (*UpdateInfo, bool) {
	raw, err := os.ReadFile(updateCachePath())
	if err != nil {
		return nil, false
	}
	var cached struct {
		At   int64      `json:"at"`
		Info UpdateInfo `json:"info"`
	}
	if err := json.Unmarshal(raw, &cached); err != nil {
		return nil, false
	}
	if time.Since(time.Unix(cached.At, 0)) > updateCacheOK {
		return nil, false
	}
	info := cached.Info
	info.Cached = true
	return &info, true
}

func writeUpdateCache(info UpdateInfo) {
	raw, err := json.Marshal(struct {
		At   int64      `json:"at"`
		Info UpdateInfo `json:"info"`
	}{At: time.Now().Unix(), Info: info})
	if err != nil {
		return
	}
	_ = os.MkdirAll(stateDir(), 0o755)
	_ = os.WriteFile(updateCachePath(), raw, 0o600)
}

// fetchRelease возвращает последний релиз как есть: нужны и теги, и список файлов.
func fetchRelease() (releasePayload, error) {
	client := httpClient(updateTimeout)
	req, err := http.NewRequest(http.MethodGet, releasesAPI, nil)
	if err != nil {
		return releasePayload{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "awgbv-helper")
	resp, err := client.Do(req)
	if err != nil {
		return releasePayload{}, fmt.Errorf("нет связи с GitHub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return releasePayload{}, fmt.Errorf("GitHub ответил %d", resp.StatusCode)
	}
	var payload releasePayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return releasePayload{}, fmt.Errorf("разбор ответа GitHub: %w", err)
	}
	if payload.Draft || payload.Prerelease {
		return releasePayload{}, errors.New("последний релиз помечен как черновик или пре-релиз")
	}
	latest := strings.TrimPrefix(strings.TrimSpace(payload.TagName), "v")
	if _, err := parseVersion(latest); err != nil {
		return releasePayload{}, fmt.Errorf("непонятный тег релиза %q: %w", payload.TagName, err)
	}
	return payload, nil
}

// fetchLatestRelease спрашивает GitHub о последнем релизе и разбирает версию.
func fetchLatestRelease() (UpdateInfo, error) {
	payload, err := fetchRelease()
	if err != nil {
		return UpdateInfo{}, err
	}
	latest := strings.TrimPrefix(strings.TrimSpace(payload.TagName), "v")
	info := UpdateInfo{
		Checked:     true,
		Latest:      latest,
		PublishedAt: payload.PublishedAt,
		PageURL:     payload.HTMLURL,
	}
	// версия хелпера живёт в том же релизе, что и аддон: отдельного канала нет
	info.HelperLatest = latest
	return info, nil
}

// checkUpdates — точка входа для команды update_check. force игнорирует кэш.
func checkUpdates(force bool) UpdateInfo {
	if !force {
		if cached, ok := readUpdateCache(); ok {
			return *cached
		}
	}
	info, err := fetchLatestRelease()
	if err != nil {
		// Честно: не смогли проверить — так и говорим. Молчаливое «всё актуально»
		// хуже ошибки, именно из-за него люди месяцами сидят на старом хелпере.
		return UpdateInfo{Checked: false, Error: err.Error()}
	}
	writeUpdateCache(info)
	return info
}
