package main

import (
	"context"
	"fmt"
	"github.com/wgdl666/wgModelHub/config"
	v2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/genai"
	"github.com/wgdl666/wgModelHub/internal/provider"
	service "github.com/wgdl666/wgModelHub/internal/service/modelhub"
	"github.com/wgdl666/wgModelHub/models"
	"google.golang.org/grpc"
	"net"
	"os"
)

func main() {
	key := os.Getenv("GEMINI_API_KEY")
	p, e := genai.NewGemini(context.Background(), "live-local", key, "", "")
	if e != nil {
		panic("create provider failed")
	}
	cfg := config.Config{Providers: map[string]config.ProviderConfig{"live-local": {Models: []string{models.Gemini38Live}, Gemini: &config.GeminiProviderConfig{APIKey: key}}}}
	svc := service.New(config.NewLiveConfig(cfg), map[string]provider.Set{"live-local": {Live: p}}, nil)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		panic(e)
	}
	fmt.Println(listener.Addr())
	server := grpc.NewServer(grpc.MaxRecvMsgSize(20 << 20))
	v2.RegisterModelHubServiceServer(server, svc)
	if e = server.Serve(listener); e != nil {
		panic(e)
	}
}
