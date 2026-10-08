package coherererank

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (r roundTrip) Do(req *http.Request) (*http.Response, error) { return r(req) }

func TestRerankDropsInstructAndReturnsScores(t *testing.T) {
	p, err := New("cohere", "https://api.cohere.com", "key")
	if err != nil {
		t.Fatal(err)
	}
	p.client = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.cohere.com/v2/rerank" {
			t.Fatalf("url %s", r.URL.String())
		}
		if r.Header.Get("authorization") != "Bearer key" {
			t.Fatalf("authorization %s", r.Header.Get("authorization"))
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		if _, ok := payload["instruct"]; ok {
			t.Fatalf("instruct must not be sent: %s", raw)
		}
		if payload["model"] != models.CohereRerankV35Official || payload["query"] != "红色" || payload["top_n"] != float64(2) {
			t.Fatalf("payload %s", raw)
		}
		body := `{"results":[{"index":1,"relevance_score":0.2},{"index":0,"relevance_score":0.9}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	event, err := p.Generate(context.Background(), models.CohereRerankV35Official, request(`{"query":"红色","documents":["a","b"],"instruct":"Given a color query"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := event.GetItems()[0].GetText(); !strings.Contains(got, `"relevance_score":0.9`) || strings.Contains(got, "instruct") {
		t.Fatalf("text %s", got)
	}
}

func TestRerankRejectsBedrockModel(t *testing.T) {
	p, err := New("cohere", "https://api.cohere.com", "key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(context.Background(), models.CohereRerankV35, request(`{"query":"红色","documents":["a"]}`)); err == nil {
		t.Fatal("bedrock model id must not be served by the official client")
	}
}

// TestRerankNewClientEmitsProviderHTTPSpan 自测真实 New() 客户端打本地上游时会落 provider.http（状态码/主机），用于坐实 429 vs 挂死。
func TestRerankNewClientEmitsProviderHTTPSpan(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/rerank" {
			t.Fatalf("path %s", r.URL.Path)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"index":0,"relevance_score":0.9}]}`)
	}))
	t.Cleanup(server.Close)

	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	p, err := New("cohere", server.URL, "key")
	if err != nil {
		t.Fatal(err)
	}
	if client, ok := p.client.(*http.Client); !ok || client.Timeout != 20*time.Second {
		t.Fatalf("client=%T timeout missing 20s", p.client)
	}
	if _, err := p.Generate(context.Background(), models.CohereRerankV35Official, request(`{"query":"红色","documents":["a"]}`)); err != nil {
		t.Fatal(err)
	}

	var httpSpan sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == "provider.http" {
			httpSpan = span
			break
		}
	}
	if httpSpan == nil {
		t.Fatal("missing provider.http span from coherererank New client")
	}
	var statusCode int64
	var host string
	for _, attr := range httpSpan.Attributes() {
		switch string(attr.Key) {
		case "http.response.status_code":
			statusCode = attr.Value.AsInt64()
		case "server.address":
			host = attr.Value.AsString()
		}
	}
	if statusCode != 200 {
		t.Fatalf("status_code=%d", statusCode)
	}
	if host != server.Listener.Addr().String() {
		t.Fatalf("server.address=%q want %s", host, server.Listener.Addr().String())
	}
}

func request(body string) *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Parts: []*modelhubv2.ContentPart{{
				Content: &modelhubv2.ContentPart_Text{Text: body},
			}}}},
		}}},
	}
}
