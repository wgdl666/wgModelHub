package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

func TestChatRequestDiagFieldsCountsInvalidHistoricalArguments(t *testing.T) {
	p := &Provider{}
	body := p.buildRequestBody(models.Qwen38Flash, &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{
			Tools: []*modelhubv2.Tool{{
				Function: &modelhubv2.FunctionDefinition{Name: "closet_search"},
			}},
			Items: []*modelhubv2.InputItem{
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role:  modelhubv2.Role_ROLE_USER,
					Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "找白衬衫"}}},
				}}},
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role: modelhubv2.Role_ROLE_ASSISTANT,
					ToolCalls: []*modelhubv2.ToolCall{
						{Id: "c1", Name: "closet_search", ArgumentsJson: []byte(`{"query":"白衬衫"}`)},
						{Id: "c2", Name: "closet_search", ArgumentsJson: []byte(`not-json`)},
						{Id: "c3", Name: "closet_search", ArgumentsJson: []byte(``)},
					},
				}}},
			},
		},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}, true)

	fields := chatRequestDiagFields(body)
	got := fieldMap(t, fields)

	if got["model"] != models.Qwen38Flash {
		t.Fatalf("model = %#v", got["model"])
	}
	if got["stream"] != true {
		t.Fatalf("stream = %#v", got["stream"])
	}
	if got["message_count"] != 2 {
		t.Fatalf("message_count = %#v", got["message_count"])
	}
	if got["tool_count"] != 1 {
		t.Fatalf("tool_count = %#v", got["tool_count"])
	}
	if got["assistant_tool_call_count"] != 3 {
		t.Fatalf("assistant_tool_call_count = %#v", got["assistant_tool_call_count"])
	}
	if got["invalid_function_arguments_count"] != 2 {
		t.Fatalf("invalid_function_arguments_count = %#v", got["invalid_function_arguments_count"])
	}

	invalid, ok := got["invalid_function_arguments"].([]invalidFunctionArgumentsEntry)
	if !ok || len(invalid) != 2 {
		t.Fatalf("invalid_function_arguments = %#v", got["invalid_function_arguments"])
	}
	if invalid[0].MessageIndex != 1 || invalid[0].ToolIndex != 1 || invalid[0].ToolName != "closet_search" || invalid[0].ArgumentsLen != len("not-json") {
		t.Fatalf("first invalid = %#v", invalid[0])
	}
	if invalid[1].MessageIndex != 1 || invalid[1].ToolIndex != 2 || invalid[1].ArgumentsLen != 0 {
		t.Fatalf("second invalid = %#v", invalid[1])
	}
	// 诊断不得把 arguments 原文带进字段值。
	encoded, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "not-json") || strings.Contains(string(encoded), "白衬衫") {
		t.Fatalf("invalid entries leaked argument text: %s", encoded)
	}
}

func TestChatRequestDiagFieldsOmitsInvalidListWhenAllValid(t *testing.T) {
	body := map[string]any{
		"model":  "m",
		"stream": false,
		"messages": []map[string]any{{
			"role": "assistant",
			"tool_calls": []map[string]any{{
				"id":   "c1",
				"type": "function",
				"function": map[string]any{
					"name":      "lookup",
					"arguments": `{"q":1}`,
				},
			}},
		}},
		"tools": []map[string]any{{"type": "function"}},
	}
	got := fieldMap(t, chatRequestDiagFields(body))
	if got["invalid_function_arguments_count"] != 0 {
		t.Fatalf("invalid count = %#v", got["invalid_function_arguments_count"])
	}
	if _, ok := got["invalid_function_arguments"]; ok {
		t.Fatalf("valid history must omit invalid list: %#v", got["invalid_function_arguments"])
	}
	if got["assistant_tool_call_count"] != 1 || got["tool_count"] != 1 || got["message_count"] != 1 {
		t.Fatalf("shape = %#v", got)
	}
}

func TestGenerateHTTPErrorLogIncludesRequestShapeDiagnostics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"The \"function.arguments\" parameter of the code model must be in JSON format."}}`)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	const apiKey = "sk-diag-secret-do-not-log"
	p := &Provider{name: "hub_dashscope", apiKey: apiKey, baseURL: srv.URL, client: srv.Client()}
	_, err := p.Generate(context.Background(), models.Qwen38Flash, &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{
			Tools: []*modelhubv2.Tool{{
				Function: &modelhubv2.FunctionDefinition{Name: "closet_search"},
			}},
			Items: []*modelhubv2.InputItem{
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role:  modelhubv2.Role_ROLE_USER,
					Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "secret-prompt-text"}}},
				}}},
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role: modelhubv2.Role_ROLE_ASSISTANT,
					ToolCalls: []*modelhubv2.ToolCall{{
						Id:            "c1",
						Name:          "closet_search",
						ArgumentsJson: []byte(`broken`),
					}},
				}}},
			},
		},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	})
	if err == nil {
		t.Fatal("expected HTTP 400")
	}
	if !strings.Contains(err.Error(), "function.arguments") {
		t.Fatalf("error mapping lost vendor body: %q", err.Error())
	}

	logLine := buf.String()
	if !strings.Contains(logLine, `"msg":"openai_http_error"`) {
		t.Fatalf("missing openai_http_error log: %s", logLine)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(logLine)), &rec); err != nil {
		t.Fatalf("parse log: %v\n%s", err, logLine)
	}
	if rec["provider"] != "hub_dashscope" {
		t.Fatalf("provider = %#v", rec["provider"])
	}
	if int(rec["status"].(float64)) != http.StatusBadRequest {
		t.Fatalf("status = %#v", rec["status"])
	}
	if rec["model"] != models.Qwen38Flash {
		t.Fatalf("model = %#v", rec["model"])
	}
	if rec["stream"] != false {
		t.Fatalf("stream = %#v", rec["stream"])
	}
	if int(rec["message_count"].(float64)) != 2 || int(rec["tool_count"].(float64)) != 1 {
		t.Fatalf("counts = message=%v tool=%v", rec["message_count"], rec["tool_count"])
	}
	if int(rec["assistant_tool_call_count"].(float64)) != 1 {
		t.Fatalf("assistant_tool_call_count = %#v", rec["assistant_tool_call_count"])
	}
	if int(rec["invalid_function_arguments_count"].(float64)) != 1 {
		t.Fatalf("invalid_function_arguments_count = %#v", rec["invalid_function_arguments_count"])
	}
	body, _ := rec["body"].(string)
	if !strings.Contains(body, "function.arguments") {
		t.Fatalf("body = %#v", rec["body"])
	}
	if strings.Contains(logLine, "secret-prompt-text") || strings.Contains(logLine, "broken") || strings.Contains(logLine, apiKey) {
		t.Fatalf("log leaked prompt/args/api key: %s", logLine)
	}
}

func fieldMap(t *testing.T, fields []any) map[string]any {
	t.Helper()
	if len(fields)%2 != 0 {
		t.Fatalf("odd fields: %#v", fields)
	}
	out := make(map[string]any, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok {
			t.Fatalf("field key = %#v", fields[i])
		}
		out[key] = fields[i+1]
	}
	return out
}
