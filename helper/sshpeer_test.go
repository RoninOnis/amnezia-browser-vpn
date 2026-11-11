package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Поддельный SSH-сервер: проверяем наш клиент и сборку команд, не поднимая
// настоящий sshd. Forced command моделируется обработчиком: он получает строку
// команды и решает, что ответить, — ровно как awgbv-peer на сервере.
type fakeSSH struct {
	ln       net.Listener
	hostKey  ssh.Signer
	handlers func(cmd string) (stdout, stderr string, ok bool)
	seen     []string
	scripts  []string
}

func newFakeSSH(t *testing.T, handler func(cmd string) (string, string, bool)) *fakeSSH {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSSH{ln: ln, hostKey: signer, handlers: handler}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeSSH) addr() string { return f.ln.Addr().String() }

func (f *fakeSSH) serve() {
	conf := &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
		PasswordCallback: func(conn ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) == "секрет" {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("пароль неверный")
		},
	}
	conf.AddHostKey(f.hostKey)
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			sc, chans, reqs, err := ssh.NewServerConn(c, conf)
			if err != nil {
				return
			}
			defer sc.Close()
			go ssh.DiscardRequests(reqs)
			for newCh := range chans {
				if newCh.ChannelType() != "session" {
					_ = newCh.Reject(ssh.UnknownChannelType, "only session")
					continue
				}
				ch, chReqs, err := newCh.Accept()
				if err != nil {
					continue
				}
				go func() {
					defer ch.Close()
					for req := range chReqs {
						switch req.Type {
						case "exec":
							var payload struct{ Command string }
							_ = ssh.Unmarshal(req.Payload, &payload)
							f.seen = append(f.seen, payload.Command)
							// `bash -s` получает скрипт в stdin — как настоящий бутстрап.
							// Reply обязателен ДО чтения stdin: клиент пишет скрипт только
							// после подтверждения запроса, иначе получается взаимная блокировка.
							if strings.Contains(payload.Command, "bash -s") {
								_ = req.Reply(true, nil)
								buf := make([]byte, 1<<20)
								n, _ := ch.Read(buf)
								f.scripts = append(f.scripts, string(buf[:n]))
								out, errOut, ok := f.handlers(payload.Command)
								_, _ = ch.Write([]byte(out))
								if errOut != "" {
									_, _ = ch.Stderr().Write([]byte(errOut))
								}
								status := uint32(0)
								if !ok {
									status = 1
								}
								_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
								return
							}
							out, errOut, ok := f.handlers(payload.Command)
							_, _ = ch.Write([]byte(out))
							if errOut != "" {
								_, _ = ch.Stderr().Write([]byte(errOut))
							}
							if ok {
								_ = req.Reply(true, nil)
								_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
							} else {
								_ = req.Reply(true, nil)
								_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{1}))
							}
							return
						case "shell":
							// бутстрап приходит как `bash -s` + скрипт в stdin
							_ = req.Reply(true, nil)
							buf := make([]byte, 1<<20)
							n, _ := ch.Read(buf)
							f.scripts = append(f.scripts, string(buf[:n]))
							out, _, _ := f.handlers("")
							_, _ = ch.Write([]byte(out))
							_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
							return
						default:
							_ = req.Reply(false, nil)
						}
					}
				}()
			}
		}(conn)
	}
}

// isolatedHome изолирует state-каталог (known_hosts) от настоящего.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local/state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local/share"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData"))
	return home
}

func TestSSHListParsesServerJSON(t *testing.T) {
	isolatedHome(t)
	body, _ := json.Marshal(PeerList{
		Container: "amnezia-awg2", Config: "awg0.conf", ListenPort: 36853, Subnet: "10.8.1.0/24",
		Peers: []PeerInfo{
			{PublicKey: "PUB1=", AllowedIPs: "10.8.1.2/32", Name: "ноутбук", LastHandshake: 1700000000},
			{PublicKey: "PUB2=", AllowedIPs: "10.8.1.3/32"},
		},
	})
	srv := newFakeSSH(t, func(cmd string) (string, string, bool) {
		if cmd == "list" {
			return string(body), "", true
		}
		return "", "неизвестная команда", false
	})
	admin, err := NewPeerAdmin(SSHConfig{Addr: srv.addr(), User: "awgbv", Password: "секрет"})
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	defer admin.Close()
	list, err := admin.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list.Container != "amnezia-awg2" || list.ListenPort != 36853 {
		t.Fatalf("не разобраны поля контейнера: %+v", list)
	}
	if len(list.Peers) != 2 || list.Peers[0].Name != "ноутбук" {
		t.Fatalf("не разобраны пиры: %+v", list.Peers)
	}
}

// Диагностика в stderr не должна ломать разбор JSON — иначе панель падала бы на
// любом предупреждении скрипта.
func TestSSHListIgnoresStderr(t *testing.T) {
	isolatedHome(t)
	srv := newFakeSSH(t, func(cmd string) (string, string, bool) {
		return `{"container":"amnezia-awg","peers":[]}`, "предупреждение: что-то странное", true
	})
	admin, err := NewPeerAdmin(SSHConfig{Addr: srv.addr(), User: "awgbv", Password: "секрет"})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.List(); err != nil {
		t.Fatalf("шум в stderr сломал разбор: %v", err)
	}
}

func TestSSHAddQuotesArguments(t *testing.T) {
	isolatedHome(t)
	srv := newFakeSSH(t, func(cmd string) (string, string, bool) {
		if strings.HasPrefix(cmd, "add ") {
			return `{"ok":true,"publicKey":"PUB=","allowedIps":"10.8.1.5/32","name":"тест"}`, "", true
		}
		return "", "нет", false
	})
	admin, err := NewPeerAdmin(SSHConfig{Addr: srv.addr(), User: "awgbv", Password: "секрет"})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	info, err := admin.Add(PeerKeys{PublicKey: "PUB=", Preshared: "PSK="}, "имя с пробелом")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if info.AllowedIPs != "10.8.1.5/32" {
		t.Fatalf("не разобран ответ: %+v", info)
	}
	// имя с пробелом должно уйти одним аргументом в кавычках
	last := srv.seen[len(srv.seen)-1]
	if !strings.Contains(last, `--name 'имя с пробелом'`) {
		t.Fatalf("аргумент с пробелом не закавычен: %s", last)
	}
	// и в команде не должно быть ничего, кроме глагола и флагов
	if strings.Contains(last, ";") || strings.Contains(last, "&&") {
		t.Fatalf("в команде появились лишние операторы: %s", last)
	}
}

func TestSSHRemoveReportsServerFailure(t *testing.T) {
	isolatedHome(t)
	srv := newFakeSSH(t, func(cmd string) (string, string, bool) {
		return "", "такого пира нет в конфиге", false
	})
	admin, err := NewPeerAdmin(SSHConfig{Addr: srv.addr(), User: "awgbv", Password: "секрет"})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	err = admin.Remove("PUB=")
	if err == nil {
		t.Fatal("ошибка сервера должна возвращаться наверх")
	}
	if !strings.Contains(err.Error(), "нет в конфиге") {
		t.Fatalf("причина потеряна: %v", err)
	}
}

// TOFU: первый контакт запоминает отпечаток, второй сервер с другим ключом
// обязан быть отвергнут. Это защита от подмены, а не «галочка для галочки».
func TestTOFURejectsChangedHostKey(t *testing.T) {
	home := isolatedHome(t)
	first := newFakeSSH(t, func(cmd string) (string, string, bool) {
		return `{"container":"amnezia-awg","peers":[]}`, "", true
	})
	admin, err := NewPeerAdmin(SSHConfig{Addr: first.addr(), User: "awgbv", Password: "секрет"})
	if err != nil {
		t.Fatalf("первое подключение: %v", err)
	}
	admin.Close()

	known := filepath.Join(home, ".local/state/awgbv/known_hosts")
	raw, err := os.ReadFile(known)
	if err != nil {
		t.Fatalf("отпечаток не сохранён: %v", err)
	}
	if !strings.Contains(string(raw), "SHA256:") {
		t.Fatalf("в known_hosts нет отпечатка: %s", raw)
	}

	// тот же адрес, но другой хост-ключ
	second := newFakeSSH(t, func(cmd string) (string, string, bool) { return "{}", "", true })
	// подменяем строку для адреса первого сервера на ключ второго
	raw2, _ := os.ReadFile(known)
	lines := strings.Split(strings.TrimSpace(string(raw2)), "\n")
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			lines[i] = fields[0] + " SHA256:ЭТОДРУГОЙКЛЮЧ"
		}
	}
	_ = os.WriteFile(known, []byte(strings.Join(lines, "\n")+"\n"), 0o600)

	if _, err := DialSSH(SSHConfig{Addr: first.addr(), User: "awgbv", Password: "секрет"}); err == nil {
		t.Fatal("подмена хост-ключа должна отвергаться")
	} else if !strings.Contains(err.Error(), "изменился") {
		t.Fatalf("сообщение должно объяснять причину: %v", err)
	}
	_ = second
}

func TestSSHWrongPasswordExplained(t *testing.T) {
	isolatedHome(t)
	srv := newFakeSSH(t, func(cmd string) (string, string, bool) { return "{}", "", true })
	_, err := DialSSH(SSHConfig{Addr: srv.addr(), User: "awgbv", Password: "неверный"})
	if err == nil {
		t.Fatal("с неверным паролем подключения быть не должно")
	}
	if !strings.Contains(err.Error(), "не принял логин или пароль") {
		t.Fatalf("ошибка должна быть понятной человеку: %v", err)
	}
}

// Бутстрап: скрипт обязан содержать всё нужное и быть идемпотентным по тексту.
func TestBootstrapScriptContent(t *testing.T) {
	isolatedHome(t)
	srv := newFakeSSH(t, func(cmd string) (string, string, bool) {
		return `{"ok":true,"container":"amnezia-awg2"}`, "", true
	})
	peerScript := "#!/usr/bin/env bash\necho hi\n"
	pub := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITESTKEY awgbv"
	out, err := BootstrapPeerAccess(SSHConfig{Addr: srv.addr(), User: "root", Password: "секрет"}, peerScript, pub)
	if err != nil {
		t.Fatalf("бутстрап: %v", err)
	}
	if !strings.Contains(out, "amnezia-awg2") {
		t.Fatalf("нет ответа selftest: %s", out)
	}
	if len(srv.scripts) == 0 {
		t.Fatal("скрипт не был отправлен")
	}
	body := srv.scripts[len(srv.scripts)-1]
	for _, want := range []string{
		"/usr/local/sbin/awgbv-peer",
		"useradd",
		"sudoers.d/awgbv-peer",
		"NOPASSWD: /usr/local/sbin/awgbv-peer",
		"restrict,command=",
		"AAAAC3NzaC1lZDI1NTE5AAAAITESTKEY",
		"selftest",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("в бутстрап-скрипте нет %q:\n%s", want, body)
		}
	}
	// никаких ALL=(ALL) — только одна конкретная команда
	if strings.Contains(body, "ALL=(ALL)") {
		t.Fatal("слишком широкие права sudo в бутстрапе")
	}
}

// Повторный бутстрап не должен ломать конфигурацию: скрипт идемпотентен,
// ключ добавляется только если его ещё нет.
func TestBootstrapScriptIsIdempotent(t *testing.T) {
	body := BootstrapScript("#!/bin/sh\ntrue\n", "ssh-ed25519 AAAAKEY x")
	if strings.Count(body, "grep -qF") != 1 {
		t.Fatalf("проверка наличия ключа должна быть одна: %s", body)
	}
	if !strings.Contains(body, "if ! id \"$PEER_USER\"") {
		t.Fatal("создание пользователя должно быть под проверкой id")
	}
}
