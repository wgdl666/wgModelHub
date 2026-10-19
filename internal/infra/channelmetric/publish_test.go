package channelmetric

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/wgdl666/wgModelHub/config"
	"github.com/wgdl666/wgModelHub/internal/infra/metricserver"
)

func TestChannelInfoIsScrapedWithoutSecrets(t *testing.T) {
	reader, err := metricserver.NewReader()
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := New(reader.Registerer())
	if err != nil {
		t.Fatal(err)
	}
	secret := "super-secret-key"
	err = publisher.Publish(map[string]config.ProviderConfig{
		"gemini_api": {Gemini: &config.GeminiProviderConfig{APIKey: secret, ProxyURL: "http://127.0.0.1:1081"}},
		"wgdl_video": {LTX: &config.LTXProviderConfig{BaseURL: "https://ltx.example/vton", Token: secret}, Models: []string{"ltx"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := scrape(t, reader.Handler())
	for _, want := range []string{
		`wg_modelhub_channel_info{channel="Gemini Developer API",host="generativelanguage.googleapis.com",instance="gemini_api",protocol="gemini"} 1`,
		`wg_modelhub_channel_info{channel="薇光点亮",host="ltx.example",instance="wgdl_video",protocol="ltx"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s\n%s", want, body)
		}
	}
	if strings.Contains(body, secret) || strings.Contains(body, "127.0.0.1") {
		t.Fatalf("metrics leaked secret or proxy:\n%s", body)
	}

	if err := publisher.Publish(map[string]config.ProviderConfig{
		"Vertex_AI": {VertexAI: &config.VertexAIProviderConfig{Project: "test-project"}},
	}); err != nil {
		t.Fatal(err)
	}
	body = scrape(t, reader.Handler())
	if strings.Contains(body, "gemini_api") || strings.Contains(body, "wgdl_video") {
		t.Fatalf("stale instances remained:\n%s", body)
	}
	if !strings.Contains(body, `instance="Vertex_AI"`) || !strings.Contains(body, `host="aiplatform.googleapis.com"`) {
		t.Fatalf("vertex missing:\n%s", body)
	}
}

func TestRegistererIsIndependent(t *testing.T) {
	registry := prometheus.NewRegistry()
	if _, err := New(registry); err != nil {
		t.Fatal(err)
	}
}

func scrape(t *testing.T, handler http.Handler) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	return string(body)
}
