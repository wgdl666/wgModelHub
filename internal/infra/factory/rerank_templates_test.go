package factory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

// 通过真实工厂和本地供应商 HTTP 验证两种协议、热更新与 stream 共用同一条转换路径。
func TestRerankTemplatesThroughFactory(t *testing.T) {
	received := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		received <- body
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"index":0,"relevance_score":0.9}],"data":[{"index":0,"score":0.9}],"output":{"results":[{"index":0,"relevance_score":0.9}]}}`))
	}))
	defer server.Close()
	cfg := config.Config{
		Database: config.DatabaseConfig{DSN: "postgres://test"}, Logfire: config.LogfireConfig{Token: "test"},
		RerankTemplates: map[string]config.RerankTemplate{
			"color": {Instruct: "main color", QueryTemplate: "主色「{{query}}」"},
			"look":  {Instruct: "all conditions", QueryTemplate: "外观「{{query}}」"},
		},
		Providers: map[string]config.ProviderConfig{
			"qwen":       {Models: []string{models.Qwen37TextRerank, models.Qwen3VLRerank}, DashScopeRerank: &config.DashScopeRerankProviderConfig{BaseURL: server.URL, APIKey: "test"}},
			"mixedbread": {Models: []string{models.MixedbreadRerankV31Listwise}, MixedbreadRerank: &config.MixedbreadRerankProviderConfig{BaseURL: server.URL, APIKey: "test"}},
			"cohere":     {Models: []string{models.CohereRerankV35Official}, CohereRerank: &config.CohereRerankProviderConfig{BaseURL: server.URL, APIKey: "test"}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	live := config.NewLiveConfig(cfg)
	sets, err := BuildLive(context.Background(), live)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"original", "updated"} {
		if version == "updated" {
			cfg.RerankTemplates = map[string]config.RerankTemplate{
				"color": {Instruct: "updated color", QueryTemplate: "新主色「{{query}}」"},
				"look":  {Instruct: "updated look", QueryTemplate: "新外观「{{query}}」"},
			}
			raw, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			live.ApplyYAML(string(raw))
		}
		for _, tc := range []struct {
			instance, model string
			native          bool
		}{
			{"qwen", models.Qwen37TextRerank, true}, {"qwen", models.Qwen3VLRerank, true},
			{"mixedbread", models.MixedbreadRerankV31Listwise, false}, {"cohere", models.CohereRerankV35Official, false},
		} {
			for _, key := range []string{"color", "look"} {
				text, _ := json.Marshal(map[string]any{"query": "红色通勤", "documents": []string{"{\"name\":\"shirt\"}"}, "instruction_key": key})
				req := &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Role: modelhubv2.Role_ROLE_USER, Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: string(text)}}}}}}}}}
				original := proto.Clone(req)
				client := sets[tc.instance].Text
				if key == "look" {
					_, err = client.GenerateStream(context.Background(), tc.model, req, func(*modelhubv2.GenerateEvent) error { return nil })
				} else {
					_, err = client.Generate(context.Background(), tc.model, req)
				}
				if err != nil {
					t.Fatalf("%s %s %s: %v", version, tc.model, key, err)
				}
				body := <-received
				if !proto.Equal(original, req) {
					t.Fatal("shared request mutated")
				}
				template := cfg.RerankTemplates[key]
				wantQuery := "红色通勤"
				if !tc.native {
					prefix := "主色"
					if key == "look" {
						prefix = "外观"
					}
					if version == "updated" {
						prefix = "新" + prefix
					}
					wantQuery = prefix + "「红色通勤」"
					if _, ok := body["instruct"]; ok {
						t.Fatal("unsupported instruct forwarded")
					}
				}
				gotQuery := body["query"]
				if tc.model == models.Qwen3VLRerank {
					gotQuery = body["input"].(map[string]any)["query"].(map[string]any)["text"]
					if body["parameters"].(map[string]any)["instruct"] != template.Instruct {
						t.Fatal("VL instruction missing")
					}
				} else if tc.native && body["instruct"] != template.Instruct {
					t.Fatal("text instruction missing")
				}
				if gotQuery != wantQuery {
					t.Fatalf("%s %s got query=%v want=%s", tc.model, key, gotQuery, wantQuery)
				}
				if _, ok := body["instruction_key"]; ok {
					t.Fatal("internal key forwarded")
				}
			}
		}
	}
	// 未配置的键在出站前失败，不能静默套用颜色模板。
	req := &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: `{"query":"q","documents":["d"],"instruction_key":"missing"}`}}}}}}}}}
	if _, err := sets["mixedbread"].Text.Generate(context.Background(), models.MixedbreadRerankV31Listwise, req); err == nil {
		t.Fatal("unknown key accepted")
	}
	select {
	case <-received:
		t.Fatal("invalid request reached supplier")
	default:
	}
}
