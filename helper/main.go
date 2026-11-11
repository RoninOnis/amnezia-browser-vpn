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

// waitForSignal блокирует до SIGINT/SIGTERM — используется режимом релея.
func waitForSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
}

// maskToken не даёт токену релея попасть в лог целиком.
func maskToken(t string) string {
	if t == "" {
		return "(без токена)"
	}
	if len(t) <= 6 {
		return "***"
	}
	return t[:4] + "***" + t[len(t)-2:]
}

func main() {
	var (
		daemonMode   = flag.Bool("daemon", false, "run as the background helper daemon")
		connectTo    = flag.String("connect", "", "profile to connect: vpn:// link, .conf file path or JSON file path")
		listenAddr   = flag.String("listen", "127.0.0.1:0", "SOCKS5 listen address (loopback only)")
		foreground   = flag.Bool("foreground", false, "keep the tunnel in the foreground (no daemon, for debugging)")
		doStatus     = flag.Bool("status", false, "print daemon/tunnel status as JSON")
		doStop       = flag.Bool("stop", false, "disconnect and stop the helper daemon")
		dumpProf     = flag.String("dump-profile", "", "parse a vpn:// link / .conf and print the normalised profile JSON")
		showVer      = flag.Bool("version", false, "print version")
		mtuFlag      = flag.Int("mtu", 0, "override profile MTU (0 = use profile value)")
		mtuAuto      = flag.Bool("mtu-auto", false, "walk MTU down until a handshake appears")
		portFallback = flag.Bool("port-fallback", false, "additionally try port 443 (server-side satellite, QUIC-only networks)")
		portFlag     = flag.Int("port", 0, "override server UDP port (0 = use profile value)")
		relayAddr    = flag.String("relay-addr", "", "relay address (\"host:443\") used as a last resort when UDP is filtered")
		relayToken   = flag.String("relay-token", "", "relay auth token")
		relayOnly    = flag.Bool("relay-only", false, "skip direct attempts and use only the relay")
		relayServe   = flag.Bool("relay-serve", false, "run as a UDP relay (server side)")
		relayListen  = flag.String("relay-listen", "0.0.0.0:443", "relay listen address for --relay-serve")
		relayAllow   = flag.String("relay-allow", "", "comma-separated allowed upstream targets for --relay-serve")
		relayTarget  = flag.String("relay-target", "", "fixed local target for --relay-serve (\"127.0.0.1:36853\"), typically the AWG port on the same host")
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

	if *relayServe {
		allowed := map[string]bool{}
		for _, t := range strings.Split(*relayAllow, ",") {
			if t = strings.TrimSpace(t); t != "" {
				allowed[t] = true
			}
		}
		srv := &RelayServer{Addr: *relayListen, Token: *relayToken, Allowed: allowed, FixedTarget: *relayTarget}
		if err := srv.Start(); err != nil {
			log.Fatalf("relay: %v", err)
		}
		if *relayTarget != "" {
			log.Printf("relay up (спутник), token %s, fixed target %s", maskToken(*relayToken), *relayTarget)
		} else {
			log.Printf("relay up, token %s, targets %v", maskToken(*relayToken), *relayAllow)
		}
		waitForSignal()
		srv.Close()
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
		opts := TunnelOptions{MTUOverride: *mtuFlag, PortOverride: *portFlag,
			Auto: *mtuAuto || *relayOnly, PortFallback: *mtuAuto || *portFallback,
			RelayAddr: *relayAddr, RelayToken: *relayToken, RelayOnly: *relayOnly}
		tun, err := StartTunnelAuto(prof, *listenAddr, opts)
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
		fallback := *portFallback || *mtuAuto
		resp, err := call(request{Cmd: "connect", Profile: prof, Listen: *listenAddr,
			MTU: *mtuFlag, MTUAuto: *mtuAuto, Port: *portFlag, PortFallback: &fallback,
			RelayAddr: *relayAddr, RelayToken: *relayToken})
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
