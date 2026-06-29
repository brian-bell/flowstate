package server_test

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/brian-bell/flowstate/server"
)

const testSPAShell = `<html><body><main>flowstate web placeholder</main></body></html>`

var testStaticAssets = fstest.MapFS{
	"_shell.html":       &fstest.MapFile{Data: []byte(testSPAShell)},
	"assets/app.js":     &fstest.MapFile{Data: []byte(`console.log("flowstate")`)},
	"assets/styles.css": &fstest.MapFile{Data: []byte(`body { color: #1b1b1b; }`)},
}

func TestHandlerServesStaticSPAShellAndAssets(t *testing.T) {
	handler := newStaticHandler(t, testStaticAssets)

	for _, tt := range []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantBody   string
		wantAllow  string
	}{
		{name: "root shell", method: http.MethodGet, path: "/", wantStatus: http.StatusOK, wantBody: testSPAShell},
		{name: "head root shell", method: http.MethodHead, path: "/", wantStatus: http.StatusOK},
		{name: "concrete asset", method: http.MethodGet, path: "/assets/app.js", wantStatus: http.StatusOK, wantBody: `console.log("flowstate")`},
		{name: "client route fallback", method: http.MethodGet, path: "/flows/placeholder", wantStatus: http.StatusOK, wantBody: testSPAShell},
		{name: "dotted flow route fallback", method: http.MethodGet, path: "/flows/a.b_c-2", wantStatus: http.StatusOK, wantBody: testSPAShell},
		{name: "dotted plan route fallback", method: http.MethodGet, path: "/plans/a.b", wantStatus: http.StatusOK, wantBody: testSPAShell},
		{name: "missing asset", method: http.MethodGet, path: "/assets/missing.js", wantStatus: http.StatusNotFound, wantBody: "404 page not found\n"},
		{name: "missing file extension", method: http.MethodGet, path: "/favicon.ico", wantStatus: http.StatusNotFound, wantBody: "404 page not found\n"},
		{name: "post client route", method: http.MethodPost, path: "/flows/placeholder", wantStatus: http.StatusMethodNotAllowed, wantBody: "method not allowed\n", wantAllow: "GET, HEAD"},
		{name: "post concrete asset", method: http.MethodPost, path: "/assets/app.js", wantStatus: http.StatusMethodNotAllowed, wantBody: "method not allowed\n", wantAllow: "GET, HEAD"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := newLocalRequest(tt.method, tt.path)
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)

			if res.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %q", res.Code, tt.wantStatus, res.Body.String())
			}
			if res.Body.String() != tt.wantBody {
				t.Fatalf("body = %q, want %q", res.Body.String(), tt.wantBody)
			}
			if got := res.Header().Get("Allow"); got != tt.wantAllow {
				t.Fatalf("Allow header = %q, want %q", got, tt.wantAllow)
			}
		})
	}
}

func TestHandlerDoesNotFallbackForAPILikePaths(t *testing.T) {
	handler := newStaticHandler(t, testStaticAssets)

	for _, path := range []string{"/graphql/anything", "/graphql/schema.graphql/extra", "/healthz/extra"} {
		t.Run(path, func(t *testing.T) {
			req := newLocalRequest(http.MethodGet, path)
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)

			if res.Code == http.StatusOK {
				t.Fatalf("status = 200, want non-200 for API-like path; body %q", res.Body.String())
			}
			if strings.Contains(res.Body.String(), "flowstate web placeholder") {
				t.Fatalf("API-like path received SPA shell:\n%s", res.Body.String())
			}
		})
	}
}

func TestStaticRoutesUseHostAndOriginValidation(t *testing.T) {
	handler := newStaticHandler(t, testStaticAssets)

	for _, path := range []string{"/", "/assets/app.js"} {
		t.Run("invalid host "+path, func(t *testing.T) {
			req := newLocalRequest(http.MethodGet, path)
			req.Host = "example.com:4321"
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)

			if res.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body %q", res.Code, res.Body.String())
			}
		})

		t.Run("invalid origin "+path, func(t *testing.T) {
			req := newLocalRequest(http.MethodGet, path)
			req.Header.Set("Origin", "http://example.com:4321")
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)

			if res.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body %q", res.Code, res.Body.String())
			}
		})
	}
}

func TestHandlerHealthzIsUnauthenticated(t *testing.T) {
	handler, err := server.NewHandler(server.HandlerOptions{
		Token:          "test-token",
		ListenerHost:   "127.0.0.1",
		ListenerPort:   "4321",
		AllowIPv6Alias: true,
	})
	if err != nil {
		t.Fatalf("NewHandler returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4321/healthz", nil)
	req.Host = "127.0.0.1:4321"
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200; body %q", res.Code, res.Body.String())
	}
	if strings.TrimSpace(res.Body.String()) != "ok" {
		t.Fatalf("healthz body = %q, want ok", res.Body.String())
	}
}

func newStaticHandler(t *testing.T, assets fs.FS) http.Handler {
	t.Helper()
	handler, err := server.NewHandler(server.HandlerOptions{
		Token:          "test-token",
		ListenerHost:   "127.0.0.1",
		ListenerPort:   "4321",
		AllowIPv6Alias: true,
		StaticAssets:   assets,
		SPAShell:       "_shell.html",
	})
	if err != nil {
		t.Fatalf("NewHandler returned error: %v", err)
	}
	return handler
}

func newLocalRequest(method string, path string) *http.Request {
	req := httptest.NewRequest(method, "http://127.0.0.1:4321"+path, nil)
	req.Host = "127.0.0.1:4321"
	return req
}

func TestHandlerServesGraphQLSchemaUnauthenticated(t *testing.T) {
	handler, err := server.NewHandler(server.HandlerOptions{
		Token:        "test-token",
		ListenerHost: "127.0.0.1",
		ListenerPort: "4321",
	})
	if err != nil {
		t.Fatalf("NewHandler returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4321/graphql/schema.graphql", nil)
	req.Host = "127.0.0.1:4321"
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("schema status = %d, want 200; body %q", res.Code, res.Body.String())
	}
	requireContainsAll(t, res.Body.String(), []string{
		"schema {",
		"type Query",
		"health: String!",
	})
}

func TestHandlerRestrictsUnauthenticatedEndpointMethods(t *testing.T) {
	handler, err := server.NewHandler(server.HandlerOptions{
		Token:        "test-token",
		ListenerHost: "127.0.0.1",
		ListenerPort: "4321",
	})
	if err != nil {
		t.Fatalf("NewHandler returned error: %v", err)
	}

	for _, path := range []string{"/healthz", "/graphql/schema.graphql"} {
		t.Run(path, func(t *testing.T) {
			headReq := httptest.NewRequest(http.MethodHead, "http://127.0.0.1:4321"+path, nil)
			headReq.Host = "127.0.0.1:4321"
			headRes := httptest.NewRecorder()
			handler.ServeHTTP(headRes, headReq)
			if headRes.Code != http.StatusOK {
				t.Fatalf("HEAD status = %d, want 200; body %q", headRes.Code, headRes.Body.String())
			}

			req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4321"+path, nil)
			req.Host = "127.0.0.1:4321"
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)

			if res.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405; body %q", res.Code, res.Body.String())
			}
			if got := res.Header().Get("Allow"); got != "GET, HEAD" {
				t.Fatalf("Allow header = %q, want GET, HEAD", got)
			}
		})
	}
}

func TestHandlerGraphQLRequiresExactBearerToken(t *testing.T) {
	handler, err := server.NewHandler(server.HandlerOptions{
		Token:        "test-token",
		ListenerHost: "127.0.0.1",
		ListenerPort: "4321",
	})
	if err != nil {
		t.Fatalf("NewHandler returned error: %v", err)
	}

	invalid := []struct {
		name    string
		headers []string
	}{
		{name: "missing"},
		{name: "duplicate", headers: []string{"Bearer test-token", "Bearer test-token"}},
		{name: "wrong scheme", headers: []string{"Basic test-token"}},
		{name: "empty token", headers: []string{"Bearer "}},
		{name: "extra credentials", headers: []string{"Bearer test-token extra"}},
		{name: "malformed", headers: []string{"Bearer"}},
		{name: "wrong token", headers: []string{"Bearer wrong-token"}},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			req := newGraphQLRequest()
			if tt.headers != nil {
				req.Header["Authorization"] = tt.headers
			}
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)

			if res.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body %q", res.Code, res.Body.String())
			}
		})
	}

	req := newGraphQLRequest()
	req.Header.Set("Authorization", "Bearer test-token")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want 200; body %q", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"health":"ok"`) {
		t.Fatalf("authorized response missing health data:\n%s", res.Body.String())
	}
}

func TestHandlerValidatesHost(t *testing.T) {
	handler, err := server.NewHandler(server.HandlerOptions{
		Token:          "test-token",
		ListenerHost:   "127.0.0.1",
		ListenerPort:   "4321",
		AllowIPv6Alias: true,
	})
	if err != nil {
		t.Fatalf("NewHandler returned error: %v", err)
	}

	for _, host := range []string{"127.0.0.1:4321", "localhost:4321", "[::1]:4321"} {
		t.Run("accepts "+host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4321/healthz", nil)
			req.Host = host
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)

			if res.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %q", res.Code, res.Body.String())
			}
		})
	}

	for _, host := range []string{
		"127.0.0.1:9999",
		"localhost.evil:4321",
		"127.0.0.1.evil:4321",
		"not a host",
		"",
		"example.com:4321",
	} {
		t.Run("rejects "+host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4321/healthz", nil)
			req.Host = host
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)

			if res.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body %q", res.Code, res.Body.String())
			}
		})
	}
}

func TestHandlerValidatesOrigin(t *testing.T) {
	handler, err := server.NewHandler(server.HandlerOptions{
		Token:          "test-token",
		ListenerHost:   "127.0.0.1",
		ListenerPort:   "4321",
		AllowIPv6Alias: true,
	})
	if err != nil {
		t.Fatalf("NewHandler returned error: %v", err)
	}

	for _, origin := range []string{"", "http://localhost:4321", "http://127.0.0.1:4321", "http://[::1]:4321"} {
		t.Run("accepts "+origin, func(t *testing.T) {
			req := newGraphQLRequest()
			req.Header.Set("Authorization", "Bearer test-token")
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)

			if res.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %q", res.Code, res.Body.String())
			}
		})
	}

	for _, origin := range []string{
		"http://example.com:4321",
		"http://localhost:9999",
		"https://localhost:4321",
		"://not-a-url",
		"null",
	} {
		t.Run("rejects "+origin, func(t *testing.T) {
			req := newGraphQLRequest()
			req.Header.Set("Authorization", "Bearer test-token")
			req.Header.Set("Origin", origin)
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)

			if res.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body %q", res.Code, res.Body.String())
			}
		})
	}
}

func TestRunGeneratesTokenReportsURLAndStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan server.Started, 1)
	done := make(chan error, 1)
	var stdout bytes.Buffer

	go func() {
		done <- server.Run(ctx, server.Options{
			Listen:  "127.0.0.1:0",
			Stdout:  &stdout,
			Started: started,
		})
	}()

	var info server.Started
	select {
	case info = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not report startup")
	}
	if info.URL == "" || !strings.HasPrefix(info.URL, "http://127.0.0.1:") {
		t.Fatalf("started URL = %q, want local 127.0.0.1 URL", info.URL)
	}
	if len(info.Token) != 43 {
		t.Fatalf("generated token length = %d, want 43 raw base64url chars", len(info.Token))
	}
	requireContainsAll(t, stdout.String(), []string{info.URL, info.Token})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop after cancel")
	}
}

func TestRunServesHTTPWithReportedURLAndToken(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan server.Started, 1)
	done := make(chan error, 1)

	go func() {
		done <- server.Run(ctx, server.Options{
			Listen:  "127.0.0.1:0",
			Stdout:  io.Discard,
			Started: started,
		})
	}()
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

	var info server.Started
	select {
	case info = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not report startup")
	}

	client := &http.Client{Timeout: 2 * time.Second}
	res, err := client.Get(info.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	body := readResponseBody(t, res)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200; body %q", res.StatusCode, body)
	}
	if strings.TrimSpace(body) != "ok" {
		t.Fatalf("healthz body = %q, want ok", body)
	}

	unauthorized := newHTTPGraphQLRequest(t, info.URL, "")
	res, err = client.Do(unauthorized)
	if err != nil {
		t.Fatalf("unauthorized POST /graphql: %v", err)
	}
	body = readResponseBody(t, res)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401; body %q", res.StatusCode, body)
	}

	authorized := newHTTPGraphQLRequest(t, info.URL, info.Token)
	res, err = client.Do(authorized)
	if err != nil {
		t.Fatalf("authorized POST /graphql: %v", err)
	}
	body = readResponseBody(t, res)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("authorized status = %d, want 200; body %q", res.StatusCode, body)
	}
	if !strings.Contains(body, `"health":"ok"`) {
		t.Fatalf("authorized response missing health data:\n%s", body)
	}
}

func newGraphQLRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4321/graphql", bytes.NewBufferString(`{"query":"{ health }"}`))
	req.Host = "127.0.0.1:4321"
	req.Header.Set("Content-Type", "application/json")
	return req
}

func newHTTPGraphQLRequest(t *testing.T, baseURL string, token string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/graphql", bytes.NewBufferString(`{"query":"{ health }"}`))
	if err != nil {
		t.Fatalf("new GraphQL request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func readResponseBody(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(body)
}

func requireContainsAll(t *testing.T, output string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}
