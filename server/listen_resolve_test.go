package server

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
)

func TestResolveListenAddressPreservesLoopbackTargets(t *testing.T) {
	tests := []struct {
		name       string
		listen     string
		wantListen string
		wantHost   string
		wantPort   string
	}{
		{name: "empty default", listen: "", wantListen: "127.0.0.1:0", wantHost: "127.0.0.1", wantPort: "0"},
		{name: "localhost", listen: "localhost:8080", wantListen: "localhost:8080", wantHost: "localhost", wantPort: "8080"},
		{name: "ipv4", listen: "127.0.0.1:0", wantListen: "127.0.0.1:0", wantHost: "127.0.0.1", wantPort: "0"},
		{name: "ipv6", listen: "[::1]:8080", wantListen: "[::1]:8080", wantHost: "::1", wantPort: "8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveListenAddress(tt.listen, ListenResolveOptions{})
			if err != nil {
				t.Fatalf("ResolveListenAddress returned error: %v", err)
			}
			if got.Listen != tt.wantListen || got.Host != tt.wantHost || got.Port != tt.wantPort || got.Scope != ListenerScopeLoopback {
				t.Fatalf("resolved listen = %#v, want listen %q host %q port %q loopback scope", got, tt.wantListen, tt.wantHost, tt.wantPort)
			}
		})
	}
}

func TestResolveListenAddressRejectsInvalidTargetsBeforeInterfaceLookup(t *testing.T) {
	tests := []string{
		"0.0.0.0:8080",
		":8080",
		"[::]:8080",
		"192.168.1.20:8080",
		"example.com:8080",
		"localhost.:8080",
		"[::ffff:127.0.0.1]:8080",
		"127.1:8080",
		"[fe80::1%lo0]:8080",
		"tailscale:",
		"tailscale:http",
		"tailscale:65536",
	}

	for _, listen := range tests {
		t.Run(listen, func(t *testing.T) {
			called := false
			_, err := ResolveListenAddress(listen, ListenResolveOptions{
				Interfaces: func() ([]ListenInterface, error) {
					called = true
					return nil, errors.New("interface lookup should not run")
				},
			})
			if err == nil {
				t.Fatal("expected validation error")
			}
			if called {
				t.Fatal("interface lookup ran for syntactically invalid listen target")
			}
		})
	}
}

func TestResolveListenAddressResolvesTailscaleIPv4(t *testing.T) {
	got, err := ResolveListenAddress("tailscale:8080", ListenResolveOptions{
		TailscaleIPs: fakeTailscaleIPs("100.88.77.66"),
		Interfaces: fakeListenInterfaces(
			fakeListenInterface("utun8", net.FlagUp, "100.88.77.66/32"),
		),
	})
	if err != nil {
		t.Fatalf("ResolveListenAddress returned error: %v", err)
	}
	if got.Listen != "100.88.77.66:8080" || got.Host != "100.88.77.66" || got.Port != "8080" || got.Scope != ListenerScopeTailscale {
		t.Fatalf("resolved listen = %#v, want concrete Tailscale IPv4 listen", got)
	}
}

func TestResolveListenAddressResolvesTailscaleIPv6WhenIPv4Unavailable(t *testing.T) {
	got, err := ResolveListenAddress("tailscale:8080", ListenResolveOptions{
		TailscaleIPs: fakeTailscaleIPs("fd7a:115c:a1e0::1234"),
		Interfaces: fakeListenInterfaces(
			fakeListenInterface("utun8", net.FlagUp, "fd7a:115c:a1e0::1234/128"),
		),
	})
	if err != nil {
		t.Fatalf("ResolveListenAddress returned error: %v", err)
	}
	if got.Listen != "[fd7a:115c:a1e0::1234]:8080" || got.Host != "fd7a:115c:a1e0::1234" || got.Port != "8080" || got.Scope != ListenerScopeTailscale {
		t.Fatalf("resolved listen = %#v, want concrete Tailscale IPv6 listen", got)
	}
}

func TestResolveListenAddressPrefersTailscaleIPv4(t *testing.T) {
	got, err := ResolveListenAddress("tailscale:0", ListenResolveOptions{
		TailscaleIPs: fakeTailscaleIPs("fd7a:115c:a1e0::1234", "100.88.77.66"),
		Interfaces: fakeListenInterfaces(
			fakeListenInterface("utun8", net.FlagUp, "fd7a:115c:a1e0::1234/128", "100.88.77.66/32"),
		),
	})
	if err != nil {
		t.Fatalf("ResolveListenAddress returned error: %v", err)
	}
	if got.Listen != "100.88.77.66:0" || got.Host != "100.88.77.66" || got.Port != "0" {
		t.Fatalf("resolved listen = %#v, want IPv4 candidate with port 0", got)
	}
}

func TestResolveListenAddressRejectsCGNATAddressNotReportedByTailscale(t *testing.T) {
	_, err := ResolveListenAddress("tailscale:8080", ListenResolveOptions{
		TailscaleIPs: fakeTailscaleIPs("100.99.99.99"),
		Interfaces: fakeListenInterfaces(
			fakeListenInterface("corp-vpn", net.FlagUp, "100.88.77.66/32"),
		),
	})
	if err == nil {
		t.Fatal("expected missing Tailscale address error")
	}
	want := `could not resolve tailscale listen target "tailscale:8080": no Tailscale address found on an up network interface`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestResolveListenAddressReportsMissingTailscaleAddress(t *testing.T) {
	tests := []struct {
		name       string
		interfaces []ListenInterface
	}{
		{name: "empty"},
		{name: "down", interfaces: []ListenInterface{fakeListenInterface("utun8", 0, "100.88.77.66/32")}},
		{name: "loopback", interfaces: []ListenInterface{fakeListenInterface("lo0", net.FlagUp|net.FlagLoopback, "100.88.77.66/32")}},
		{name: "private ipv4", interfaces: []ListenInterface{fakeListenInterface("en0", net.FlagUp, "192.168.1.10/24")}},
		{name: "unspecified ipv4", interfaces: []ListenInterface{fakeListenInterface("utun8", net.FlagUp, "0.0.0.0/32")}},
		{name: "unspecified ipv6", interfaces: []ListenInterface{fakeListenInterface("utun8", net.FlagUp, "::/128")}},
		{name: "outside tailscale ipv6", interfaces: []ListenInterface{fakeListenInterface("utun8", net.FlagUp, "fd00::1234/128")}},
		{name: "ipv4 mapped ipv6", interfaces: []ListenInterface{fakeListenInterface("utun8", net.FlagUp, "::ffff:100.88.77.66/128")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveListenAddress("tailscale:8080", ListenResolveOptions{
				TailscaleIPs: fakeTailscaleIPs("100.88.77.66", "fd7a:115c:a1e0::1234"),
				Interfaces:   fakeListenInterfaces(tt.interfaces...),
			})
			if err == nil {
				t.Fatal("expected missing Tailscale address error")
			}
			want := `could not resolve tailscale listen target "tailscale:8080": no Tailscale address found on an up network interface`
			if err.Error() != want {
				t.Fatalf("error = %q, want %q", err.Error(), want)
			}
		})
	}
}

func TestParseTailscaleIPOutput(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    []netip.Addr
		wantErr bool
	}{
		{
			name:   "ipv4 and ipv6",
			output: "100.88.77.66\nfd7a:115c:a1e0::1234\n",
			want: []netip.Addr{
				netip.MustParseAddr("100.88.77.66"),
				netip.MustParseAddr("fd7a:115c:a1e0::1234"),
			},
		},
		{name: "empty output"},
		{name: "invalid token", output: "100.88.77.66\nnot-an-ip\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTailscaleIPOutput([]byte(tt.output))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected parse error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTailscaleIPOutput returned error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("addresses = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("addresses = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestValidateListenAddressAcceptsOnlyLoopbackAndTailscaleTargets(t *testing.T) {
	accepted := []string{"localhost:8080", "127.0.0.1:0", "[::1]:8080", "tailscale:0", "tailscale:65535"}
	for _, listen := range accepted {
		t.Run("accepts "+listen, func(t *testing.T) {
			if err := ValidateListenAddress(listen); err != nil {
				t.Fatalf("ValidateListenAddress returned error: %v", err)
			}
		})
	}

	rejected := []string{"tailscale:", "tailscale:http", "tailscale:65536", "example.com:8080", "0.0.0.0:8080"}
	for _, listen := range rejected {
		t.Run("rejects "+listen, func(t *testing.T) {
			if err := ValidateListenAddress(listen); err == nil || !strings.Contains(err.Error(), "localhost, a loopback IP, or tailscale:PORT") {
				t.Fatalf("ValidateListenAddress error = %v, want listen validation error", err)
			}
		})
	}
}

func fakeListenInterfaces(interfaces ...ListenInterface) func() ([]ListenInterface, error) {
	return func() ([]ListenInterface, error) {
		return interfaces, nil
	}
}

func fakeTailscaleIPs(addrs ...string) func() ([]netip.Addr, error) {
	return func() ([]netip.Addr, error) {
		parsed := make([]netip.Addr, 0, len(addrs))
		for _, addr := range addrs {
			parsed = append(parsed, netip.MustParseAddr(addr))
		}
		return parsed, nil
	}
}

func fakeListenInterface(name string, flags net.Flags, prefixes ...string) ListenInterface {
	interfaceAddrs := make([]netip.Prefix, 0, len(prefixes))
	for _, prefix := range prefixes {
		interfaceAddrs = append(interfaceAddrs, netip.MustParsePrefix(prefix))
	}
	return ListenInterface{Name: name, Flags: flags, Addrs: interfaceAddrs}
}
