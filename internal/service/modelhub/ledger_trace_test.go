package modelhub

import (
	"context"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/callledger"
	"github.com/wgdl666/wgModelHub/internal/provider"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type uriStore struct {
	keys []string
	body []byte
}

func (s *uriStore) Put(_ context.Context, key, _ string, body []byte) (string, error) {
	s.keys = append(s.keys, key)
	s.body = append([]byte(nil), body...)
	return "s3://bucket/" + key, nil
}

func TestLedgerWritesTraceID(t *testing.T) {
	mem := &callledger.Memory{}
	text := &ledgerTextProvider{event: &modelhubv2.GenerateEvent{Items: []*modelhubv2.OutputItem{{Item: &modelhubv2.OutputItem_Text{Text: "ok"}}}}}
	svc := newLedgerService(textLedgerCFG("m"), map[string]provider.Set{"p": {Text: text}}, nil, mem)
	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("test").Start(context.Background(), "generate")
	defer span.End()
	req := &modelhubv2.GenerateRequest{
		Model:  "m",
		Input:  &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Role: modelhubv2.Role_ROLE_USER, Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "hi"}}}}}}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}
	if err := svc.Generate(req, &generateRecorder{ctx: ctx}); err != nil {
		t.Fatal(err)
	}
	if len(mem.Records) != 1 {
		t.Fatalf("records=%d", len(mem.Records))
	}
	if mem.Records[0].TraceID != span.SpanContext().TraceID().String() {
		t.Fatalf("trace=%q", mem.Records[0].TraceID)
	}
	if mem.Records[0].Status != callledger.StatusSucceeded {
		t.Fatalf("status=%s", mem.Records[0].Status)
	}
}

func TestLedgerUploadsImageBytes(t *testing.T) {
	mem := &callledger.Memory{}
	img := &ledgerImageProvider{event: &modelhubv2.GenerateEvent{Items: []*modelhubv2.OutputItem{{Item: &modelhubv2.OutputItem_Image{Image: &modelhubv2.Media{
		MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte("out")},
	}}}}}}
	svc := newLedgerService(config.Config{
		Providers: map[string]config.ProviderConfig{"p": {Models: []string{"m"}, OpenAI: &config.OpenAIProviderConfig{APIKey: "k", BaseURL: "https://x"}}},
	}, map[string]provider.Set{"p": {Image: img}}, nil, mem)
	store := &uriStore{}
	svc.SetObjectStore(store)
	req := &modelhubv2.GenerateRequest{
		Model: "m",
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
			Role: modelhubv2.Role_ROLE_USER,
			Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
				MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte("in")},
			}}}},
		}}}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
	}
	if err := svc.Generate(req, &generateRecorder{ctx: context.Background()}); err != nil {
		t.Fatal(err)
	}
	if len(store.keys) != 2 || string(store.body) != "out" {
		t.Fatalf("keys=%v body=%q", store.keys, store.body)
	}
	raw := mem.Records[0].InputPayload
	if !containsURI(raw, "s3://bucket/"+store.keys[0]) {
		t.Fatalf("input payload missing uri: %v", raw)
	}
}

func containsURI(value any, uri string) bool {
	switch node := value.(type) {
	case map[string]any:
		for _, child := range node {
			if text, ok := child.(string); ok && text == uri {
				return true
			}
			if containsURI(child, uri) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if containsURI(child, uri) {
				return true
			}
		}
	}
	return false
}
