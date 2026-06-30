package daemonclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClientMutationRetriesWithOneIdempotencyKey(t *testing.T) {
	transport := &sequenceTransport{responses: []roundTripResult{
		{status: http.StatusServiceUnavailable, body: `unavailable`},
		{status: http.StatusBadGateway, body: `bad gateway`},
		{status: http.StatusOK, body: `{"data":{"ok":true}}`},
	}}
	var sleeps []time.Duration
	client, err := New(Options{
		EndpointURL: "http://127.0.0.1:4321",
		Token:       "test-token",
		HTTPClient:  &http.Client{Transport: transport},
		MaxAttempts: 3,
		Backoff: func(attempt int) time.Duration {
			return time.Duration(attempt) * time.Millisecond
		},
		Sleep: func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		},
		NewID: func() (string, error) {
			return "idempotency-1", nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := client.mutation(context.Background(), `mutation { ok }`, nil, &out); err != nil {
		t.Fatalf("mutation: %v", err)
	}
	if !out.OK {
		t.Fatalf("mutation data = %#v, want ok", out)
	}
	if len(transport.requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(transport.requests))
	}
	for i, req := range transport.requests {
		if got := req.Header.Get("Idempotency-Key"); got != "idempotency-1" {
			t.Fatalf("request %d idempotency key = %q, want idempotency-1", i, got)
		}
	}
	if len(sleeps) != 2 || sleeps[0] != time.Millisecond || sleeps[1] != 2*time.Millisecond {
		t.Fatalf("sleeps = %#v, want 1ms and 2ms", sleeps)
	}
}

func TestClientMutationRetryExhaustionReturnsUnavailable(t *testing.T) {
	transport := &sequenceTransport{responses: []roundTripResult{
		{status: http.StatusServiceUnavailable, body: `unavailable`},
		{err: errors.New("dial failed")},
	}}
	client, err := New(Options{
		EndpointURL: "http://127.0.0.1:4321",
		Token:       "test-token",
		HTTPClient:  &http.Client{Transport: transport},
		MaxAttempts: 2,
		Backoff:     func(int) time.Duration { return 0 },
		Sleep:       func(context.Context, time.Duration) error { return nil },
		NewID:       func() (string, error) { return "idempotency-1", nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = client.mutation(context.Background(), `mutation { ok }`, nil, nil)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("mutation error = %v, want ErrUnavailable", err)
	}
}

func TestClientMutationMintsNewKeyPerLogicalCallAndQueriesHaveNoKey(t *testing.T) {
	transport := &sequenceTransport{responses: []roundTripResult{
		{status: http.StatusOK, body: `{"data":{"ok":true}}`},
		{status: http.StatusOK, body: `{"data":{"ok":true}}`},
		{status: http.StatusOK, body: `{"data":{"ok":true}}`},
	}}
	var next int
	client, err := New(Options{
		EndpointURL: "http://127.0.0.1:4321",
		Token:       "test-token",
		HTTPClient:  &http.Client{Transport: transport},
		NewID: func() (string, error) {
			next++
			return "idempotency-" + string(rune('0'+next)), nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.mutation(context.Background(), `mutation { ok }`, nil, nil); err != nil {
		t.Fatalf("first mutation: %v", err)
	}
	if err := client.mutation(context.Background(), `mutation { ok }`, nil, nil); err != nil {
		t.Fatalf("second mutation: %v", err)
	}
	if err := client.query(context.Background(), `query { ok }`, nil, nil); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got := transport.requests[0].Header.Get("Idempotency-Key"); got != "idempotency-1" {
		t.Fatalf("first mutation key = %q", got)
	}
	if got := transport.requests[1].Header.Get("Idempotency-Key"); got != "idempotency-2" {
		t.Fatalf("second mutation key = %q", got)
	}
	if got := transport.requests[2].Header.Get("Idempotency-Key"); got != "" {
		t.Fatalf("query idempotency key = %q, want empty", got)
	}
}

func TestClientUnauthorizedIsNotRetried(t *testing.T) {
	transport := &sequenceTransport{responses: []roundTripResult{
		{status: http.StatusUnauthorized, body: `unauthorized`},
		{status: http.StatusOK, body: `{"data":{"ok":true}}`},
	}}
	client, err := New(Options{
		EndpointURL: "http://127.0.0.1:4321",
		Token:       "test-token",
		HTTPClient:  &http.Client{Transport: transport},
		MaxAttempts: 2,
		NewID:       func() (string, error) { return "idempotency-1", nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = client.mutation(context.Background(), `mutation { ok }`, nil, nil)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("mutation error = %v, want ErrUnauthorized", err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(transport.requests))
	}
}

type sequenceTransport struct {
	responses []roundTripResult
	requests  []*http.Request
}

type roundTripResult struct {
	status int
	body   string
	err    error
}

func (t *sequenceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	t.requests = append(t.requests, clone)
	if len(t.responses) == 0 {
		return nil, errors.New("unexpected request")
	}
	next := t.responses[0]
	t.responses = t.responses[1:]
	if next.err != nil {
		return nil, next.err
	}
	return &http.Response{
		StatusCode: next.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(next.body)),
		Request:    req,
	}, nil
}
