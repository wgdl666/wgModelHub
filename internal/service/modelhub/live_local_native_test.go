package modelhub

import (
	"github.com/wgdl666/wgModelHub/config"
	v2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/genai"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"google.golang.org/grpc"
	"net"
	"os"
	"testing"
)

// 本地真人录音回放专用入口，只有显式环境变量才建立真实供应商连接。
func TestDiagnosticLocalServer(t *testing.T) {
	path := os.Getenv("LIVE_DIAGNOSTIC_ADDRESS_FILE")
	if path == "" {
		t.Skip("local replay only")
	}
	p, e := genai.NewGemini(t.Context(), "local", os.Getenv("GEMINI_API_KEY"), "", "")
	if e != nil {
		t.Fatal(e)
	}
	cfg := config.Config{Providers: map[string]config.ProviderConfig{"local": {Models: []string{models.Gemini38Live}, Gemini: &config.GeminiProviderConfig{APIKey: os.Getenv("GEMINI_API_KEY")}}}}
	service := newTestService(cfg, map[string]provider.Set{"local": {Live: p}}, nil)
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	server := grpc.NewServer()
	v2.RegisterModelHubServiceServer(server, service)
	if e = os.WriteFile(path, []byte(l.Addr().String()), 0600); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(server.Stop)
	if e = server.Serve(l); e != nil {
		t.Fatal(e)
	}
}
