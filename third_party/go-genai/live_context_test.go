package genai

import (
	"context"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLiveConnectCancelDuringSetup(t *testing.T) {
	upgraded := make(chan struct{})
	released := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer c.Close()
		close(upgraded)
		for {
			if _, _, e = c.ReadMessage(); e != nil {
				close(released)
				return
			}
		}
	}))
	defer server.Close()
	client, e := NewClient(t.Context(), &ClientConfig{APIKey: "test", Backend: BackendGeminiAPI, HTTPOptions: HTTPOptions{APIVersion: "v1alpha", BaseURL: strings.Replace(server.URL, "http://", "ws://", 1)}})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := client.Live.Connect(ctx, "gemini-3.8-live", nil); done <- e }()
	select {
	case <-upgraded:
	case <-time.After(time.Second):
		t.Fatal("not connected")
	}
	cancel()
	select {
	case e = <-done:
		if e == nil {
			t.Fatal("canceled connect succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("setup ignored cancellation")
	}
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("socket leaked")
	}
}
