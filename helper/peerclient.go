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

	// EndpointHost — публичный адрес сервера. Скрипт его не знает (он смотрит
	// изнутри), поэтому подставляет клиент, когда собирает vpn://.
	EndpointHost string `json:"-"`
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
	// одинарная кавычка сломала бы формат команды, перевод строки — разбор
	name = strings.Map(func(r rune) rune {
		switch r {
		case '\'', '"', '\n', '\r', '	', ';', '|', '&', '$', '`', '\\':
			return '_'
		}
		return r
	}, name)
	if len([]rune(name)) > 40 {
		name = string([]rune(name)[:40])
	}
	// Ключи идут без кавычек: это base64 без спецсимволов, а sshd подставляет
	// строку в свою оболочку без повторного разбора кавычек — кавычки приехали бы
	// в скрипт как часть аргумента. Имя, наоборот, кавычим: в нём бывают пробелы.
	raw, err := p.runJSON("add", "--pubkey", keys.PublicKey,
		"--psk", keys.Preshared, "--name", shellQuote(name))
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
	raw, err := p.runJSON("remove", "--pubkey", publicKey)
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
	// Домашний каталог ОБЯЗАН совпадать с тем, куда кладём authorized_keys: sshd
	// ищет ключ по passwd, и с домашним /nonexistent вход по ключу не работает
	// (проверено живьём: бутстрап «успешен», а панель потом не может войти).
	// Оболочка — обычный /bin/sh, НЕ nologin: sshd запускает forced command через
	// шелл пользователя, и с nologin вход падает с «account is currently not
	// available» уже ПОСЛЕ успешной аутентификации. Запирает доступ не шелл, а
	// restrict,command= в authorized_keys: этим ключом можно выполнить только
	// awgbv-peer, а без ключа войти нечем (пароля у пользователя нет).
	b.WriteString("  useradd --system --no-create-home --home-dir \"$KEY_HOME\" --shell /bin/sh \"$PEER_USER\" 2>/dev/null \\\n")
	b.WriteString("    || useradd -r -M -d \"$KEY_HOME\" -s /bin/sh \"$PEER_USER\"\n")
	b.WriteString("else\n")
	b.WriteString("  # уже созданный ранее пользователь мог остаться с /nonexistent или nologin\n")
	b.WriteString("  if [ \"$(getent passwd \"$PEER_USER\" | cut -d: -f6)\" != \"$KEY_HOME\" ]; then\n")
	b.WriteString("    usermod -d \"$KEY_HOME\" \"$PEER_USER\"\n")
	b.WriteString("  fi\n")
	b.WriteString("  case \"$(getent passwd \"$PEER_USER\" | cut -d: -f7)\" in\n")
	b.WriteString("    */nologin|/bin/false) usermod -s /bin/sh \"$PEER_USER\" ;;\n")
	b.WriteString("  esac\n")
	b.WriteString("fi\n\n")

	b.WriteString("cat > \"$SUDOERS\" <<'SUDO_EOF'\n")
	b.WriteString("# Управление пирами AmneziaWG из браузерного VPN: ровно одна команда, без пароля.\n")
	// sudo вычищает окружение, и без env_keep переменная SSH_ORIGINAL_COMMAND до
	// скрипта не доходит: forced command получает «нет глагола» и панель не работает.
	b.WriteString("Defaults:awgbv env_keep += \"SSH_ORIGINAL_COMMAND\"\n")
	b.WriteString("awgbv ALL=(root) NOPASSWD: /usr/local/sbin/awgbv-peer\n")
	b.WriteString("SUDO_EOF\n")
	b.WriteString("chmod 0440 \"$SUDOERS\"\n")
	b.WriteString("if command -v visudo >/dev/null 2>&1; then visudo -cf \"$SUDOERS\" >/dev/null; fi\n\n")

	if strings.TrimSpace(authorizedKeyLine) != "" {
		b.WriteString("install -d -m 0700 -o \"$PEER_USER\" -g \"$PEER_USER\" \"$KEY_HOME\" \"$KEY_HOME/.ssh\"\n")
		b.WriteString("AUTH=\"$KEY_HOME/.ssh/authorized_keys\"\n")
		b.WriteString("touch \"$AUTH\"\n")
		// Глагол передаём АРГУМЕНТОМ в forced command: sshd выполняет строку своей
		// оболочкой, поэтому $SSH_ORIGINAL_COMMAND раскрывается до запуска sudo, и
		// sudo уже не может «потерять» её вместе с остальным окружением.
		forced := `restrict,command="sudo /usr/local/sbin/awgbv-peer $SSH_ORIGINAL_COMMAND" ` + authorizedKeyLine
		// Строку с нашим ключом ПЕРЕЗАПИСЫВАЕМ, а не пропускаем при наличии:
		// иначе после исправления формата forced command на сервере остаётся
		// старая строка, и обновление установщика ничего не меняет.
		keyBlob := strings.Fields(authorizedKeyLine)[1]
		b.WriteString("if grep -qF " + shellQuote(keyBlob) + " \"$AUTH\"; then\n")
		b.WriteString("  grep -vF " + shellQuote(keyBlob) + " \"$AUTH\" > \"$AUTH.tmp\" || true\n")
		b.WriteString("  mv \"$AUTH.tmp\" \"$AUTH\"\n")
		b.WriteString("fi\n")
		b.WriteString("printf '%s\\n' " + shellQuote(forced) + " >> \"$AUTH\"\n")
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
