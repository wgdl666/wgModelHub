package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDownloadPublicURLRejectsOversizedBody(t *testing.T) {
	const maxBytes = 8
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("123456789")) // 9 bytes，超过 maxBytes
	}))
	defer server.Close()

	_, err := DownloadPublicURL(context.Background(), server.Client(), "test", server.URL, maxBytes)
	if err == nil || !strings.Contains(err.Error(), "media exceeds") {
		t.Fatalf("err=%v", err)
	}
}
