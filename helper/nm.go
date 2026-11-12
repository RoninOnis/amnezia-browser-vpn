package main

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Firefox native messaging framing: uint32 length (native byte order, little
// endian on all platforms we target) followed by UTF-8 JSON.
const maxNMMessage = 32 << 20

type nmMessage struct {
	ID           int64    `json:"id,omitempty"`
	Cmd          string   `json:"cmd"`
	Profile      *Profile `json:"profile,omitempty"`
	Listen       string   `json:"listen,omitempty"`
	MTU          int      `json:"mtu,omitempty"`
	MTUAuto      bool     `json:"mtuAuto,omitempty"`
	Port         int      `json:"port,omitempty"`
	PortFallback *bool    `json:"portFallback,omitempty"`
	RelayAddr    string   `json:"relayAddr,omitempty"`
	RelayToken   string   `json:"relayToken,omitempty"`
	Force        bool     `json:"force,omitempty"`
}

type nmReply struct {
	ID      int64             `json:"id,omitempty"`
	OK      bool              `json:"ok"`
	Error   string            `json:"error,omitempty"`
	Status  *TunnelStatus     `json:"status,omitempty"`
	Version string            `json:"version,omitempty"`
	Update  *UpdateInfo       `json:"update,omitempty"`
	SelfUp  *SelfUpdateResult `json:"selfUpdate,omitempty"`
}

func runNativeMessaging() error {
	in := bufio.NewReaderSize(os.Stdin, 1<<16)
	out := bufio.NewWriter(os.Stdout)
	for {
		var lenBuf [4]byte
		if _, err := io.ReadFull(in, lenBuf[:]); err != nil {
			return nil // browser closed the port
		}
		n := binary.LittleEndian.Uint32(lenBuf[:])
		if n == 0 || n > maxNMMessage {
			return fmt.Errorf("bad native message length %d", n)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(in, buf); err != nil {
			return nil
		}
		var msg nmMessage
		reply := nmReply{}
		if err := json.Unmarshal(buf, &msg); err != nil {
			// «bad json» ничего не говорит пользователю: показываем, что именно
			// не так, если это можно понять (например, порт введён словами).
			reply = nmReply{OK: false, Error: humanParseError(err)}
		} else {
			reply = handleNM(msg)
		}
		enc, err := json.Marshal(reply)
		if err != nil {
			continue
		}
		var l [4]byte
		binary.LittleEndian.PutUint32(l[:], uint32(len(enc)))
		if _, err := out.Write(l[:]); err != nil {
			return nil
		}
		if _, err := out.Write(enc); err != nil {
			return nil
		}
		if err := out.Flush(); err != nil {
			return nil
		}
	}
}

// waitSettled polls the daemon until the tunnel leaves the "connecting" state
// or the timeout passes. The browser gets a definite answer instead of a
// transient state that flips to an error on the next poll — that was the
// "works only on the second click" complaint.
func waitSettled(timeout time.Duration) *TunnelStatus {
	deadline := time.Now().Add(timeout)
	var last *TunnelStatus
	for time.Now().Before(deadline) {
		resp, err := call(request{Cmd: "status"})
		if err == nil && resp != nil && resp.Status != nil {
			last = resp.Status
			if resp.Status.State != "connecting" {
				return resp.Status
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return last
}

// humanParseError переводит ошибку разбора кадра в понятный текст.
func humanParseError(err error) string {
	text := err.Error()
	if strings.Contains(text, "cannot unmarshal") {
		// типовое: в числовое поле прислали не число
		return "не понял значение в запросе — проверьте, что в полях числа, а не текст"
	}
	return "не понял запрос от расширения, попробуйте ещё раз"
}

func handleNM(msg nmMessage) nmReply {
	reply := nmReply{ID: msg.ID, OK: true}
	switch msg.Cmd {
	case "ping":
		return reply
	case "version":
		reply.Version = version
		return reply
	case "status":
		resp, err := call(request{Cmd: "status"})
		if err != nil {
			reply.OK, reply.Error = false, err.Error()
			return reply
		}
		if resp == nil {
			reply.Status = &TunnelStatus{Running: false, State: "stopped"}
			return reply
		}
		reply.OK, reply.Error, reply.Status = resp.OK, resp.Error, resp.Status
		return reply
	case "self_update":
		res := selfUpdate()
		reply.OK = res.Error == ""
		reply.Error = res.Error
		reply.SelfUp = &res
		if res.To != "" {
			reply.Version = res.To
		}
		return reply
	case "update_check":
		// Делаем проверку прямо здесь, не поднимая демон: это один HTTP-запрос,
		// и проверка версий должна работать даже если демон не запущен.
		info := checkUpdates(msg.Force)
		reply.OK = true
		reply.Update = &info
		return reply
	case "connect":
		if msg.Profile == nil {
			reply.OK, reply.Error = false, "connect: no profile"
			return reply
		}
		if err := ensureDaemon(); err != nil {
			reply.OK, reply.Error = false, err.Error()
			return reply
		}
		resp, err := call(request{Cmd: "connect", Profile: msg.Profile, Listen: msg.Listen})
		if err != nil {
			reply.OK, reply.Error = false, err.Error()
			return reply
		}
		if resp == nil {
			// демон мог умереть между ensureDaemon и вызовом — поднимаем и пробуем ещё раз
			if err := ensureDaemon(); err != nil {
				reply.OK, reply.Error = false, err.Error()
				return reply
			}
			// ВАЖНО: пересылаем все поля запроса, а не только профиль и listen.
			// Раньше mtuAuto/mtu/port/portFallback терялись здесь, и демон всегда
			// шёл без авто-режима: перебор MTU и попытка на 443 не выполнялись
			// никогда, хотя расширение их просило.
			resp, err = call(request{
				Cmd:          "connect",
				Profile:      msg.Profile,
				Listen:       msg.Listen,
				MTU:          msg.MTU,
				MTUAuto:      msg.MTUAuto,
				Port:         msg.Port,
				PortFallback: msg.PortFallback,
				RelayAddr:    msg.RelayAddr,
				RelayToken:   msg.RelayToken,
			})
			if err != nil {
				reply.OK, reply.Error = false, err.Error()
				return reply
			}
			if resp == nil {
				reply.OK, reply.Error = false, "helper daemon unavailable"
				return reply
			}
		}
		reply.OK, reply.Error = resp.OK, resp.Error
		if resp.OK {
			if settled := waitSettled(12 * time.Second); settled != nil {
				reply.Status = settled
			} else {
				reply.Status = resp.Status
			}
		} else {
			reply.Status = resp.Status
		}
		return reply
	case "disconnect":
		resp, err := call(request{Cmd: "disconnect"})
		if err != nil {
			reply.OK, reply.Error = false, err.Error()
			return reply
		}
		if resp == nil {
			reply.Status = &TunnelStatus{Running: false, State: "stopped"}
			return reply
		}
		reply.OK, reply.Error, reply.Status = resp.OK, resp.Error, resp.Status
		return reply
	case "quit":
		_, _ = call(request{Cmd: "quit"})
		reply.Status = &TunnelStatus{Running: false, State: "stopped"}
		return reply
	default:
		reply.OK, reply.Error = false, "unknown cmd "+msg.Cmd
		return reply
	}
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
