package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brian-bell/flowstate/internal/daemoncoords"
	"github.com/brian-bell/flowstate/internal/version"
)

func runInBackground(t *testing.T, opts Options) (<-chan Started, <-chan error, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan Started, 1)
	done := make(chan error, 1)
	opts.Stdout = io.Discard
	opts.Started = started
	go func() {
		done <- Run(ctx, opts)
	}()
	return started, done, cancel
}

func waitStarted(t *testing.T, started <-chan Started, done <-chan error) Started {
	t.Helper()
	select {
	case info := <-started:
		return info
	case err := <-done:
		t.Fatalf("Run returned before startup: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("server did not report startup")
	}
	return Started{}
}

func waitStopped(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop after cancel")
	}
}

func TestRunPublishesCoordsAfterSuccessfulBind(t *testing.T) {
	coordsPath := filepath.Join(t.TempDir(), "daemon.json")
	t.Setenv("FLOWSTATE_DAEMON_COORDS", coordsPath)

	started, done, cancel := runInBackground(t, Options{
		Listen:        "127.0.0.1:0",
		Token:         "test-token",
		StateRoot:     t.TempDir(),
		PublishCoords: true,
	})
	info := waitStarted(t, started, done)
	t.Cleanup(func() {
		cancel()
		waitStopped(t, done)
	})

	coords, err := daemoncoords.Read()
	if err != nil {
		t.Fatalf("read published coords: %v", err)
	}
	if coords.URL != info.URL {
		t.Fatalf("coords URL = %q, want started URL %q", coords.URL, info.URL)
	}
	if !strings.HasPrefix(coords.URL, "http://127.0.0.1:") {
		t.Fatalf("coords URL = %q, want loopback URL with actual port", coords.URL)
	}
	if coords.Token != info.Token {
		t.Fatalf("coords token = %q, want started token %q", coords.Token, info.Token)
	}
	if coords.Version != version.String() {
		t.Fatalf("coords version = %q, want %q", coords.Version, version.String())
	}
	if coords.PID != os.Getpid() {
		t.Fatalf("coords pid = %d, want %d", coords.PID, os.Getpid())
	}
}

func TestRunDoesNotPublishCoordsWhenDisabled(t *testing.T) {
	coordsPath := filepath.Join(t.TempDir(), "daemon.json")
	t.Setenv("FLOWSTATE_DAEMON_COORDS", coordsPath)

	started, done, cancel := runInBackground(t, Options{
		Listen:    "127.0.0.1:0",
		Token:     "test-token",
		StateRoot: t.TempDir(),
	})
	waitStarted(t, started, done)
	t.Cleanup(func() {
		cancel()
		waitStopped(t, done)
	})

	if _, err := os.Stat(coordsPath); !os.IsNotExist(err) {
		t.Fatalf("coords file present with publishing disabled, stat err = %v", err)
	}
}

func TestRunRemovesOwnedPublishedCoordsOnShutdown(t *testing.T) {
	coordsPath := filepath.Join(t.TempDir(), "daemon.json")
	t.Setenv("FLOWSTATE_DAEMON_COORDS", coordsPath)

	started, done, cancel := runInBackground(t, Options{
		Listen:        "127.0.0.1:0",
		Token:         "test-token",
		StateRoot:     t.TempDir(),
		PublishCoords: true,
	})
	waitStarted(t, started, done)
	if _, err := os.Stat(coordsPath); err != nil {
		t.Fatalf("coords file missing while running: %v", err)
	}

	cancel()
	waitStopped(t, done)

	if _, err := os.Stat(coordsPath); !os.IsNotExist(err) {
		t.Fatalf("coords file still present after shutdown, stat err = %v", err)
	}
}

func TestRunDoesNotRemoveReplacedCoordsOnShutdown(t *testing.T) {
	coordsPath := filepath.Join(t.TempDir(), "daemon.json")
	t.Setenv("FLOWSTATE_DAEMON_COORDS", coordsPath)

	started, done, cancel := runInBackground(t, Options{
		Listen:        "127.0.0.1:0",
		Token:         "test-token",
		StateRoot:     t.TempDir(),
		PublishCoords: true,
	})
	waitStarted(t, started, done)

	replacement := daemoncoords.Coords{
		URL:     "http://127.0.0.1:9999",
		Token:   "other-token",
		PID:     os.Getpid() + 1,
		Version: "other-version",
	}
	data, err := json.Marshal(replacement)
	if err != nil {
		t.Fatalf("marshal replacement coords: %v", err)
	}
	if err := os.WriteFile(coordsPath, data, 0o600); err != nil {
		t.Fatalf("write replacement coords: %v", err)
	}

	cancel()
	waitStopped(t, done)

	got, err := daemoncoords.Read()
	if err != nil {
		t.Fatalf("read coords after shutdown: %v", err)
	}
	if got != replacement {
		t.Fatalf("coords = %+v, want replacement %+v preserved", got, replacement)
	}
}

func TestRunDoesNotWriteCoordsWhenBindFails(t *testing.T) {
	coordsPath := filepath.Join(t.TempDir(), "daemon.json")
	preexisting := []byte(`{"url":"http://127.0.0.1:1111","token":"pre","pid":1,"version":"pre"}`)
	if err := os.WriteFile(coordsPath, preexisting, 0o600); err != nil {
		t.Fatalf("write preexisting coords: %v", err)
	}
	t.Setenv("FLOWSTATE_DAEMON_COORDS", coordsPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := Run(ctx, Options{
		Listen:        "127.0.0.1:0",
		Token:         "test-token",
		StateRoot:     t.TempDir(),
		PublishCoords: true,
		Stdout:        io.Discard,
		listen: func(string, string) (net.Listener, error) {
			return nil, fmt.Errorf("bind failed")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "bind failed") {
		t.Fatalf("Run error = %v, want bind failure", err)
	}

	got, err := os.ReadFile(coordsPath)
	if err != nil {
		t.Fatalf("read coords after bind failure: %v", err)
	}
	if !bytes.Equal(got, preexisting) {
		t.Fatalf("coords file changed after bind failure: %s", got)
	}
}

func TestRunReturnsErrorDoesNotStartAndClosesListenersWhenPublishCoordsFails(t *testing.T) {
	fileParent := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(fileParent, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}
	t.Setenv("FLOWSTATE_DAEMON_COORDS", filepath.Join(fileParent, "daemon.json"))

	loopback := newBlockingListener(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5555})
	listen, _ := fakeListenByAddr(map[string]net.Listener{"127.0.0.1:0": loopback})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan Started, 1)
	err := Run(ctx, Options{
		Listen:        "127.0.0.1:0",
		Token:         "test-token",
		StateRoot:     t.TempDir(),
		PublishCoords: true,
		Stdout:        io.Discard,
		Started:       started,
		listen:        listen,
	})
	if err == nil || !strings.Contains(err.Error(), "coords") {
		t.Fatalf("Run error = %v, want coords publish failure", err)
	}
	if len(started) != 0 {
		t.Fatal("Run reported Started despite coords publish failure")
	}
	if !loopback.isClosed() {
		t.Fatal("bound listener not closed after coords publish failure")
	}
}

func fakeTailscaleResolve() ListenResolveOptions {
	return ListenResolveOptions{
		TailscaleIPs: fakeTailscaleIPs("100.88.77.66"),
		Interfaces: fakeListenInterfaces(
			fakeListenInterface("utun8", net.FlagUp, "100.88.77.66/32"),
		),
	}
}

// fakeListenByAddr returns a listen func that serves listeners keyed by their
// requested address and records every address it was asked to bind, in order.
func fakeListenByAddr(listeners map[string]net.Listener) (func(string, string) (net.Listener, error), *[]string) {
	var addrs []string
	listen := func(network string, address string) (net.Listener, error) {
		addrs = append(addrs, address)
		listener, ok := listeners[address]
		if !ok {
			return nil, fmt.Errorf("unexpected listen address %q", address)
		}
		return listener, nil
	}
	return listen, &addrs
}

func TestRunResolvesTailscaleListenBeforeBinding(t *testing.T) {
	loopback := newBlockingListener(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5555})
	tailscale := newBlockingListener(&net.TCPAddr{IP: net.ParseIP("100.88.77.66"), Port: 8080})
	listen, addrs := fakeListenByAddr(map[string]net.Listener{
		"127.0.0.1:0":       loopback,
		"100.88.77.66:8080": tailscale,
	})

	info := runServerWithFakeTailscaleListener(t, Options{
		Listen:  "tailscale:8080",
		Token:   "test-token",
		resolve: fakeTailscaleResolve(),
		listen:  listen,
	})

	if len(*addrs) != 2 || (*addrs)[0] != "127.0.0.1:0" || (*addrs)[1] != "100.88.77.66:8080" {
		t.Fatalf("bind addresses = %v, want loopback first then resolved Tailscale address", *addrs)
	}
	for _, address := range *addrs {
		if strings.Contains(address, "tailscale") || strings.HasPrefix(address, "0.0.0.0") || strings.HasPrefix(address, "[::]") {
			t.Fatalf("listen address must be concrete and non-wildcard, got %q", address)
		}
	}
	if info.URL != "http://127.0.0.1:5555" {
		t.Fatalf("started URL = %q, want loopback URL even in Tailscale scope", info.URL)
	}
}

func TestRunReportsAssignedPortForTailscalePortZero(t *testing.T) {
	loopback := newBlockingListener(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5555})
	tailscale := newBlockingListener(&net.TCPAddr{IP: net.ParseIP("100.88.77.66"), Port: 4321})
	listen, addrs := fakeListenByAddr(map[string]net.Listener{
		"127.0.0.1:0":    loopback,
		"100.88.77.66:0": tailscale,
	})

	info := runServerWithFakeTailscaleListener(t, Options{
		Listen:  "tailscale:0",
		Token:   "test-token",
		resolve: fakeTailscaleResolve(),
		listen:  listen,
	})

	if len(*addrs) != 2 || (*addrs)[0] != "127.0.0.1:0" || (*addrs)[1] != "100.88.77.66:0" {
		t.Fatalf("bind addresses = %v, want loopback then requested port zero on resolved Tailscale address", *addrs)
	}
	if info.URL != "http://127.0.0.1:5555" {
		t.Fatalf("started URL = %q, want loopback URL with assigned loopback port", info.URL)
	}
}

func TestBindListenersLoopbackTargetBindsOneListener(t *testing.T) {
	loopback := newBlockingListener(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5555})
	listen, addrs := fakeListenByAddr(map[string]net.Listener{"127.0.0.1:0": loopback})

	bound, url, err := bindListeners(ResolvedListen{
		Listen: "127.0.0.1:0",
		Host:   "127.0.0.1",
		Port:   "0",
		Scope:  ListenerScopeLoopback,
	}, listen)
	if err != nil {
		t.Fatalf("bindListeners returned error: %v", err)
	}
	if len(bound) != 1 {
		t.Fatalf("bound listeners = %d, want 1", len(bound))
	}
	if len(*addrs) != 1 || (*addrs)[0] != "127.0.0.1:0" {
		t.Fatalf("bind addresses = %v, want single loopback bind", *addrs)
	}
	if url != "http://127.0.0.1:5555" {
		t.Fatalf("loopback URL = %q, want assigned loopback port", url)
	}
	if bound[0].endpoint.Host != "127.0.0.1" || bound[0].endpoint.Port != "5555" {
		t.Fatalf("loopback endpoint = %+v, want 127.0.0.1:5555", bound[0].endpoint)
	}
}

func TestBindListenersTailscaleTargetBindsLoopbackAndTailscale(t *testing.T) {
	loopback := newBlockingListener(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5555})
	tailscale := newBlockingListener(&net.TCPAddr{IP: net.ParseIP("100.88.77.66"), Port: 8080})
	listen, addrs := fakeListenByAddr(map[string]net.Listener{
		"127.0.0.1:0":       loopback,
		"100.88.77.66:8080": tailscale,
	})

	bound, url, err := bindListeners(ResolvedListen{
		Listen: "100.88.77.66:8080",
		Host:   "100.88.77.66",
		Port:   "8080",
		Scope:  ListenerScopeTailscale,
	}, listen)
	if err != nil {
		t.Fatalf("bindListeners returned error: %v", err)
	}
	if len(bound) != 2 {
		t.Fatalf("bound listeners = %d, want 2", len(bound))
	}
	if len(*addrs) != 2 || (*addrs)[0] != "127.0.0.1:0" || (*addrs)[1] != "100.88.77.66:8080" {
		t.Fatalf("bind addresses = %v, want loopback first then resolved Tailscale address", *addrs)
	}
	if url != "http://127.0.0.1:5555" {
		t.Fatalf("loopback URL = %q, want loopback listener port", url)
	}
	if bound[0].endpoint.Host != "127.0.0.1" || bound[0].endpoint.Port != "5555" {
		t.Fatalf("loopback endpoint = %+v, want 127.0.0.1:5555", bound[0].endpoint)
	}
	if bound[1].endpoint.Host != "100.88.77.66" || bound[1].endpoint.Port != "8080" {
		t.Fatalf("tailscale endpoint = %+v, want 100.88.77.66:8080", bound[1].endpoint)
	}
}

func TestRunShutsDownAllListenersOnCancel(t *testing.T) {
	loopback := newBlockingListener(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5555})
	tailscale := newBlockingListener(&net.TCPAddr{IP: net.ParseIP("100.88.77.66"), Port: 8080})
	listen, _ := fakeListenByAddr(map[string]net.Listener{
		"127.0.0.1:0":       loopback,
		"100.88.77.66:8080": tailscale,
	})

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan Started, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Listen:  "tailscale:8080",
			Token:   "test-token",
			resolve: fakeTailscaleResolve(),
			listen:  listen,
			Stdout:  io.Discard,
			Started: started,
		})
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("server did not report startup")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop after cancel")
	}
	if !loopback.isClosed() || !tailscale.isClosed() {
		t.Fatalf("listeners closed: loopback=%v tailscale=%v, want both closed", loopback.isClosed(), tailscale.isClosed())
	}
}

func TestRunStopsAllListenersAfterServeError(t *testing.T) {
	serveErr := fmt.Errorf("boom")
	loopback := newErrorListener(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5555}, serveErr)
	tailscale := newBlockingListener(&net.TCPAddr{IP: net.ParseIP("100.88.77.66"), Port: 8080})
	listen, _ := fakeListenByAddr(map[string]net.Listener{
		"127.0.0.1:0":       loopback,
		"100.88.77.66:8080": tailscale,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Listen:  "tailscale:8080",
			Token:   "test-token",
			resolve: fakeTailscaleResolve(),
			listen:  listen,
			Stdout:  io.Discard,
		})
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("Run error = %v, want first serve error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not return after serve error")
	}
	if !tailscale.isClosed() {
		t.Fatal("remaining listener was not closed after serve error")
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

func (l *blockingListener) isClosed() bool {
	select {
	case <-l.closed:
		return true
	default:
		return false
	}
}

var _ net.Listener = (*blockingListener)(nil)

// errorListener returns a fixed error from the first Accept call, then blocks so
// http.Server treats the error as a serve failure.
type errorListener struct {
	addr net.Addr
	err  error

	once    sync.Once
	blocked chan struct{}

	closeOnce sync.Once
	closed    chan struct{}
}

func newErrorListener(addr net.Addr, err error) *errorListener {
	return &errorListener{addr: addr, err: err, blocked: make(chan struct{}), closed: make(chan struct{})}
}

func (l *errorListener) Accept() (net.Conn, error) {
	first := false
	l.once.Do(func() { first = true })
	if first {
		return nil, l.err
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *errorListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
	})
	return nil
}

func (l *errorListener) Addr() net.Addr {
	return l.addr
}

func (l *errorListener) isClosed() bool {
	select {
	case <-l.closed:
		return true
	default:
		return false
	}
}

var _ net.Listener = (*errorListener)(nil)
