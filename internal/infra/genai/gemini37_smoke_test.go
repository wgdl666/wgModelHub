package genai

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

// TestGemini37FlashLowHubToolsSmoke 用修改后的 provider + Hub 实际工具 schema 验证
// gemini-3.7-flash + DISABLED→LOW 能返回合法工具调用。输出仅含状态/模型/延迟/工具名/错误类别。
func TestGemini37FlashLowHubToolsSmoke(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY unset; skip live smoke")
	}

	tools, err := loadHubIntentToolSchemas(t)
	if err != nil {
		t.Fatalf("load hub tools: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	p, err := NewGemini(ctx, "smoke_gemini", apiKey, "", "")
	if err != nil {
		t.Fatalf("create provider: category=%s", classifySmokeErr(err))
	}

	req := &modelhubv2.GenerateRequest{
		Model: models.Gemini37Flash,
		Input: &modelhubv2.Input{
			Tools: tools,
			ToolChoice: &modelhubv2.ToolChoice{
				Mode: modelhubv2.ToolChoiceMode_TOOL_CHOICE_MODE_REQUIRED,
			},
			Items: []*modelhubv2.InputItem{
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role: modelhubv2.Role_ROLE_SYSTEM,
					Parts: []*modelhubv2.ContentPart{{
						Content: &modelhubv2.ContentPart_Text{Text: "你是意图路由助手，必须调用一个工具。"},
					}},
				}}},
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role: modelhubv2.Role_ROLE_USER,
					Parts: []*modelhubv2.ContentPart{{
						Content: &modelhubv2.ContentPart_Text{Text: "帮我看看衣橱里有哪些红色的衣服"},
					}},
				}}},
			},
		},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{
			Thinking: modelhubv2.ThinkingMode_THINKING_MODE_DISABLED,
		}}},
	}

	start := time.Now()
	event, err := p.Generate(ctx, models.Gemini37Flash, req)
	latency := time.Since(start)
	if err != nil {
		t.Fatalf("smoke failed: model=%s latency_ms=%d category=%s", models.Gemini37Flash, latency.Milliseconds(), classifySmokeErr(err))
	}
	toolName := ""
	if event != nil {
		for _, item := range event.GetItems() {
			if call := item.GetToolCall(); call != nil && call.Name != "" {
				toolName = call.Name
				break
			}
		}
	}
	if toolName == "" {
		t.Fatalf("smoke failed: model=%s latency_ms=%d category=no_tool_call", models.Gemini37Flash, latency.Milliseconds())
	}
	t.Logf("smoke ok: model=%s latency_ms=%d tool=%s status=ok", models.Gemini37Flash, latency.Milliseconds(), toolName)
}

func loadHubIntentToolSchemas(t *testing.T) ([]*modelhubv2.Tool, error) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// worktree/.../wgModelHub/internal/infra/genai -> sibling wgHub prompts tools
	toolsDir := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "../../../../wgHub/internal/prompts/shared/tools"))
	entries, err := os.ReadDir(toolsDir)
	if err != nil {
		return nil, err
	}
	var tools []*modelhubv2.Tool
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(toolsDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var envelope struct {
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, err
		}
		if envelope.Function.Name == "" || len(envelope.Function.Parameters) == 0 {
			continue
		}
		tools = append(tools, &modelhubv2.Tool{
			Function: &modelhubv2.FunctionDefinition{
				Name:                 envelope.Function.Name,
				Description:          envelope.Function.Description,
				ParametersJsonSchema: append([]byte(nil), envelope.Function.Parameters...),
			},
		})
	}
	if len(tools) == 0 {
		t.Fatal("no hub tools loaded")
	}
	return tools, nil
}

func classifySmokeErr(err error) string {
	if err == nil {
		return "none"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "invalid_argument"), strings.Contains(msg, "400"):
		return "invalid_argument"
	case strings.Contains(msg, "permission"), strings.Contains(msg, "401"), strings.Contains(msg, "403"):
		return "auth"
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline"):
		return "timeout"
	case strings.Contains(msg, "unavailable"), strings.Contains(msg, "503"):
		return "unavailable"
	default:
		return "other"
	}
}
