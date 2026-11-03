package provider

import (
	"encoding/json"
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
)

// EmbeddingBatchTexts 统一批量查询的准入规则，普通单条调用返回 nil。
func EmbeddingBatchTexts(input *modelhubv2.Input, maxTexts, maxTextBytes int) ([]string, error) {
	var traits struct {
		Batch     bool   `json:"embedding_batch"`
		InputType string `json:"input_type"`
	}
	if raw := strings.TrimSpace(input.GetModelTraits()); raw != "" {
		if err := json.Unmarshal([]byte(raw), &traits); err != nil || raw == "null" {
			return nil, NotAttempted(ErrorInvalidArgument, "invalid embedding model_traits")
		}
	}
	if !traits.Batch {
		return nil, nil
	}
	if traits.InputType != "search_query" {
		return nil, NotAttempted(ErrorInvalidArgument, "batch embedding requires input_type search_query")
	}
	if len(input.GetItems()) == 0 || len(input.GetItems()) > maxTexts {
		return nil, NotAttempted(ErrorInvalidArgument, "batch embedding exceeds request text count budget")
	}
	texts := make([]string, 0, len(input.GetItems()))
	textBytes := 0
	for _, item := range input.GetItems() {
		message := item.GetMessage()
		if message.GetRole() != modelhubv2.Role_ROLE_USER || len(message.GetParts()) != 1 || len(message.GetToolCalls()) != 0 {
			return nil, NotAttempted(ErrorInvalidArgument, "batch embedding requires one user text per item")
		}
		raw := message.GetParts()[0].GetText()
		text := strings.TrimSpace(raw)
		if text == "" {
			return nil, NotAttempted(ErrorInvalidArgument, "batch embedding requires non-empty text parts")
		}
		textBytes += len(raw)
		if textBytes > maxTextBytes {
			return nil, NotAttempted(ErrorInvalidArgument, "batch embedding exceeds request text byte budget")
		}
		texts = append(texts, text)
	}
	return texts, nil
}
