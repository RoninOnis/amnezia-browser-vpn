package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Клиент панели администратора: разговаривает с серверным awgbv-peer.
//
// После бутстрапа подключение идёт пользователем awgbv с ключом, у которого в
// authorized_keys стоит restrict,command="sudo /usr/local/sbin/awgbv-peer".
// То есть единственное, что можно выполнить этим ключом, — глаголы list/add/remove.
type PeerInfo struct {
	PublicKey     string `json:"publicKey"`
	PresharedKey  string `json:"presharedKey"`
	Endpoint      string `json:"endpoint"`
	AllowedIPs    string `json:"allowedIps"`
	LastHandshake int64  `json:"lastHandshake"`
	Rx            int64  `json:"rx"`
	Tx            int64  `json:"tx"`
	Name          string `json:"name"`
}

type PeerList struct {
	Container  string `json:"container"`
	Config     string `json:"config"`
	Tool       string `json:"tool,omitempty"`
	ListenPort int    `json:"listenPort"`
	Subnet     string `json:"subnet"`
	ServerKey  string `json:"serverPublicKey"`
	// Params — параметры обфускации строками "ключ=значение". Скрипт отдаёт их
	// плоским списком: собирать JSON прямо в awk пришлось бы с тройным
	// экранированием кавычек, и это уже ломалось.
	Params []string   `json:"params"`
	Peers  []PeerInfo `json:"peers"`
}

// ParamsMap разбирает параметры в map для сборки клиентского конфига.
func (l *PeerList) ParamsMap() map[string]string {
	out := make(map[string]string, len(l.Params))
	for _, item := range l.Params {
		if i := strings.Index(item, "="); i > 0 {
			out[item[:i]] = item[i+1:]
		}
	}
	return out
}

// PeerAdmin — обёртка над SSH-подключением к серверу.
type PeerAdmin struct {
	ssh *SSHClient
}

func NewPeerAdmin(cfg SSHConfig) (*PeerAdmin, error) {
	cl, err := DialSSH(cfg)
	if err != nil {
		return nil, err
	}
	return &PeerAdmin{ssh: cl}, nil
}

func (p *PeerAdmin) Close() error { return p.ssh.Close() }

// runJSON выполняет глагол и разбирает ответ. Берём только stdout: stderr
// скрипта содержит диагностику, и подмешивать её в JSON нельзя.
func (p *PeerAdmin) runJSON(args ...string) ([]byte, error) {
	cmd := strings.Join(args, " ")
	out, err := p.ssh.RunStdout(cmd)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, errors.New("сервер ничего не ответил (скрипт установлен?)")
	}
	if !strings.HasPrefix(trimmed, "{") {
		return nil, fmt.Errorf("неожиданный ответ сервера: %s", firstLines(trimmed, 2))
	}
	return []byte(trimmed), nil
}

// List возвращает контейнер, подсеть и всех пиров.
func (p *PeerAdmin) List() (*PeerList, error) {
	raw, err := p.runJSON("list")
	if err != nil {
		return nil, err
	}
	var out PeerList
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("разбор списка пиров: %w", err)
	}
	return &out, nil
}

// SelfTest проверяет, что скрипт видит контейнер: если контейнера нет, дальше
// идти бессмысленно и пользователю нужно сказать именно это.
func (p *PeerAdmin) SelfTest() (*PeerList, error) {
	raw, err := p.runJSON("selftest")
	if err != nil {
		return nil, err
	}
	var out PeerList
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("разбор ответа selftest: %w", err)
	}
	return &out, nil
}

// Add добавляет пира и возвращает выданный внутренний адрес.
func (p *PeerAdmin) Add(keys PeerKeys, name string) (*PeerInfo, error) {
	if name == "" {
		name = "клиент"
	}
	raw, err := p.runJSON("add", "--pubkey", shellQuote(keys.PublicKey),
		"--psk", shellQuote(keys.Preshared), "--name", shellQuote(name))
	if err != nil {
		return nil, err
	}
	var res struct {
		OK         bool   `json:"ok"`
		PublicKey  string `json:"publicKey"`
		AllowedIPs string `json:"allowedIps"`
		Name       string `json:"name"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("разбор ответа add: %w", err)
	}
	if !res.OK {
		return nil, errors.New("сервер сообщил о неудаче добавления пира")
	}
	return &PeerInfo{PublicKey: res.PublicKey, AllowedIPs: res.AllowedIPs, Name: res.Name}, nil
}

// Remove отзывает пира по публичному ключу.
func (p *PeerAdmin) Remove(publicKey string) error {
	raw, err := p.runJSON("remove", "--pubkey", shellQuote(publicKey))
	if err != nil {
		return err
	}
	var res struct {
		OK      bool `json:"ok"`
		Removed bool `json:"removed"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("разбор ответа remove: %w", err)
	}
	if !res.OK || !res.Removed {
		return errors.New("сервер сообщил о неудаче отзыва пира")
	}
	return nil
}

// shellQuote оборачивает аргумент в одинарные кавычки так, чтобы его нельзя было
// превратить в другую команду. Ключи у нас base64, но полагаться на это нельзя.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// peerScriptMarker отделяет встроенный скрипт от остального текста бутстрапа.
const peerScriptMarker = "AWGBV_PEER_SCRIPT_EOF"

// BootstrapScript собирает shell-скрипт первичной настройки сервера: положить
// awgbv-peer, создать пользователя awgbv, разрешить ему ровно одну команду через
// sudo и добавить наш ключ с restrict+command.
//
// Скрипт идемпотентен: повторный запуск ничего не портит и не плодит дубли.
func BootstrapScript(peerScript, authorizedKeyLine string) string {
	var b bytes.Buffer
	b.WriteString("set -eu\n")
	b.WriteString("PEER_BIN=/usr/local/sbin/awgbv-peer\n")
	b.WriteString("SUDOERS=/etc/sudoers.d/awgbv-peer\n")
	b.WriteString("PEER_USER=awgbv\n")
	b.WriteString("KEY_HOME=/var/lib/awgbv\n\n")

	b.WriteString("install -d -m 0755 /usr/local/sbin\n")
	b.WriteString("cat > \"$PEER_BIN.new\" <<'" + peerScriptMarker + "'\n")
	b.WriteString(strings.TrimRight(peerScript, "\n"))
	b.WriteString("\n" + peerScriptMarker + "\n")
	b.WriteString("install -m 0700 -o root -g root \"$PEER_BIN.new\" \"$PEER_BIN\"\n")
	b.WriteString("rm -f \"$PEER_BIN.new\"\n\n")

	b.WriteString("if ! id \"$PEER_USER\" >/dev/null 2>&1; then\n")
	b.WriteString("  useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin \"$PEER_USER\" 2>/dev/null \\\n")
	b.WriteString("    || useradd -r -M -d /nonexistent -s /usr/sbin/nologin \"$PEER_USER\"\n")
	b.WriteString("fi\n\n")

	b.WriteString("cat > \"$SUDOERS\" <<'SUDO_EOF'\n")
	b.WriteString("# Управление пирами AmneziaWG из браузерного VPN: ровно одна команда, без пароля.\n")
	b.WriteString("awgbv ALL=(root) NOPASSWD: /usr/local/sbin/awgbv-peer\n")
	b.WriteString("SUDO_EOF\n")
	b.WriteString("chmod 0440 \"$SUDOERS\"\n")
	b.WriteString("if command -v visudo >/dev/null 2>&1; then visudo -cf \"$SUDOERS\" >/dev/null; fi\n\n")

	if strings.TrimSpace(authorizedKeyLine) != "" {
		b.WriteString("install -d -m 0700 -o \"$PEER_USER\" -g \"$PEER_USER\" \"$KEY_HOME\" \"$KEY_HOME/.ssh\"\n")
		b.WriteString("AUTH=\"$KEY_HOME/.ssh/authorized_keys\"\n")
		b.WriteString("touch \"$AUTH\"\n")
		b.WriteString("if ! grep -qF " + shellQuote(strings.Fields(authorizedKeyLine)[1]) + " \"$AUTH\"; then\n")
		b.WriteString("  printf '%s\\n' " + shellQuote("restrict,command=\"sudo "+"/usr/local/sbin/awgbv-peer"+"\" "+authorizedKeyLine) + " >> \"$AUTH\"\n")
		b.WriteString("fi\n")
		b.WriteString("chown \"$PEER_USER:$PEER_USER\" \"$AUTH\"\n")
		b.WriteString("chmod 0600 \"$AUTH\"\n\n")
	}

	b.WriteString("sudo -u \"$PEER_USER\" sudo -n \"$PEER_BIN\" selftest\n")
	return b.String()
}

// BootstrapPeerAccess выполняет первичную настройку по полному SSH (root).
// Вызывается один раз: дальше работа идёт ограниченным ключом.
func BootstrapPeerAccess(cfg SSHConfig, peerScript, authorizedKeyLine string) (string, error) {
	cl, err := DialSSH(cfg)
	if err != nil {
		return "", err
	}
	defer cl.Close()
	return cl.RunScript(BootstrapScript(peerScript, authorizedKeyLine))
}
