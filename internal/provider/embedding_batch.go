package provider

import (
	"encoding/json"
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
)

// EmbeddingBatchTexts 解析现有批量查询协议。普通单条返回 nil，预算为零表示沿用 RPC 整体限制。
func EmbeddingBatchTexts(input *modelhubv2.Input, maxTexts, maxTextBytes int) ([]string, error) {
	rawTraits := strings.TrimSpace(input.GetModelTraits())
	if rawTraits == "" {
		return nil, nil
	}
	var traits struct {
		Batch     bool   `json:"embedding_batch"`
		InputType string `json:"input_type"`
	}
	if err := json.Unmarshal([]byte(rawTraits), &traits); err != nil || rawTraits == "null" {
		return nil, NotAttempted(ErrorInvalidArgument, "invalid embedding model_traits")
	}
	if !traits.Batch {
		return nil, nil
	}
	if traits.InputType != "search_query" {
		return nil, NotAttempted(ErrorInvalidArgument, "batch embedding requires input_type search_query")
	}
	items := input.GetItems()
	if len(items) == 0 || (maxTexts > 0 && len(items) > maxTexts) {
		return nil, NotAttempted(ErrorInvalidArgument, "batch embedding exceeds request text count budget")
	}
	texts := make([]string, len(items))
	textBytes := 0
	for i, item := range items {
		message := item.GetMessage()
		parts := message.GetParts()
		if message.GetRole() != modelhubv2.Role_ROLE_USER || len(parts) != 1 || len(message.GetToolCalls()) != 0 {
			return nil, NotAttempted(ErrorInvalidArgument, "batch embedding requires one user text per item")
		}
		raw := parts[0].GetText()
		text := strings.TrimSpace(raw)
		if text == "" {
			return nil, NotAttempted(ErrorInvalidArgument, "batch embedding requires non-empty text parts")
		}
		textBytes += len(raw)
		if maxTextBytes > 0 && textBytes > maxTextBytes {
			return nil, NotAttempted(ErrorInvalidArgument, "batch embedding exceeds request text byte budget")
		}
		texts[i] = text
	}
	return texts, nil
}
