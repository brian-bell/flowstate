package server

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunResolvesTailscaleListenBeforeBinding(t *testing.T) {
	listener := newBlockingListener(&net.TCPAddr{IP: net.ParseIP("100.88.77.66"), Port: 8080})
	gotNetwork := ""
	gotAddress := ""

	info := runServerWithFakeTailscaleListener(t, Options{
		Listen: "tailscale:8080",
		Token:  "test-token",
		resolve: ListenResolveOptions{
			TailscaleIPs: fakeTailscaleIPs("100.88.77.66"),
			Interfaces: fakeListenInterfaces(
				fakeListenInterface("utun8", net.FlagUp, "100.88.77.66/32"),
			),
		},
		listen: func(network string, address string) (net.Listener, error) {
			gotNetwork = network
			gotAddress = address
			return listener, nil
		},
	})

	if gotNetwork != "tcp" {
		t.Fatalf("listen network = %q, want tcp", gotNetwork)
	}
	if gotAddress != "100.88.77.66:8080" {
		t.Fatalf("listen address = %q, want resolved Tailscale address", gotAddress)
	}
	if strings.Contains(gotAddress, "tailscale") || strings.HasPrefix(gotAddress, "0.0.0.0") || strings.HasPrefix(gotAddress, "[::]") {
		t.Fatalf("listen address must be concrete and non-wildcard, got %q", gotAddress)
	}
	if info.URL != "http://100.88.77.66:8080" {
		t.Fatalf("started URL = %q, want resolved Tailscale URL", info.URL)
	}
}

func TestRunReportsAssignedPortForTailscalePortZero(t *testing.T) {
	listener := newBlockingListener(&net.TCPAddr{IP: net.ParseIP("100.88.77.66"), Port: 4321})
	gotAddress := ""

	info := runServerWithFakeTailscaleListener(t, Options{
		Listen: "tailscale:0",
		Token:  "test-token",
		resolve: ListenResolveOptions{
			TailscaleIPs: fakeTailscaleIPs("100.88.77.66"),
			Interfaces: fakeListenInterfaces(
				fakeListenInterface("utun8", net.FlagUp, "100.88.77.66/32"),
			),
		},
		listen: func(_ string, address string) (net.Listener, error) {
			gotAddress = address
			return listener, nil
		},
	})

	if gotAddress != "100.88.77.66:0" {
		t.Fatalf("listen address = %q, want requested port zero on resolved Tailscale address", gotAddress)
	}
	if info.URL != "http://100.88.77.66:4321" {
		t.Fatalf("started URL = %q, want actual assigned listener port", info.URL)
	}
}

func runServerWithFakeTailscaleListener(t *testing.T, opts Options) Started {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan Started, 1)
	done := make(chan error, 1)
	opts.Stdout = io.Discard
	opts.Started = started

	go func() {
		done <- Run(ctx, opts)
	}()

	var info Started
	select {
	case info = <-started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("server did not report startup")
	}

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Run returned error after cancel: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("server did not stop after cancel")
		}
	})
	return info
}

type blockingListener struct {
	addr net.Addr

	closeOnce sync.Once
	closed    chan struct{}
}

func newBlockingListener(addr net.Addr) *blockingListener {
	return &blockingListener{addr: addr, closed: make(chan struct{})}
}

func (l *blockingListener) Accept() (net.Conn, error) {
	<-l.closed
	return nil, net.ErrClosed
}

func (l *blockingListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
	})
	return nil
}

func (l *blockingListener) Addr() net.Addr {
	return l.addr
}

var _ net.Listener = (*blockingListener)(nil)
