package main

import (
	"context"
	"fmt"
	"net"

	"github.com/amnezia-vpn/amneziawg-go/v3/tun/netstack"
	"github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/bufferpool"
)

// tunnelResolver resolves names inside the tunnel (so DNS is answered by the
// VPN side, never by the local resolver), which is what we want for the
// whitelist/blacklist split: proxied requests never leak DNS locally.
type tunnelResolver struct {
	tnet *netstack.Net
}

func (r tunnelResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	addrs, err := r.tnet.LookupContextHost(ctx, name)
	if err != nil {
		return ctx, nil, err
	}
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			return ctx, ip, nil
		}
	}
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil {
			return ctx, ip, nil
		}
	}
	return ctx, nil, fmt.Errorf("no address for %q", name)
}

func newSocksListener(tnet *netstack.Net, listen string) (net.Listener, error) {
	server := socks5.NewServer(
		socks5.WithDial(func(ctx context.Context, network, addr string) (net.Conn, error) {
			return tnet.DialContext(ctx, network, addr)
		}),
		socks5.WithResolver(tunnelResolver{tnet: tnet}),
		socks5.WithBufferPool(bufferpool.NewPool(64*1024)),
	)
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("socks listen %s: %w", listen, err)
	}
	go func() {
		if err := server.Serve(ln); err != nil {
			// Serve returns after Close(); nothing actionable here.
			_ = err
		}
	}()
	return ln, nil
}
