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

// TestOpenPublicURLCreateRequestKeepsOrdinaryError：共享下载不可全局 NotAttempted，
// 否则结果下载失败会被 service.shouldRecord 丢弃已提交生成的账本。
func TestOpenPublicURLCreateRequestKeepsOrdinaryError(t *testing.T) {
	err := func() error {
		_, err := OpenPublicURL(context.Background(), http.DefaultClient, "test", "://bad")
		return err
	}()
	if err == nil {
		t.Fatal("expected create request error")
	}
	if IsNotAttempted(err) {
		t.Fatal("shared OpenPublicURL must not mark NotAttempted")
	}
}
