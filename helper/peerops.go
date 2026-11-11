package main

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Операции панели администратора: реестр серверов, ключ доступа и выдача
// клиентских конфигов.
//
// Что здесь принципиально:
//   - пароль SSH используется только для бутстрапа и НЕ сохраняется;
//   - ключ доступа (ed25519) живёт в state-каталоге с правами 0600;
//   - приватный ключ нового клиента создаётся локально и в ответе серверу не
//     участвует: на сервер уходят только публичный ключ и PSK;
//   - наружу (в расширение) отдаётся готовая ссылка vpn://, как в Amnezia.

type ServerRecord struct {
	Host    string `json:"host"`
	Port    int    `json:"port,omitempty"`
	User    string `json:"user,omitempty"`
	AddedAt int64  `json:"addedAt"`
}

func serversPath() string { return filepath.Join(stateDir(), "servers.json") }
func sshKeyPath() string  { return filepath.Join(stateDir(), "awgbv_access_key") }

func loadServers() []ServerRecord {
	raw, err := os.ReadFile(serversPath())
	if err != nil {
		return nil
	}
	var out []ServerRecord
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func saveServers(list []ServerRecord) error {
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(serversPath(), raw, 0o600)
}

// rememberServer добавляет сервер в реестр (или обновляет запись).
func rememberServer(rec ServerRecord) error {
	list := loadServers()
	for i, item := range list {
		if item.Host == rec.Host {
			rec.AddedAt = item.AddedAt
			list[i] = rec
			return saveServers(list)
		}
	}
	rec.AddedAt = time.Now().Unix()
	return saveServers(append(list, rec))
}

func forgetServer(host string) error {
	list := loadServers()
	out := list[:0]
	for _, item := range list {
		if item.Host != host {
			out = append(out, item)
		}
	}
	return saveServers(out)
}

// ensureAccessKey возвращает путь к ключу доступа, создавая его при первом
// обращении. Ключ один на все серверы: он и есть «наш» ключ панели.
func ensureAccessKey() (string, string, error) {
	if raw, err := os.ReadFile(sshKeyPath()); err == nil && len(raw) > 0 {
		// публичную часть восстанавливаем из приватной: отдельный файл не нужен
		line, err := publicKeyLineFromPEM(raw)
		if err != nil {
			return "", "", err
		}
		return sshKeyPath(), line, nil
	}
	priv, pubLine, err := sshKeyPair()
	if err != nil {
		return "", "", err
	}
	path, err := writeStateFile("awgbv_access_key", priv)
	if err != nil {
		return "", "", err
	}
	return path, pubLine, nil
}

// adminConfig собирает параметры подключения к серверу для панели.
func adminConfig(host string, port int) SSHConfig {
	if port == 0 {
		port = 22
	}
	return SSHConfig{
		Addr:    fmt.Sprintf("%s:%d", host, port),
		User:    "awgbv",
		KeyPath: sshKeyPath(),
	}
}

// PeerBootstrap — первичная настройка сервера: полный SSH (пароль или ключ),
// затем установка серверного скрипта, пользователя и ограниченного ключа.
type BootstrapRequest struct {
	Host     string `json:"host"`
	Port     int    `json:"port,omitempty"`
	User     string `json:"user,omitempty"` // root по умолчанию
	Password string `json:"password,omitempty"`
	KeyPath  string `json:"keyPath,omitempty"`
}

func PeerBootstrap(req BootstrapRequest) (map[string]any, error) {
	if strings.TrimSpace(req.Host) == "" {
		return nil, errors.New("не указан адрес сервера")
	}
	script, err := peerScriptSource()
	if err != nil {
		return nil, err
	}
	_, pubLine, err := ensureAccessKey()
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
	out, err := BootstrapPeerAccess(cfg, script, pubLine)
	if err != nil {
		return nil, err
	}
	if err := rememberServer(ServerRecord{Host: req.Host, Port: port, User: "awgbv"}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "selftest": strings.TrimSpace(out), "host": req.Host, "port": port}, nil
}

// peerScriptSource берёт серверный скрипт из релиза и сверяет sha256, если он
// опубликован. Скачивать его в момент бутстрапа честнее, чем вшивать в бинарь:
// скрипт можно поправить без пересборки хелпера.
func peerScriptSource() (string, error) {
	// путь для разработки и живых проверок до того, как ассет попал в релиз
	if local := os.Getenv("AWGBV_PEER_SCRIPT"); local != "" {
		body, err := os.ReadFile(local)
		if err != nil {
			return "", fmt.Errorf("AWGBV_PEER_SCRIPT: %w", err)
		}
		if !bytes.Contains(body, []byte("awgbv-peer")) {
			return "", errors.New("файл по AWGBV_PEER_SCRIPT не похож на awgbv-peer.sh")
		}
		return string(body), nil
	}
	release, err := fetchRelease()
	if err != nil {
		return "", err
	}
	var url, sumsURL string
	for _, a := range release.Assets {
		switch a.Name {
		case "awgbv-peer.sh":
			url = a.BrowserDownloadURL
		case "SHA256SUMS":
			sumsURL = a.BrowserDownloadURL
		}
	}
	if url == "" {
		return "", errors.New("в релизе нет awgbv-peer.sh — обновите хелпер")
	}
	tmp, err := os.CreateTemp("", "awgbv-peer-*.sh")
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
			if want, ok := parseSums(string(raw))["awgbv-peer.sh"]; ok {
				got, err := fileSHA256(path)
				if err != nil {
					return "", err
				}
				if got != want {
					return "", fmt.Errorf("sha256 awgbv-peer.sh не совпал: ожидался %s, получен %s", want[:12], got[:12])
				}
			}
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !bytes.Contains(body, []byte("awgbv-peer")) {
		return "", errors.New("скачанный awgbv-peer.sh не похож на наш скрипт")
	}
	return string(body), nil
}

// PeerSelftest проверяет, что панель видит контейнер на сервере.
func PeerSelftest(host string, port int) (*PeerList, error) {
	admin, err := NewPeerAdmin(adminConfig(host, port))
	if err != nil {
		return nil, err
	}
	defer admin.Close()
	out, err := admin.SelfTest()
	if err == nil && out != nil {
		out.EndpointHost = host
	}
	return out, err
}

// PeerListPeers — список пиров.
func PeerListPeers(host string, port int) (*PeerList, error) {
	admin, err := NewPeerAdmin(adminConfig(host, port))
	if err != nil {
		return nil, err
	}
	defer admin.Close()
	out, err := admin.List()
	if err == nil && out != nil {
		out.EndpointHost = host
	}
	return out, err
}

// PeerCreate генерирует новую пару ключей, добавляет пира и возвращает готовую
// ссылку vpn:// для клиента. Приватный ключ остаётся у пользователя.
func PeerCreate(host string, port int, name string) (map[string]any, error) {
	admin, err := NewPeerAdmin(adminConfig(host, port))
	if err != nil {
		return nil, err
	}
	defer admin.Close()

	list, err := admin.List()
	if err != nil {
		return nil, err
	}
	list.EndpointHost = host
	keys, err := newPeerKeys()
	if err != nil {
		return nil, err
	}
	info, err := admin.Add(keys, name)
	if err != nil {
		return nil, err
	}
	link, err := buildVPNLink(list, keys, info)
	if err != nil {
		// пир уже добавлен: не скрывать это, но сказать, что ссылку собрать не вышло
		return map[string]any{
			"ok": false, "publicKey": keys.PublicKey, "allowedIps": info.AllowedIPs,
			"error": "пир добавлен, но ссылку собрать не удалось: " + err.Error(),
		}, nil
	}
	return map[string]any{
		"ok":         true,
		"publicKey":  keys.PublicKey,
		"allowedIps": info.AllowedIPs,
		"name":       name,
		"vpnLink":    link,
	}, nil
}

// PeerRevoke отзывает пира.
func PeerRevoke(host string, port int, publicKey string) error {
	admin, err := NewPeerAdmin(adminConfig(host, port))
	if err != nil {
		return err
	}
	defer admin.Close()
	return admin.Remove(publicKey)
}

// vpnLinkDoc — структура ссылки vpn:// в том же виде, что отдаёт Amnezia:
// конфиг клиента лежит в containers[].awg.last_config ВЛОЖЕННОЙ JSON-строкой.
type vpnLinkDoc struct {
	DefaultContainer string `json:"defaultContainer"`
	Description      string `json:"description"`
	DNS1             string `json:"dns1"`
	DNS2             string `json:"dns2"`
	HostName         string `json:"hostName,omitempty"`
	Containers       []struct {
		Container string         `json:"container"`
		Awg       map[string]any `json:"awg"`
	} `json:"containers"`
}

// buildVPNLink собирает ссылку vpn:// для нового клиента.
func buildVPNLink(list *PeerList, keys PeerKeys, info *PeerInfo) (string, error) {
	if list.ServerKey == "" {
		return "", errors.New("сервер не отдал публичный ключ")
	}
	params := list.ParamsMap()
	clientIP := strings.TrimSuffix(info.AllowedIPs, "/32")
	if clientIP == "" {
		return "", errors.New("сервер не выдал внутренний адрес пира")
	}
	last := map[string]any{
		"hostName":              firstNonEmpty(list.EndpointHost, ""),
		"port":                  fmt.Sprintf("%d", list.ListenPort),
		"client_priv_key":       keys.PrivateKey,
		"client_pub_key":        keys.PublicKey,
		"server_pub_key":        list.ServerKey,
		"psk_key":               keys.Preshared,
		"client_ip":             clientIP,
		"mtu":                   "1376",
		"persistent_keep_alive": "25",
	}
	for _, k := range []string{"Jc", "Jmin", "Jmax", "S1", "S2", "S3", "S4", "H1", "H2", "H3", "H4", "I1", "I2", "I3", "I4", "I5"} {
		if v, ok := params[k]; ok && v != "" && v != "0" && v != "0-0" {
			last[k] = v
		}
	}
	lastJSON, err := json.Marshal(last)
	if err != nil {
		return "", err
	}
	doc := vpnLinkDoc{
		DefaultContainer: "amnezia-awg",
		Description:      firstNonEmpty(info.Name, "awgbv"),
		DNS1:             "1.1.1.1",
		DNS2:             "1.0.0.1",
		HostName:         list.EndpointHost,
	}
	doc.Containers = append(doc.Containers, struct {
		Container string         `json:"container"`
		Awg       map[string]any `json:"awg"`
	}{
		Container: "amnezia-awg",
		Awg: map[string]any{
			"last_config":        string(lastJSON),
			"isThirdPartyConfig": false,
			"port":               fmt.Sprintf("%d", list.ListenPort),
			"transport_proto":    "udp",
		},
	})
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	w, err := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err := w.Write(raw); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	// заголовок: длина исходного JSON, затем сжатые данные
	out := make([]byte, 4, 4+len(buf.Bytes()))
	binary.BigEndian.PutUint32(out, uint32(len(raw)))
	out = append(out, buf.Bytes()...)
	return "vpn://" + base64.RawURLEncoding.EncodeToString(out), nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
