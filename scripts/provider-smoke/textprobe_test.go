package main

import (
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
)

func TestTextProbeInputsAndResponseChecks(t *testing.T) {
	for _, mode := range []string{"text", "text-stream", "vision", "vision-stream", "tools", "schema"} {
		req := textProbeRequest("model", mode, []byte("png"))
		parts := req.GetInput().GetItems()[0].GetMessage().GetParts()
		wantParts := 1
		if mode == "vision" || mode == "vision-stream" {
			wantParts = 2
		}
		if len(parts) != wantParts || (wantParts == 1 && parts[0].GetImage() != nil) {
			t.Fatalf("%s: unexpected image input", mode)
		}
		if req.GetOutput().GetStream() != (mode == "text-stream" || mode == "vision-stream") {
			t.Fatalf("%s: stream flag", mode)
		}
	}
	tool := &modelhubv2.ToolCall{Id: "call-1", Name: "record_result", ArgumentsJson: []byte(`{"code":"OK"}`)}
	for _, tc := range []struct {
		mode, text string
		tools      []*modelhubv2.ToolCall
		valid      bool
	}{
		{"text", "OK", nil, true}, {"text-stream", " OK\n", nil, true}, {"text", "wrong", nil, false},
		{"schema", `{"code":"OK"}`, nil, true}, {"schema", `{"code":"bad"}`, nil, false}, {"schema", `{"code":"OK","extra":1}`, nil, false},
		{"tools", "", []*modelhubv2.ToolCall{tool}, true}, {"tools", "", nil, false},
	} {
		if err := validateTextProbe(tc.mode, tc.text, tc.tools); (err == nil) != tc.valid {
			t.Fatalf("%s: err=%v", tc.mode, err)
		}
	}
}
