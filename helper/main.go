package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	var (
		daemonMode = flag.Bool("daemon", false, "run as the background helper daemon")
		connectTo  = flag.String("connect", "", "profile to connect: vpn:// link, .conf file path or JSON file path")
		listenAddr = flag.String("listen", "127.0.0.1:0", "SOCKS5 listen address (loopback only)")
		foreground = flag.Bool("foreground", false, "keep the tunnel in the foreground (no daemon, for debugging)")
		doStatus   = flag.Bool("status", false, "print daemon/tunnel status as JSON")
		doStop     = flag.Bool("stop", false, "disconnect and stop the helper daemon")
		dumpProf   = flag.String("dump-profile", "", "parse a vpn:// link / .conf and print the normalised profile JSON")
		showVer    = flag.Bool("version", false, "print version")
		mtuFlag    = flag.Int("mtu", 0, "override profile MTU (0 = use profile value)")
		mtuAuto    = flag.Bool("mtu-auto", false, "walk MTU down (1376->1280->1240->1200) until a handshake appears")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return
	}

	if *dumpProf != "" {
		prof, err := loadProfileArg(*dumpProf)
		if err != nil {
			log.Fatalf("profile: %v", err)
		}
		printJSON(prof)
		return
	}

	switch {
	case *daemonMode:
		logToFile()
		if err := RunDaemon(); err != nil {
			log.Printf("daemon: %v", err)
			os.Exit(1)
		}
		return
	case *doStatus:
		printJSON(statusOrStopped())
		return
	case *doStop:
		if resp, err := call(request{Cmd: "quit"}); err == nil && resp != nil && resp.OK {
			printJSON(&TunnelStatus{Running: false, State: "stopped"})
			return
		}
		printJSON(&TunnelStatus{Running: false, State: "stopped"})
		return
	case *connectTo != "" && *foreground:
		prof, err := loadProfileArg(*connectTo)
		if err != nil {
			log.Fatalf("profile: %v", err)
		}
		tun, err := StartTunnelAuto(prof, *listenAddr, *mtuFlag, *mtuAuto)
		if err != nil {
			log.Fatalf("connect: %v", err)
		}
		go func() {
			tick := time.NewTicker(5 * time.Second)
			defer tick.Stop()
			for range tick.C {
				st := tun.Status()
				log.Printf("status: state=%s handshake=%s rx=%dB tx=%dB",
					st.State, handshakeAge(st), st.RxBytes, st.TxBytes)
			}
		}()
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		tun.Close()
		return
	case *connectTo != "":
		prof, err := loadProfileArg(*connectTo)
		if err != nil {
			log.Fatalf("profile: %v", err)
		}
		if err := ensureDaemon(); err != nil {
			log.Fatalf("daemon: %v", err)
		}
		resp, err := call(request{Cmd: "connect", Profile: prof, Listen: *listenAddr,
			MTU: *mtuFlag, MTUAuto: *mtuAuto})
		if err != nil || resp == nil {
			log.Fatalf("connect: %v", err)
		}
		if !resp.OK {
			log.Fatalf("connect: %s", resp.Error)
		}
		printJSON(resp.Status)
		return
	}

	// Default: native messaging host (invoked by the browser).
	if err := runNativeMessaging(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loadProfileArg(arg string) (*Profile, error) {
	if st, err := os.Stat(arg); err == nil && !st.IsDir() {
		raw, err := os.ReadFile(arg)
		if err != nil {
			return nil, err
		}
		return LoadProfile(string(raw))
	}
	return LoadProfile(arg)
}

func statusOrStopped() *TunnelStatus {
	resp, err := call(request{Cmd: "status"})
	if err != nil || resp == nil {
		return &TunnelStatus{Running: false, State: "stopped"}
	}
	if resp.Status == nil {
		return &TunnelStatus{Running: false, State: "stopped", Err: resp.Error}
	}
	return resp.Status
}

func handshakeAge(st TunnelStatus) string {
	if st.LastHandshake == 0 {
		return "never"
	}
	return time.Since(time.Unix(st.LastHandshake, 0)).Round(time.Second).String() + " ago"
}

func printJSON(v any) {
	enc, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return
	}
	fmt.Println(string(enc))
}

func logToFile() {
	_ = os.MkdirAll(stateDir(), 0o700)
	f, err := os.OpenFile(logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetPrefix("[" + strings.ToLower(version) + "] ")
}
