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
)

// Firefox native messaging framing: uint32 length (native byte order, little
// endian on all platforms we target) followed by UTF-8 JSON.
const maxNMMessage = 32 << 20

type nmMessage struct {
	ID      int64    `json:"id,omitempty"`
	Cmd     string   `json:"cmd"`
	Profile *Profile `json:"profile,omitempty"`
	Listen  string   `json:"listen,omitempty"`
}

type nmReply struct {
	ID      int64         `json:"id,omitempty"`
	OK      bool          `json:"ok"`
	Error   string        `json:"error,omitempty"`
	Status  *TunnelStatus `json:"status,omitempty"`
	Version string        `json:"version,omitempty"`
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
			reply = nmReply{OK: false, Error: "bad json"}
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
			reply.OK, reply.Error = false, "helper daemon unavailable"
			return reply
		}
		reply.OK, reply.Error, reply.Status = resp.OK, resp.Error, resp.Status
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
