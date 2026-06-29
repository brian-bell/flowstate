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
			host, port, err := validatedListenerAddr(tt.addr)
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
