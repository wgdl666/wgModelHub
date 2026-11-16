package vmindgateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestInvokeKeepsPerAdapterTimeoutWithoutChangingSharedClient(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/prefix/v1/gateway/requests/model" || r.Header.Get("Authorization") != "Bearer token" {
			t.Error("invalid Gateway request")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := New("vision", server.URL+"/prefix", "token", nil)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for _, timeout := range []time.Duration{60 * time.Second, 180 * time.Second} {
		workers.Go(func() {
			response, err := client.Invoke(t.Context(), "model", map[string]any{"image": "aW1hZ2U="}, timeout)
			if err != nil {
				t.Error(err)
				return
			}
			response.Body.Close()
		})
	}
	workers.Wait()
	if calls.Load() != 2 || client.http.Timeout != 125*time.Second {
		t.Fatalf("calls=%d timeout=%s", calls.Load(), client.http.Timeout)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Invoke(ctx, "model", map[string]any{}, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	if calls.Load() != 2 {
		t.Fatal("canceled request reached Gateway")
	}
}

func TestInvokeDoesNotFollowRedirects(t *testing.T) {
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer other.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer gateway.Close()
	client, err := New("vision", gateway.URL, "token", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Invoke(t.Context(), "model", map[string]any{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || leaked.Load() != 0 {
		t.Fatalf("status=%d forwarded=%d", response.StatusCode, leaked.Load())
	}
}
