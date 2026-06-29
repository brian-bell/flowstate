package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/brian-bell/flowstate/server/graph"
	"github.com/brian-bell/flowstate/server/graph/generated"
	"github.com/brian-bell/flowstate/server/webassets"
)

//go:embed graph/schema.graphqls
var schemaGraphQL string

const DefaultSPAShell = "_shell.html"

type HandlerOptions struct {
	Token          string
	ListenerHost   string
	ListenerPort   string
	AllowIPv6Alias bool
	StaticAssets   fs.FS
	SPAShell       string
}

type Options struct {
	Listen  string
	Token   string
	Stdout  io.Writer
	Started chan<- Started
}

type Started struct {
	URL   string
	Token string
}

func Run(ctx context.Context, opts Options) error {
	listen := opts.Listen
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	if err := ValidateListenAddress(listen); err != nil {
		return err
	}
	token := opts.Token
	if token == "" {
		generated, err := generateToken()
		if err != nil {
			return err
		}
		token = generated
	}

	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer listener.Close()

	listenerHost, listenerPort, err := validatedListenerAddr(listener.Addr())
	if err != nil {
		return err
	}
	handler, err := NewHandler(HandlerOptions{
		Token:          token,
		ListenerHost:   listenerHost,
		ListenerPort:   listenerPort,
		AllowIPv6Alias: listenerHost == "::1",
	})
	if err != nil {
		return err
	}

	server := &http.Server{Handler: handler}
	serveErr := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	started := Started{
		URL:   "http://" + net.JoinHostPort(listenerHost, listenerPort),
		Token: token,
	}
	if opts.Stdout != nil {
		fmt.Fprintf(opts.Stdout, "URL: %s\nToken: %s\n", started.URL, started.Token)
	}
	if opts.Started != nil {
		select {
		case opts.Started <- started:
		case <-ctx.Done():
		}
	}

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-serveErr
	}
}

func NewHandler(opts HandlerOptions) (http.Handler, error) {
	if opts.Token == "" {
		return nil, errors.New("token is required")
	}
	if opts.ListenerHost == "" || opts.ListenerPort == "" {
		return nil, errors.New("listener host and port are required")
	}
	staticAssets, spaShell, err := staticAssetOptions(opts)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !allowGetOrHead(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/graphql/schema.graphql", func(w http.ResponseWriter, r *http.Request) {
		if !allowGetOrHead(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(schemaGraphQL))
	})
	graphqlHandler := handler.New(generated.NewExecutableSchema(generated.Config{Resolvers: &graph.Resolver{}}))
	graphqlHandler.AddTransport(transport.POST{})
	mux.Handle("/graphql", requireBearerToken(opts.Token, graphqlHandler))
	mux.Handle("/", newStaticSPAHandler(staticAssets, spaShell))
	return requireAllowedHost(opts, requireAllowedOrigin(opts, mux)), nil
}

func staticAssetOptions(opts HandlerOptions) (fs.FS, string, error) {
	staticAssets := opts.StaticAssets
	if staticAssets == nil {
		staticAssets = webassets.Assets()
	}
	spaShell := opts.SPAShell
	if spaShell == "" {
		spaShell = DefaultSPAShell
	}
	if !fs.ValidPath(spaShell) || strings.HasPrefix(spaShell, ".") || strings.HasSuffix(spaShell, "/") {
		return nil, "", fmt.Errorf("SPA shell path must be a clean relative file path: %q", spaShell)
	}
	info, err := fs.Stat(staticAssets, spaShell)
	if err != nil {
		return nil, "", fmt.Errorf("SPA shell %q is missing from static assets: %w", spaShell, err)
	}
	if info.IsDir() {
		return nil, "", fmt.Errorf("SPA shell %q is a directory", spaShell)
	}
	return staticAssets, spaShell, nil
}

func newStaticSPAHandler(staticAssets fs.FS, spaShell string) http.Handler {
	fileServer := http.FileServer(http.FS(staticAssets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isReservedAPIPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		if !allowGetOrHead(w, r) {
			return
		}
		if staticFileName, ok := exactStaticFile(staticAssets, r.URL.Path); ok {
			serveStaticPath(fileServer, w, r, staticFileName)
			return
		}
		if isStaticAssetRequest(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		serveStaticPath(fileServer, w, r, spaShell)
	})
}

func isReservedAPIPath(requestPath string) bool {
	return strings.HasPrefix(requestPath, "/healthz/") ||
		strings.HasPrefix(requestPath, "/graphql/")
}

func isStaticAssetRequest(requestPath string) bool {
	cleanPath := path.Clean("/" + requestPath)
	if cleanPath == "/assets" || strings.HasPrefix(cleanPath, "/assets/") {
		return true
	}
	return path.Dir(cleanPath) == "/" && path.Ext(cleanPath) != ""
}

func exactStaticFile(staticAssets fs.FS, requestPath string) (string, bool) {
	cleanPath := path.Clean("/" + requestPath)
	if cleanPath == "/" {
		return "", false
	}
	name := strings.TrimPrefix(cleanPath, "/")
	if !fs.ValidPath(name) {
		return "", false
	}
	info, err := fs.Stat(staticAssets, name)
	if err != nil || info.IsDir() {
		return "", false
	}
	return name, true
}

func serveStaticPath(fileServer http.Handler, w http.ResponseWriter, r *http.Request, staticPath string) {
	staticRequest := r.Clone(r.Context())
	staticRequest.URL.Path = "/" + staticPath
	staticRequest.URL.RawPath = ""
	fileServer.ServeHTTP(w, staticRequest)
}

func generateToken() (string, error) {
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(tokenBytes[:]), nil
}

func ValidateListenAddress(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil || host == "" || port == "" {
		return invalidListenAddress(listen)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return invalidListenAddress(listen)
	}
	if host == "localhost" {
		return nil
	}
	if strings.Contains(host, "%") {
		return invalidListenAddress(listen)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !addr.IsLoopback() || addr.Is4In6() {
		return invalidListenAddress(listen)
	}
	return nil
}

func invalidListenAddress(listen string) error {
	return fmt.Errorf("listen address must be host:port with host localhost or a loopback IP: %q", listen)
}

func validatedListenerAddr(addr net.Addr) (string, string, error) {
	if tcpAddr, ok := addr.(*net.TCPAddr); ok {
		if tcpAddr.IP == nil || !tcpAddr.IP.IsLoopback() {
			return "", "", fmt.Errorf("listener bound to non-loopback address %q", addr.String())
		}
		if tcpAddr.Port < 0 || tcpAddr.Port > 65535 {
			return "", "", fmt.Errorf("listener bound to invalid port %d", tcpAddr.Port)
		}
		return tcpAddr.IP.String(), strconv.Itoa(tcpAddr.Port), nil
	}

	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "", "", fmt.Errorf("listener bound to invalid address %q", addr.String())
	}
	parsed, err := netip.ParseAddr(host)
	if err != nil || !parsed.IsLoopback() {
		return "", "", fmt.Errorf("listener bound to non-loopback address %q", addr.String())
	}
	return host, port, nil
}

func allowGetOrHead(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func requireBearerToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headerValues := r.Header.Values("Authorization")
		if len(headerValues) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		got, ok := strings.CutPrefix(headerValues[0], "Bearer ")
		if !ok || got == "" || strings.ContainsAny(got, " \t\r\n") || !hmac.Equal([]byte(got), []byte(token)) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requireAllowedHost(opts HandlerOptions, next http.Handler) http.Handler {
	allowedHosts := allowedLoopbackHosts(opts)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.Host)
		if err != nil || port != opts.ListenerPort || !allowedHosts[host] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requireAllowedOrigin(opts HandlerOptions, next http.Handler) http.Handler {
	allowedHosts := allowedLoopbackHosts(opts)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origins := r.Header.Values("Origin")
		if len(origins) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		if len(origins) != 1 || !isAllowedOrigin(origins[0], opts.ListenerPort, allowedHosts) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isAllowedOrigin(origin string, port string, allowedHosts map[string]bool) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Port() == port && allowedHosts[u.Hostname()]
}

func allowedLoopbackHosts(opts HandlerOptions) map[string]bool {
	allowedHosts := map[string]bool{
		"localhost":       true,
		"127.0.0.1":       true,
		opts.ListenerHost: true,
	}
	if opts.AllowIPv6Alias {
		allowedHosts["::1"] = true
	}
	return allowedHosts
}
