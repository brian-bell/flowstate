package server

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

const defaultListenAddress = "127.0.0.1:0"

type ListenerScope string

const (
	ListenerScopeLoopback  ListenerScope = "loopback"
	ListenerScopeTailscale ListenerScope = "tailscale"
)

type ResolvedListen struct {
	Listen string
	Host   string
	Port   string
	Scope  ListenerScope
}

type ListenInterface struct {
	Name  string
	Flags net.Flags
	Addrs []netip.Prefix
}

type ListenResolveOptions struct {
	Interfaces func() ([]ListenInterface, error)
}

func ResolveListenAddress(listen string, opts ListenResolveOptions) (ResolvedListen, error) {
	target, err := parseListenTarget(listen)
	if err != nil {
		return ResolvedListen{}, err
	}
	if target.host != "tailscale" {
		return ResolvedListen{
			Listen: target.listen,
			Host:   target.host,
			Port:   target.port,
			Scope:  ListenerScopeLoopback,
		}, nil
	}

	interfaces := opts.Interfaces
	if interfaces == nil {
		interfaces = systemListenInterfaces
	}
	tailscaleHost, err := resolveTailscaleHost(interfaces)
	if err != nil {
		return ResolvedListen{}, fmt.Errorf("could not resolve tailscale listen target %q: %w", target.listen, err)
	}
	return ResolvedListen{
		Listen: net.JoinHostPort(tailscaleHost, target.port),
		Host:   tailscaleHost,
		Port:   target.port,
		Scope:  ListenerScopeTailscale,
	}, nil
}

type listenTarget struct {
	listen string
	host   string
	port   string
}

func parseListenTarget(listen string) (listenTarget, error) {
	if listen == "" {
		listen = defaultListenAddress
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil || host == "" || port == "" {
		return listenTarget{}, invalidListenAddress(listen)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return listenTarget{}, invalidListenAddress(listen)
	}
	if host == "localhost" || host == "tailscale" {
		return listenTarget{listen: listen, host: host, port: port}, nil
	}
	if strings.Contains(host, "%") {
		return listenTarget{}, invalidListenAddress(listen)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !addr.IsLoopback() || addr.Is4In6() {
		return listenTarget{}, invalidListenAddress(listen)
	}
	return listenTarget{listen: listen, host: host, port: port}, nil
}

func resolveTailscaleHost(interfaces func() ([]ListenInterface, error)) (string, error) {
	ifaces, err := interfaces()
	if err != nil {
		return "", err
	}
	var ipv6 string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		for _, prefix := range iface.Addrs {
			addr := prefix.Addr()
			if !isUsableTailscaleAddr(addr) {
				continue
			}
			if addr.Is4() {
				return addr.String(), nil
			}
			if ipv6 == "" {
				ipv6 = addr.String()
			}
		}
	}
	if ipv6 != "" {
		return ipv6, nil
	}
	return "", fmt.Errorf("no Tailscale address found on an up network interface")
}

func isUsableTailscaleAddr(addr netip.Addr) bool {
	if !addr.IsValid() || addr.IsUnspecified() || addr.Is4In6() {
		return false
	}
	if addr.Is4() {
		return netip.MustParsePrefix("100.64.0.0/10").Contains(addr)
	}
	return netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(addr)
}

func systemListenInterfaces() ([]ListenInterface, error) {
	netInterfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	listenInterfaces := make([]ListenInterface, 0, len(netInterfaces))
	for _, iface := range netInterfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		prefixes := make([]netip.Prefix, 0, len(addrs))
		for _, addr := range addrs {
			prefix, ok := parseInterfacePrefix(addr)
			if ok {
				prefixes = append(prefixes, prefix)
			}
		}
		listenInterfaces = append(listenInterfaces, ListenInterface{
			Name:  iface.Name,
			Flags: iface.Flags,
			Addrs: prefixes,
		})
	}
	return listenInterfaces, nil
}

func parseInterfacePrefix(addr net.Addr) (netip.Prefix, bool) {
	if prefix, err := netip.ParsePrefix(addr.String()); err == nil {
		return prefix, true
	}
	if parsed, err := netip.ParseAddr(addr.String()); err == nil {
		return netip.PrefixFrom(parsed, parsed.BitLen()), true
	}
	return netip.Prefix{}, false
}
