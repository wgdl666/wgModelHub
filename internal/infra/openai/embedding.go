package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

// generateEmbedding 复用网关鉴权与请求账本，只转换向量协议，不承担业务重试。
func (p *Provider) generateEmbedding(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if p.gateway == nil {
		return nil, provider.New(provider.ErrorConfiguration, "Vmind embedding requires model_gateway")
	}
	input := request.GetInput()
	texts, err := provider.EmbeddingBatchTexts(input, 0, 0)
	if err != nil {
		return nil, err
	}
	// 当前调用链只提交单图或单文本，不隐式接受未接入的批量协议。
	if texts != nil {
		return nil, provider.New(provider.ErrorInvalidArgument, "Vmind embedding batch is not supported")
	}
	var inputs []any
	if image := provider.FirstImageMedia(input); image != nil {
		if _, ok := provider.InlineImageBytes(image); !ok {
			return nil, provider.New(provider.ErrorInvalidArgument, "embedding image must be inline bytes")
		}
		inputs = []any{map[string]any{"image": mediaURL(image)}}
	} else {
		text := strings.TrimSpace(provider.UserText(input))
		if text == "" {
			return nil, provider.New(provider.ErrorInvalidArgument, "embedding requires image or text")
		}
		inputs = []any{text}
	}
	response, err := p.gateway.Invoke(ctx, model, map[string]any{"inputs": inputs, "dim": 1024, "normalize": true}, 110*time.Second)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, provider.TakeHTTPError(p.name, response.StatusCode, response.Body)
	}
	var result struct {
		Dim        int         `json:"dim"`
		Embeddings [][]float64 `json:"embeddings"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&result); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, "decode Vmind embedding", err)
	}
	if result.Dim != 1024 || len(result.Embeddings) != len(inputs) {
		return nil, provider.New(provider.ErrorInvalidResponse, "invalid Vmind embedding count or dimension")
	}
	for _, vector := range result.Embeddings {
		if len(vector) != 1024 {
			return nil, provider.New(provider.ErrorInvalidResponse, "Vmind embedding must return 1024 floats")
		}
	}
	raw, err := json.Marshal(map[string]any{"embedding": result.Embeddings[0]})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, "encode Vmind embedding", err)
	}
	return provider.TextFinalEvent(string(raw), nil, "", "", nil), nil
}
