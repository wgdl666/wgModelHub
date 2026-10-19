// Package cohereembed 把 Cohere 官方 embed-v4.0 收成 ModelHub 文本结果。
// query 和以 { 开头的文档文本使用不同 input_type，混用会拉低召回。
package cohereembed

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

const (
	vectorDim        = 1024
	maxBatchTexts    = 96
	maxResponseBytes = 4 << 20
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Provider struct {
	name     string
	client   httpDoer
	endpoint string
	apiKey   string
}

func New(name, baseURL, apiKey string) (*Provider, error) {
	name = strings.TrimSpace(name)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	apiKey = strings.TrimSpace(apiKey)
	if name == "" || baseURL == "" || apiKey == "" {
		return nil, provider.New(provider.ErrorConfiguration, "cohere embedding requires base_url and api_key")
	}
	// 与精排同路：官网 embed 也要 provider.http，才能区分「向量慢」和「精排慢」。
	return &Provider{name: name, client: telemetry.NewTimedHTTPClient(20 * time.Second), endpoint: baseURL + "/v2/embed", apiKey: apiKey}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.CohereEmbedV4 {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "cohere embedding does not serve model %q", model)
	}
	payload, batch, err := coherePayload(model, request)
	if err != nil {
		return nil, err
	}
	expected := 1
	if batch {
		expected = len(payload["texts"].([]string))
	}
	vectors, err := p.post(ctx, payload, expected)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"embeddings": vectors}
	if !batch {
		result = map[string]any{"embedding": vectors[0]}
	}
	body, err := json.Marshal(result)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" encode embedding", err)
	}
	return provider.TextFinalEvent(string(body), nil, "", "", nil), nil
}

func (p *Provider) GenerateStream(ctx context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	event, err := p.Generate(ctx, model, request)
	if err != nil {
		return nil, err
	}
	text := event.GetItems()[0].GetText()
	if emit != nil && text != "" {
		if err := emit(provider.TextDeltaEvent(text)); err != nil {
			return nil, err
		}
	}
	return provider.MetadataFinalEvent("", "", nil), nil
}

func coherePayload(model string, request *modelhubv2.GenerateRequest) (map[string]any, bool, error) {
	var input *modelhubv2.Input
	if request != nil {
		input = request.GetInput()
	}
	base := map[string]any{"model": model, "embedding_types": []string{"float"}, "output_dimension": vectorDim}
	// 显式标记才按条目拆批；普通多段文本仍按原协议合成一个向量。
	var traits struct {
		Batch     bool   `json:"embedding_batch"`
		InputType string `json:"input_type"`
	}
	if raw := strings.TrimSpace(input.GetModelTraits()); raw != "" {
		if err := json.Unmarshal([]byte(raw), &traits); err != nil || raw == "null" {
			return nil, false, provider.New(provider.ErrorInvalidArgument, "invalid embedding model_traits")
		}
	}
	if traits.Batch {
		if traits.InputType != "search_query" {
			return nil, false, provider.New(provider.ErrorInvalidArgument, "batch embedding requires input_type search_query")
		}
		if len(input.GetItems()) == 0 || len(input.GetItems()) > maxBatchTexts {
			return nil, false, provider.New(provider.ErrorInvalidArgument, "batch embedding requires 1..96 text items")
		}
		texts := make([]string, 0, len(input.GetItems()))
		for _, item := range input.GetItems() {
			message := item.GetMessage()
			if message.GetRole() != modelhubv2.Role_ROLE_USER || len(message.GetParts()) != 1 || len(message.GetToolCalls()) != 0 {
				return nil, false, provider.New(provider.ErrorInvalidArgument, "batch embedding requires one user text per item")
			}
			text := strings.TrimSpace(message.GetParts()[0].GetText())
			if text == "" {
				return nil, false, provider.New(provider.ErrorInvalidArgument, "batch embedding requires non-empty text parts")
			}
			texts = append(texts, text)
		}
		base["input_type"] = "search_query"
		base["texts"] = texts
		return base, true, nil
	}
	if image := provider.FirstImageMedia(input); image != nil {
		data, ok := provider.InlineImageBytes(image)
		if !ok {
			return nil, false, provider.New(provider.ErrorInvalidArgument, "embedding image must be inline bytes")
		}
		mime := image.GetMimeType()
		if mime == "" {
			mime = "image/jpeg"
		}
		base["input_type"] = "search_document"
		base["images"] = []string{"data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)}
		return base, false, nil
	}
	text := strings.TrimSpace(provider.UserText(input))
	if text == "" {
		return nil, false, provider.New(provider.ErrorInvalidArgument, "embedding requires an image or text")
	}
	inputType := "search_query"
	if strings.HasPrefix(text, "{") {
		inputType = "search_document"
	}
	base["input_type"] = inputType
	base["texts"] = []string{text}
	return base, false, nil
}

func (p *Provider) post(ctx context.Context, payload any, expected int) ([][]float64, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" encode embedding", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" create embedding request", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" embedding", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" read embedding", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, provider.Errorf(provider.ErrorUnavailable, "%s embedding HTTP %d", p.name, resp.StatusCode)
	}
	// 批量响应可超过原先的 1 MiB；仍保留总大小上限，禁止截断后误用首行。
	if len(raw) > maxResponseBytes {
		return nil, provider.New(provider.ErrorInvalidResponse, "cohere embedding response exceeds size limit")
	}
	vectors, err := decodeFloatEmbeddings(raw)
	if err != nil {
		return nil, err
	}
	if len(vectors) != expected {
		return nil, provider.Errorf(provider.ErrorInvalidResponse, "cohere embedding count is %d, want %d", len(vectors), expected)
	}
	for _, vector := range vectors {
		if len(vector) != vectorDim {
			return nil, provider.Errorf(provider.ErrorInvalidResponse, "cohere embedding dimension is %d, want %d", len(vector), vectorDim)
		}
		for _, value := range vector {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, provider.New(provider.ErrorInvalidResponse, "cohere embedding contains non-finite values")
			}
		}
	}
	return vectors, nil
}

func decodeFloatEmbeddings(raw []byte) ([][]float64, error) {
	var decoded struct {
		Embeddings json.RawMessage `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, "decode cohere embedding", err)
	}
	if len(decoded.Embeddings) == 0 {
		return nil, provider.New(provider.ErrorInvalidResponse, "cohere embedding response is empty")
	}
	rowsJSON := decoded.Embeddings
	if !bytes.HasPrefix(bytes.TrimSpace(rowsJSON), []byte("[")) {
		var byType struct {
			Float json.RawMessage `json:"float"`
		}
		if err := json.Unmarshal(rowsJSON, &byType); err != nil {
			return nil, provider.Wrap(provider.ErrorInvalidResponse, "decode cohere float embeddings", err)
		}
		rowsJSON = byType.Float
	}
	// encoding/json 会把 null 数值静默变成 0，不能把供应商缺失值当作有效向量。
	if bytes.Contains(rowsJSON, []byte("null")) {
		return nil, provider.New(provider.ErrorInvalidResponse, "cohere embedding contains null values")
	}
	var rows [][]float64
	if err := json.Unmarshal(rowsJSON, &rows); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, "decode cohere float embeddings", err)
	}
	if len(rows) == 0 {
		return nil, provider.New(provider.ErrorInvalidResponse, "cohere embedding response is empty")
	}
	return rows, nil
}
