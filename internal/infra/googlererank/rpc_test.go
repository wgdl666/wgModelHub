package googlererank

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	modelhub "github.com/wgdl666/wgModelHub/internal/service/modelhub"
	"github.com/wgdl666/wgModelHub/models"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"gopkg.in/yaml.v3"
)

func rpcClient(t *testing.T, p *Provider) (modelhubv2.ModelHubServiceClient, *config.LiveConfig) {
	t.Helper()
	cfg := config.Config{Providers: map[string]config.ProviderConfig{
		"google": {Models: []string{models.GoogleSemanticRankerDefault005}, GoogleRerank: &config.GoogleRerankProviderConfig{Project: p.project}},
	}}
	cfg.Logfire.Token = "test-token"
	cfg.Database.DSN = "postgres://unused/modelhub"
	live := config.NewLiveConfig(cfg)
	server := grpc.NewServer()
	modelhubv2.RegisterModelHubServiceServer(server, modelhub.New(live, map[string]provider.Set{"google": {Text: p}}, nil))
	listener := bufconn.Listen(1 << 20)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///google-rerank-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return modelhubv2.NewModelHubServiceClient(conn), live
}

// 真实服务层必须按模型路由进入适配器，且两种跑法都只返回一个 final。
func TestGenerateRPCAndListModels(t *testing.T) {
	p := testProvider(t, 200, `{"records":[{"id":"1","score":0.9},{"id":"0","score":0.1}]}`)
	client, _ := rpcClient(t, p)
	catalog, err := client.ListModels(context.Background(), &modelhubv2.ListModelsRequest{Category: modelhubv2.ModelCategory_MODEL_CATEGORY_RERANK})
	if err != nil || len(catalog.GetModels()) != 1 || catalog.GetModels()[0].GetModel() != models.GoogleSemanticRankerDefault005 {
		t.Fatalf("catalog=%v err=%v", catalog, err)
	}
	for _, streaming := range []bool{false, true} {
		req := request(testInput)
		req.Output.Stream = streaming
		stream, err := client.Generate(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		var text string
		finals := 0
		for {
			event, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if event.Final {
				finals++
				if streaming && len(event.Items) != 0 {
					t.Fatal("stream final must contain only metadata")
				}
			}
			for _, item := range event.Items {
				text += item.GetText()
			}
		}
		if finals != 1 || text != `{"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.1}]}` {
			t.Fatalf("stream=%t finals=%d text=%s", streaming, finals, text)
		}
	}
}

// AppConfig/Nacos 的模型列表更新后复用原客户端；未开放的 ID 不能触发上游调用。
func TestConfiguredModelsHotReloadAndPassThrough(t *testing.T) {
	seen := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		seen <- payload.Model
		io.WriteString(w, `{"records":[{"id":"1","score":0.9},{"id":"0","score":0.1}]}`)
	}))
	defer upstream.Close()
	p := testProvider(t, 200, "")
	p.endpoint, p.client = upstream.URL, upstream.Client()
	client, live := rpcClient(t, p)
	call := func(model string) error {
		req := request(testInput)
		req.Model = model
		stream, err := client.Generate(context.Background(), req)
		if err != nil {
			return err
		}
		_, err = stream.Recv()
		return err
	}
	if err := call(models.GoogleSemanticRankerFast005); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("undeclared model: %v", err)
	}
	select {
	case model := <-seen:
		t.Fatalf("undeclared model called Google: %s", model)
	default:
	}
	previous := models.GoogleSemanticRankerDefault005
	for _, model := range []string{models.GoogleSemanticRankerFast005, models.GoogleSemanticRankerDefaultLatest, models.GoogleSemanticRankerFastLatest, models.GoogleSemanticRankerDefault005} {
		cfg := live.Load()
		pCfg := cfg.Providers["google"]
		pCfg.Models = []string{model}
		// 替换配置副本，避免在热更新校验前改动正在服务的路由。
		cfg.Providers = map[string]config.ProviderConfig{"google": pCfg}
		raw, err := yaml.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		live.ApplyYAML(string(raw))
		catalog, err := client.ListModels(context.Background(), &modelhubv2.ListModelsRequest{Category: modelhubv2.ModelCategory_MODEL_CATEGORY_RERANK})
		if err != nil || len(catalog.GetModels()) != 1 || catalog.GetModels()[0].GetModel() != model {
			t.Fatalf("reloaded catalog=%v err=%v", catalog, err)
		}
		if err := call(previous); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("removed model %s: %v", previous, err)
		}
		if err := call(model); err != nil {
			t.Fatal(err)
		}
		if got := <-seen; got != model {
			t.Fatalf("upstream model=%q want=%q", got, model)
		}
		previous = model
	}
}
