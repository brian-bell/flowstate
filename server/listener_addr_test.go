package server

import (
	"net"
	"testing"
)

func TestValidatedListenerAddrRequiresLoopbackTCPAddress(t *testing.T) {
	tests := []struct {
		name     string
		addr     *net.TCPAddr
		wantHost string
		wantPort string
		wantErr  bool
	}{
		{
			name:     "ipv4 loopback",
			addr:     &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 4321},
			wantHost: "127.0.0.1",
			wantPort: "4321",
		},
		{
			name:     "ipv6 loopback",
			addr:     &net.TCPAddr{IP: net.ParseIP("::1"), Port: 4321},
			wantHost: "::1",
			wantPort: "4321",
		},
		{
			name:    "non loopback",
			addr:    &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 4321},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, err := validatedListenerAddr(tt.addr, ResolvedListen{Scope: ListenerScopeLoopback})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validatedListenerAddr returned nil error for %v", tt.addr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validatedListenerAddr returned error: %v", err)
			}
			if host != tt.wantHost || port != tt.wantPort {
				t.Fatalf("validatedListenerAddr = host %q port %q, want host %q port %q", host, port, tt.wantHost, tt.wantPort)
			}
		})
	}
}

func TestValidatedListenerAddrRequiresResolvedTailscaleAddress(t *testing.T) {
	resolved := ResolvedListen{
		Listen: "100.88.77.66:4321",
		Host:   "100.88.77.66",
		Port:   "4321",
		Scope:  ListenerScopeTailscale,
	}
	host, port, err := validatedListenerAddr(&net.TCPAddr{IP: net.ParseIP("100.88.77.66"), Port: 4321}, resolved)
	if err != nil {
		t.Fatalf("validatedListenerAddr returned error: %v", err)
	}
	if host != "100.88.77.66" || port != "4321" {
		t.Fatalf("validatedListenerAddr = host %q port %q, want resolved host and actual port", host, port)
	}

	for _, addr := range []*net.TCPAddr{
		{IP: net.ParseIP("100.88.77.67"), Port: 4321},
		{IP: net.ParseIP("0.0.0.0"), Port: 4321},
		{IP: net.ParseIP("::"), Port: 4321},
	} {
		t.Run(addr.String(), func(t *testing.T) {
			if _, _, err := validatedListenerAddr(addr, resolved); err == nil {
				t.Fatalf("validatedListenerAddr returned nil error for %v", addr)
			}
		})
	}
}
