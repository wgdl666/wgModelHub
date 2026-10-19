package googlererank

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	modelhub "github.com/wgdl666/wgModelHub/internal/service/modelhub"
	"github.com/wgdl666/wgModelHub/models"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func rpcClient(t *testing.T, p *Provider) modelhubv2.ModelHubServiceClient {
	t.Helper()
	cfg := config.Config{Providers: map[string]config.ProviderConfig{
		"google": {Models: []string{models.GoogleSemanticRankerDefault005}, GoogleRerank: &config.GoogleRerankProviderConfig{Project: p.project}},
	}}
	server := grpc.NewServer()
	modelhubv2.RegisterModelHubServiceServer(server, modelhub.New(config.NewLiveConfig(cfg), map[string]provider.Set{"google": {Text: p}}, nil))
	listener := bufconn.Listen(1 << 20)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///google-rerank-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return modelhubv2.NewModelHubServiceClient(conn)
}

// 真实服务层必须按模型路由进入适配器，且两种跑法都只返回一个 final。
func TestGenerateRPCAndListModels(t *testing.T) {
	p := testProvider(t, 200, `{"records":[{"id":"1","score":0.9},{"id":"0","score":0.1}]}`)
	client := rpcClient(t, p)
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
