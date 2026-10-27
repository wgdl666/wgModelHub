package openai

import (
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
	"testing"
)

// 锁定新 GPT 协议边界：普通与流式请求都不能泄漏千问思考字段或旧 token 字段。
func TestGPTRequestParameters(t *testing.T) {
	ids := []string{models.GPT56Sol, models.GPT56Luna, models.GPT56Terra, models.GPT6Astra, models.GPT6Sol, models.GPT6Luna, models.GPT61Sol}
	cases := []struct {
		name  string
		mode  modelhubv2.ThinkingMode
		level modelhubv2.ThinkingLevel
		want  string
	}{
		{"default", 0, 0, ""},
		{"enabled", modelhubv2.ThinkingMode_THINKING_MODE_ENABLED, 0, "medium"},
		{"disabled_wins", modelhubv2.ThinkingMode_THINKING_MODE_DISABLED, modelhubv2.ThinkingLevel_THINKING_LEVEL_HIGH, "low"},
		{"minimal", 0, modelhubv2.ThinkingLevel_THINKING_LEVEL_MINIMAL, "low"},
		{"low", 0, modelhubv2.ThinkingLevel_THINKING_LEVEL_LOW, "low"},
		{"medium", 0, modelhubv2.ThinkingLevel_THINKING_LEVEL_MEDIUM, "medium"},
		{"high", 0, modelhubv2.ThinkingLevel_THINKING_LEVEL_HIGH, "high"},
	}
	for _, id := range ids {
		for _, tc := range cases {
			t.Run(id+"/"+tc.name, func(t *testing.T) {
				req := systemUserRequest("You are concise.", "Reply OK.", nil)
				out := req.GetOutput().GetText()
				out.Thinking = tc.mode
				out.ThinkingLevel = tc.level
				limit := int32(256)
				out.MaxOutputTokens = &limit
				out.ThinkingBudget = &limit
				for _, stream := range []bool{false, true} {
					body := (&Provider{}).buildRequestBody(id, req, stream)
					if body["model"] != id || body["max_completion_tokens"] != limit {
						t.Fatalf("model/token mapping: %#v", body)
					}
					for _, key := range []string{"max_tokens", "enable_thinking", "thinking_budget"} {
						if _, ok := body[key]; ok {
							t.Fatalf("unexpected %s", key)
						}
					}
					got, ok := body["reasoning_effort"]
					if tc.want == "" {
						if ok {
							t.Fatalf("unexpected effort %v", got)
						}
					} else if got != tc.want {
						t.Fatalf("effort=%v want %s", got, tc.want)
					}
				}
			})
		}
	}
}
