package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

const defaultIdempotencyTTL = 10 * time.Minute

type IdempotencyCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]*idempotencyEntry
}

type IdempotencyOptions struct {
	TTL time.Duration
	Now func() time.Time
}

func NewIdempotencyCache(opts IdempotencyOptions) *IdempotencyCache {
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultIdempotencyTTL
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &IdempotencyCache{ttl: ttl, now: now, entries: make(map[string]*idempotencyEntry)}
}

type idempotencyEntry struct {
	identity  string
	done      chan struct{}
	response  cachedHTTPResponse
	cacheable bool
	expiresAt time.Time
}

type cachedHTTPResponse struct {
	status int
	header http.Header
	body   []byte
}

func withIdempotency(next http.Handler, cache *IdempotencyCache) http.Handler {
	if cache == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" || r.Method != http.MethodPost || r.URL.Path != "/graphql" {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request body", http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()
		if !isGraphQLMutation(body) {
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
			return
		}
		identity := idempotencyIdentity(r.Method, r.URL.Path, body)
		response, conflict := cache.replayOrRun(key, identity, func() cachedHTTPResponse {
			recorder := newCaptureResponseWriter()
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(recorder, r)
			return recorder.response()
		})
		if conflict {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"errors":[{"message":"idempotency key conflict"}],"data":null}`))
			return
		}
		writeCachedHTTPResponse(w, response)
	})
}

func (c *IdempotencyCache) replayOrRun(key, identity string, run func() cachedHTTPResponse) (cachedHTTPResponse, bool) {
	now := c.now()
	c.mu.Lock()
	c.cleanup(now)
	if entry, ok := c.entries[key]; ok {
		if entry.identity != identity {
			c.mu.Unlock()
			return cachedHTTPResponse{}, true
		}
		done := entry.done
		c.mu.Unlock()
		<-done
		return entry.response, false
	}
	entry := &idempotencyEntry{identity: identity, done: make(chan struct{})}
	c.entries[key] = entry
	c.mu.Unlock()

	response := run()
	cacheable := isCacheableGraphQLResponse(response)

	c.mu.Lock()
	entry.response = response
	entry.cacheable = cacheable
	entry.expiresAt = now.Add(c.ttl)
	close(entry.done)
	if !cacheable {
		delete(c.entries, key)
	}
	c.mu.Unlock()
	return response, false
}

func (c *IdempotencyCache) cleanup(now time.Time) {
	for key, entry := range c.entries {
		if !entry.cacheable {
			continue
		}
		if !entry.expiresAt.IsZero() && !now.Before(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
}

func idempotencyIdentity(method, path string, body []byte) string {
	sum := sha256.Sum256(body)
	return method + "\n" + path + "\n" + hex.EncodeToString(sum[:])
}

func isGraphQLMutation(body []byte) bool {
	var req struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	doc, err := parser.ParseQuery(&ast.Source{Input: req.Query})
	if err != nil {
		return false
	}
	for _, op := range doc.Operations {
		if op.Operation == ast.Mutation {
			return true
		}
	}
	return false
}

func isCacheableGraphQLResponse(response cachedHTTPResponse) bool {
	if response.status < 200 || response.status >= 300 {
		return false
	}
	var envelope map[string]json.RawMessage
	return json.Unmarshal(response.body, &envelope) == nil &&
		(envelope["data"] != nil || envelope["errors"] != nil)
}

func writeCachedHTTPResponse(w http.ResponseWriter, response cachedHTTPResponse) {
	for key, values := range response.header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(response.body)
}

type captureResponseWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newCaptureResponseWriter() *captureResponseWriter {
	return &captureResponseWriter{header: make(http.Header)}
}

func (w *captureResponseWriter) Header() http.Header {
	return w.header
}

func (w *captureResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *captureResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(data)
}

func (w *captureResponseWriter) response() cachedHTTPResponse {
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	return cachedHTTPResponse{
		status: status,
		header: w.header.Clone(),
		body:   append([]byte(nil), w.body.Bytes()...),
	}
}

func (w *captureResponseWriter) Flush() {}
