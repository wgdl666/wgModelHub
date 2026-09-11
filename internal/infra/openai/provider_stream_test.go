package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
)

func TestGenerateStreamForwardsQwenToolCallDeltasAsIs(t *testing.T) {
	// 第一包只有 name，第二包只有 arguments 碎片；ModelHub 不得改写成完整快照。
	sse := strings.Join([]string{
		`data: {"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"closet_search","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"query\":\"白衬衫\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	p := &Provider{name: "openai", apiKey: "test", baseURL: srv.URL, client: srv.Client()}
	var calls []*modelhubv2.ToolCall
	_, err := p.GenerateStream(context.Background(), "qwen3.8-flash", &modelhubv2.GenerateRequest{}, func(event *modelhubv2.GenerateEvent) error {
		for _, item := range event.GetItems() {
			if call := item.GetToolCall(); call != nil {
				calls = append(calls, call)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("forwarded tool deltas = %d, want 2", len(calls))
	}
	if calls[0].GetId() != "c1" || calls[0].GetName() != "closet_search" || string(calls[0].GetArgumentsJson()) != "" {
		t.Fatalf("first delta = %#v", calls[0])
	}
	if calls[1].GetId() != "" || calls[1].GetName() != "" || string(calls[1].GetArgumentsJson()) != `{"query":"白衬衫"}` {
		t.Fatalf("second delta must stay args-only, got %#v", calls[1])
	}
}

func TestGenerateStreamHTTPErrorIncludesVendorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"tool_choice is invalid","code":"invalid_parameter_error"}}`)
	}))
	defer srv.Close()

	p := &Provider{name: "hub_dashscope", apiKey: "test", baseURL: srv.URL, client: srv.Client()}
	_, err := p.GenerateStream(context.Background(), "qwen3.8-flash", &modelhubv2.GenerateRequest{}, nil)
	if err == nil {
		t.Fatal("expected HTTP 400")
	}
	got := err.Error()
	if !strings.Contains(got, "hub_dashscope returned HTTP 400") || !strings.Contains(got, "tool_choice is invalid") {
		t.Fatalf("error = %q", got)
	}
}
