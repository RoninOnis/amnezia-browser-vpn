package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"
)

const appDirName = "awgbv"

// controlFile is written by the daemon so clients (native-messaging host, CLI)
// can find it: loopback port + shared secret + pid.
type controlFile struct {
	Port    int    `json:"port"`
	Token   string `json:"token"`
	PID     int    `json:"pid"`
	Version string `json:"version"`
	Started int64  `json:"started"`
}

type request struct {
	Token   string   `json:"token,omitempty"`
	Cmd     string   `json:"cmd"`
	Profile *Profile `json:"profile,omitempty"`
	Listen  string   `json:"listen,omitempty"`
	// PortFallback сознательно не выключается ничем, кроме явного portFallback:false
	// в запросе: попытка на 443 стоит 4 секунды и только тогда, когда штатный порт
	// уже не ответил. Именно она спасает в сетях, где наружу пускают только QUIC.
	// MTU/Port > 0 переопределяют значения из профиля; MTUAuto/PortFallback
	// включают подбор MTU вниз и запасной порт 443.
	MTU          int    `json:"mtu,omitempty"`
	MTUAuto      bool   `json:"mtuAuto,omitempty"`
	Port         int    `json:"port,omitempty"`
	PortFallback *bool  `json:"portFallback,omitempty"`
	RelayAddr    string `json:"relayAddr,omitempty"`
	RelayToken   string `json:"relayToken,omitempty"`
	Force        bool   `json:"force,omitempty"` // update_check: игнорировать кэш
}

type response struct {
	OK      bool              `json:"ok"`
	Error   string            `json:"error,omitempty"`
	Status  *TunnelStatus     `json:"status,omitempty"`
	Update  *UpdateInfo       `json:"update,omitempty"`
	SelfUp  *SelfUpdateResult `json:"selfUpdate,omitempty"`
	Version string            `json:"version,omitempty"`
}

func stateDir() string {
	if d := os.Getenv("AWGBV_STATE_DIR"); d != "" {
		return d
	}
	// Платформу определяем по GOOS, а не по наличию переменной: при пустом
	// LOCALAPPDATA (урезанное окружение, служба, отладка) раньше молча
	// срабатывала Linux-ветка, и состояние уезжало в
	// %USERPROFILE%\.local\state мимо каталога установки.
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			if home, err := os.UserHomeDir(); err == nil {
				base = filepath.Join(home, "AppData", "Local")
			}
		}
		if base != "" {
			return filepath.Join(base, "AwgBrowserVpn")
		}
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, appDirName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), appDirName)
	}
	return filepath.Join(home, ".local", "state", appDirName)
}

func controlPath() string { return filepath.Join(stateDir(), "control.json") }
func logPath() string     { return filepath.Join(stateDir(), "helper.log") }
func lockPath() string    { return filepath.Join(stateDir(), "daemon.lock") }

// Daemon serves the control protocol and owns at most one tunnel.
type Daemon struct {
	mu         sync.Mutex
	tunnel     *Tunnel
	connecting bool // идёт перебор попыток: новый connect не должен его обрывать
	lastErr    string
	listen     string
	ln         net.Listener
	token      string
}

func readControl() (*controlFile, error) {
	raw, err := os.ReadFile(controlPath())
	if err != nil {
		return nil, err
	}
	var cf controlFile
	if err := json.Unmarshal(raw, &cf); err != nil {
		return nil, err
	}
	if cf.Port == 0 || cf.Token == "" {
		return nil, fmt.Errorf("control file incomplete")
	}
	return &cf, nil
}

func removeControl() { _ = os.Remove(controlPath()) }

// call sends one request to a running daemon. Returns (nil, nil) when no
// daemon is running.
func call(req request) (*response, error) {
	cf, err := readControl()
	if err != nil {
		return nil, nil
	}
	req.Token = cf.Token
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(cf.Port)), 2*time.Second)
	if err != nil {
		return nil, nil
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	enc, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(enc, '\n')); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ensureDaemon starts the detached daemon if it is not already running.
func ensureDaemon() error {
	if resp, err := call(request{Cmd: "ping"}); err == nil && resp != nil && resp.OK {
		return nil
	}
	removeControl()
	if err := spawnDetached(); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := call(request{Cmd: "ping"}); err == nil && resp != nil && resp.OK {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("helper daemon did not come up (see %s)", logPath())
}

// RunDaemon is the daemon entrypoint.
func RunDaemon() error {
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	release, ok := tryLock(lockPath())
	if !ok {
		// Another daemon is already alive.
		return nil
	}
	defer release()
	defer removeControl()

	token, err := randomToken()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	d := &Daemon{ln: ln, token: token}
	cf := controlFile{
		Port:    ln.Addr().(*net.TCPAddr).Port,
		Token:   token,
		PID:     os.Getpid(),
		Version: version,
		Started: time.Now().Unix(),
	}
	raw, _ := json.MarshalIndent(cf, "", "  ")
	if err := os.WriteFile(controlPath(), raw, 0o600); err != nil {
		return err
	}
	log.Printf("daemon listening on 127.0.0.1:%d (state dir %s)", cf.Port, stateDir())

	for {
		conn, err := ln.Accept()
		if err != nil {
			return nil
		}
		go d.handle(conn)
	}
}

func (d *Daemon) statusLocked() *TunnelStatus {
	if d.tunnel == nil {
		// Пока идёт перебор попыток, туннеля ещё нет, но это НЕ «stopped»:
		// иначе расширение считает, что всё упало, и запускает новое подключение,
		// обрывая текущую попытку. Именно так терялась попытка на 443.
		if d.connecting {
			return &TunnelStatus{Running: false, State: "connecting", Err: d.lastErr}
		}
		return &TunnelStatus{Running: false, State: "stopped", Err: d.lastErr}
	}
	st := d.tunnel.Status()
	return &st
}

func (d *Daemon) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return
	}
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResp(conn, response{OK: false, Error: "bad json"})
		return
	}
	if req.Token != d.token {
		writeResp(conn, response{OK: false, Error: "unauthorised"})
		return
	}

	if req.Cmd == "connect" {
		// Connect длится до нескольких секунд (перебор порт × MTU) — держать
		// мьютекс всё это время нельзя: опрос статуса встанет в очередь, а
		// расширение решит, что хелпер завис.
		d.handleConnect(conn, req)
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	switch req.Cmd {
	case "ping":
		writeResp(conn, response{OK: true})
	case "status":
		writeResp(conn, response{OK: true, Status: d.statusLocked()})
	case "update_check":
		info := checkUpdates(req.Force)
		writeResp(conn, response{OK: true, Update: &info})
	case "version":
		// версия именно этого процесса: после подмены бинаря расширение
		// сверяет её и понимает, что демон уже новый
		writeResp(conn, response{OK: true, Version: version})
	case "connect": // обрабатывается в handleConnect, сюда не попадает
		writeResp(conn, response{OK: false, Error: "internal: connect handled elsewhere"})
	case "disconnect":
		if d.tunnel != nil {
			d.tunnel.Close()
			d.tunnel = nil
		}
		writeResp(conn, response{OK: true, Status: &TunnelStatus{Running: false, State: "stopped"}})
	case "quit":
		if d.tunnel != nil {
			d.tunnel.Close()
			d.tunnel = nil
		}
		writeResp(conn, response{OK: true, Status: &TunnelStatus{Running: false, State: "stopped"}})
		go func() {
			time.Sleep(100 * time.Millisecond)
			os.Exit(0)
		}()
	default:
		writeResp(conn, response{OK: false, Error: "unknown cmd " + req.Cmd})
	}
}

// handleConnect поднимает туннель, не удерживая мьютекс на всё время перебора.
// Пока попытки идут, статус отвечает «connecting», а повторный connect не
// перезапускает перебор — иначе расширение, видя «не подключено», каждые 12 с
// начинало всё заново и до попытки на 443 дело не доходило.
func (d *Daemon) handleConnect(conn net.Conn, req request) {
	if req.Profile == nil {
		writeResp(conn, response{OK: false, Error: "no profile"})
		return
	}

	d.mu.Lock()
	if d.connecting {
		st := d.statusLocked()
		d.mu.Unlock()
		writeResp(conn, response{OK: true, Status: st})
		return
	}
	d.connecting = true
	if d.tunnel != nil {
		d.tunnel.Close()
		d.tunnel = nil
	}
	d.mu.Unlock()

	listen := req.Listen
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	prof := *req.Profile
	prof.normalize()
	opts := TunnelOptions{
		MTUOverride:  req.MTU,
		PortOverride: req.Port,
		Auto:         req.MTUAuto,                                  // перебор MTU — только если просили
		PortFallback: req.PortFallback == nil || *req.PortFallback, // 443 пробуем по умолчанию
		RelayAddr:    req.RelayAddr,
		RelayToken:   req.RelayToken,
	}

	tun, err := StartTunnelAuto(&prof, listen, opts)

	d.mu.Lock()
	d.connecting = false
	if err != nil {
		d.lastErr = err.Error()
		st := d.statusLocked()
		d.mu.Unlock()
		log.Printf("connect failed: %v", err)
		writeResp(conn, response{OK: false, Error: err.Error(), Status: st})
		return
	}
	d.tunnel = tun
	d.lastErr = ""
	st := d.statusLocked()
	d.mu.Unlock()
	writeResp(conn, response{OK: true, Status: st})
}

func writeResp(conn net.Conn, resp response) {
	enc, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(enc, '\n'))
}
