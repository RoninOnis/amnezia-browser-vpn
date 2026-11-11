package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSH-слой панели администратора.
//
// Amnezia управляет сервером ровно так же — SSH exec + sudo docker exec; никакого
// HTTP API у неё нет. Мы повторяем канал, но запираем его:
//   - хост-ключ запоминается при первом подключении (TOFU), при расхождении — отказ;
//   - после бутстрапа все операции идут через forced command не-root пользователя,
//     пароль root используется один раз и не сохраняется.
type SSHConfig struct {
	Addr     string // host:port, по умолчанию :22
	User     string
	Password string
	KeyPath  string // путь к приватному ключу (OpenSSH или PEM)
	Timeout  time.Duration
}

// SSHClient — обёртка над одним подключением. Операции короткие: подключились,
// выполнили одну команду, закрыли.
type SSHClient struct {
	cfg  SSHConfig
	conn *ssh.Client
}

// knownHostsPath — файл с отпечатками серверов, по строке на сервер.
func knownHostsPath() string {
	return filepath.Join(stateDir(), "known_hosts")
}

// hostFingerprint — SHA256 отпечаток ключа сервера в формате, привычном OpenSSH.
func hostFingerprint(key ssh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// verifyHostKey реализует TOFU: при первом подключении отпечаток записывается,
// при последующих — сверяется. Подмену ключа не «подтверждаем галочкой», а
// отказываемся работать: это признак MITM.
func verifyHostKey(host string, remote net.Addr, key ssh.PublicKey) error {
	// ключ привязан к адресу, а не к имени: имя может резолвиться по-разному,
	// а адрес — то, к кому мы реально подключились
	if remote != nil {
		host = remote.String()
	}
	path := knownHostsPath()
	known := map[string]string{}
	if raw, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && !strings.HasPrefix(fields[0], "#") {
				known[fields[0]] = fields[1]
			}
		}
	}
	fp := hostFingerprint(key)
	if prev, ok := known[host]; ok {
		if prev == fp {
			return nil
		}
		return fmt.Errorf("ключ сервера %s изменился: был %s, стал %s — подключение отменено", host, prev, fp)
	}
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s %s\n", host, fp); err != nil {
		return err
	}
	return nil
}

// authMethods собирает способы входа: ключ, затем пароль. Пароль идёт последним,
// чтобы не отправлять его на сервер, который пускает по ключу.
func (c SSHConfig) authMethods() ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if c.KeyPath != "" {
		raw, err := os.ReadFile(c.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("чтение ключа: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(raw)
		if err != nil {
			// ключ может быть зашифрован паролем — тогда нужен ParsePrivateKeyWithPassphrase,
			// но мы такие не поддерживаем и говорим об этом прямо
			if _, ok := err.(*ssh.PassphraseMissingError); ok {
				return nil, errors.New("ключ защищён паролем: снимите пароль или используйте ключ без пароля")
			}
			return nil, fmt.Errorf("разбор ключа: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if c.Password != "" {
		methods = append(methods, ssh.Password(c.Password))
	}
	if len(methods) == 0 {
		return nil, errors.New("нет способа входа: укажите ключ или пароль")
	}
	return methods, nil
}

// DialSSH подключается к серверу и проверяет хост-ключ по TOFU.
func DialSSH(cfg SSHConfig) (*SSHClient, error) {
	if cfg.Addr == "" {
		return nil, errors.New("не указан адрес сервера")
	}
	if !strings.Contains(cfg.Addr, ":") {
		cfg.Addr = net.JoinHostPort(cfg.Addr, "22")
	}
	if cfg.User == "" {
		cfg.User = "root"
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	methods, err := cfg.authMethods()
	if err != nil {
		return nil, err
	}
	conf := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            methods,
		HostKeyCallback: verifyHostKey,
		Timeout:         timeout,
	}
	conn, err := ssh.Dial("tcp", cfg.Addr, conf)
	if err != nil {
		return nil, classifySSHError(cfg.Addr, err)
	}
	return &SSHClient{cfg: cfg, conn: conn}, nil
}

// classifySSHError переводит типовые ошибки в понятные человеку фразы: иначе
// пользователь видит «handshake failed» и не понимает, что делать.
func classifySSHError(addr string, err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unable to authenticate"), strings.Contains(msg, "no supported methods"):
		return fmt.Errorf("сервер %s не принял логин или пароль", addr)
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "connection timed out"):
		return fmt.Errorf("сервер %s не отвечает (порт 22 закрыт или адрес неверный)", addr)
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("сервер %s отказал в подключении: SSH не слушает или порт другой", addr)
	case strings.Contains(msg, "no such host"):
		return fmt.Errorf("адрес %s не найден", addr)
	case strings.Contains(msg, "ключ сервера"):
		return err // отпечаток: сообщение уже понятное
	}
	return fmt.Errorf("SSH к %s: %w", addr, err)
}

func (c *SSHClient) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Run выполняет одну команду и возвращает stdout вместе с stderr: часть скриптов
// Amnezia пишет диагностику в stderr, и терять её нельзя.
func (c *SSHClient) Run(cmd string) (string, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	var out, errb bytes.Buffer
	session.Stdout = &out
	session.Stderr = &errb
	err = session.Run(cmd)
	text := out.String()
	if errb.Len() > 0 {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += errb.String()
	}
	if err != nil {
		return text, fmt.Errorf("команда завершилась ошибкой (%v): %s", err, firstLines(text, 3))
	}
	return text, nil
}

// RunScript отправляет скрипт на stdin `bash -s`. Так бутстрап не зависит от
// кавычек в шелле и не упирается в длину командной строки.
func (c *SSHClient) RunScript(script string) (string, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	var out, errb bytes.Buffer
	session.Stdout = &out
	session.Stderr = &errb
	session.Stdin = strings.NewReader(script)
	err = session.Run("bash -s")
	text := out.String()
	if errb.Len() > 0 {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += errb.String()
	}
	if err != nil {
		return text, fmt.Errorf("скрипт завершился ошибкой (%v): %s", err, firstLines(text, 3))
	}
	return text, nil
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	out := strings.Join(lines, " | ")
	if len(out) > 300 {
		out = out[:300] + "…"
	}
	return out
}

/* ------------------------------------------------------------ ключи клиентов */

// PeerKeys — учётные данные нового клиента.
//
// Приватный ключ создаётся ЗДЕСЬ и серверу не передаётся: наружу уходят только
// публичный ключ и PSK-ключ пира, как это делает и сам клиент Amnezia.
type PeerKeys struct {
	PrivateKey string `json:"privateKey"` // base64, остаётся у пользователя
	PublicKey  string `json:"publicKey"`  // base64, уходит на сервер
	Preshared  string `json:"preshared"`  // base64, общий PSK сервера и пира
}

func b64key(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// newKeyPair создаёт пару X25519 в том же виде, что WireGuard/AmneziaWG:
// приватный ключ — 32 байта с клэмпингом, публичный — результат scalar mult.
func newKeyPair() (priv, pub string, err error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return b64key(key.Bytes()), b64key(key.PublicKey().Bytes()), nil
}

// newPreshared генерирует PSK для пира.
func newPreshared() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return b64key(buf), nil
}

// newPeerKeys создаёт всё, что нужно новому клиенту.
func newPeerKeys() (PeerKeys, error) {
	priv, pub, err := newKeyPair()
	if err != nil {
		return PeerKeys{}, err
	}
	psk, err := newPreshared()
	if err != nil {
		return PeerKeys{}, err
	}
	return PeerKeys{PrivateKey: priv, PublicKey: pub, Preshared: psk}, nil
}

// sshKeyPair создаёт ключ ed25519 для доступа к серверу после бутстрапа.
// Формат — OpenSSH private key, чтобы файл можно было отдать обычному ssh.
func sshKeyPair() (privPEM, authorizedKey string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "awgbv")
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(block)), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))), nil
}

// writeStateFile пишет секрет с правами 600: приватные ключи не должны быть
// доступны другим пользователям машины.
func writeStateFile(name, content string) (string, error) {
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(stateDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	// на всякий случай: файл мог существовать с другими правами
	_ = os.Chmod(path, 0o600)
	return path, nil
}
