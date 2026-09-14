package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"google.golang.org/protobuf/proto"
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
	for i, call := range calls {
		if call.Index == nil || *call.Index != 0 {
			t.Fatalf("delta[%d] index presence = %#v, want explicit 0", i, call.Index)
		}
	}
}

func TestGenerateStreamPreservesIndexZeroAcrossFragmentKinds(t *testing.T) {
	// 同一逻辑调用：id-only -> name-only -> 空片段 -> 多段 arguments；每包都保留 index=0 presence。
	sse := strings.Join([]string{
		`data: {"id":"chatcmpl-frag","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-frag","function":{"arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"closet_search","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"query\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"白衬衫\"}"}}]}}]}`,
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
	if len(calls) != 5 {
		t.Fatalf("forwarded fragments = %d, want 5", len(calls))
	}
	want := []struct {
		id   string
		name string
		args string
	}{
		{id: "call-frag"},
		{name: "closet_search"},
		{},
		{args: `{"query":`},
		{args: `"白衬衫"}`},
	}
	for i, call := range calls {
		if call.Index == nil || *call.Index != 0 {
			t.Fatalf("fragment[%d] index = %#v, want explicit 0", i, call.Index)
		}
		if call.GetId() != want[i].id || call.GetName() != want[i].name || string(call.GetArgumentsJson()) != want[i].args {
			t.Fatalf("fragment[%d] = id=%q name=%q args=%q, want %#v", i, call.GetId(), call.GetName(), call.GetArgumentsJson(), want[i])
		}
		roundTripped := &modelhubv2.ToolCall{}
		raw, err := proto.Marshal(call)
		if err != nil {
			t.Fatalf("marshal fragment[%d]: %v", i, err)
		}
		if err := proto.Unmarshal(raw, roundTripped); err != nil {
			t.Fatalf("unmarshal fragment[%d]: %v", i, err)
		}
		if roundTripped.Index == nil || *roundTripped.Index != 0 {
			t.Fatalf("round-trip fragment[%d] lost index presence: %#v", i, roundTripped.Index)
		}
	}
}

func TestGenerateStreamDistinguishesMissingIndexFromExplicitZero(t *testing.T) {
	// 锁定：供应商 JSON 缺 index 不得伪造成显式 0；显式 index:0 必须保留 presence。
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"id":"call-missing","function":{"name":"closet_search","arguments":"{}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-zero","function":{"name":"closet_search","arguments":"{}"}}]}}]}`,
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
		t.Fatalf("forwarded calls = %d, want 2", len(calls))
	}
	if calls[0].GetId() != "call-missing" {
		t.Fatalf("missing-index call id = %q, want call-missing", calls[0].GetId())
	}
	if calls[0].Index != nil {
		t.Fatalf("missing JSON index forged to %#v, want nil", calls[0].Index)
	}
	if calls[1].GetId() != "call-zero" {
		t.Fatalf("explicit-zero call id = %q, want call-zero", calls[1].GetId())
	}
	if calls[1].Index == nil || *calls[1].Index != 0 {
		t.Fatalf("explicit index:0 = %#v, want presence 0", calls[1].Index)
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
