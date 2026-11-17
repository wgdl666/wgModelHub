package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
)

const EmbeddingDimension = 1024

type EmbeddingInput struct {
	Texts          []string
	ImageDataURI   string
	SearchDocument bool
}

// ParseEmbeddingInput 统一检查向量输入。调用方决定批次大小，适配器只组装这一批。
func ParseEmbeddingInput(input *modelhubv2.Input) (EmbeddingInput, error) {
	value := input.GetEmbedding()
	if value == nil || len(input.GetItems()) != 0 {
		return EmbeddingInput{}, NotAttempted(ErrorInvalidArgument, "embedding requires input.embedding without input.items")
	}
	texts := value.GetTexts()
	if image := value.GetImage(); image != nil {
		if _, inline := image.GetSource().(*modelhubv2.Media_Data); !inline || len(texts) != 0 {
			return EmbeddingInput{}, NotAttempted(ErrorInvalidArgument, "embedding image must be inline and supplied without texts")
		}
		uri := "data:" + strings.TrimSpace(image.GetMimeType()) + ";base64," + base64.StdEncoding.EncodeToString(image.GetData())
		return EmbeddingInput{ImageDataURI: uri, SearchDocument: true}, nil
	}
	if len(texts) == 0 {
		return EmbeddingInput{}, NotAttempted(ErrorInvalidArgument, "embedding requires texts or an inline image")
	}
	normalized := make([]string, len(texts))
	for i, text := range texts {
		normalized[i] = strings.TrimSpace(text)
		if normalized[i] == "" {
			return EmbeddingInput{}, NotAttempted(ErrorInvalidArgument, "embedding requires non-empty texts")
		}
	}
	return EmbeddingInput{Texts: normalized, SearchDocument: value.GetSearchDocument()}, nil
}

// SearchType 将查询和建索引映射为 Cohere 系列原生 input_type。
func (input EmbeddingInput) SearchType() string {
	if input.SearchDocument {
		return "search_document"
	}
	return "search_query"
}

// DecodeFloatEmbeddings 解析 Cohere 官方与 Bedrock 的 float 回包，核对输入与向量一一对应。
func DecodeFloatEmbeddings(raw []byte, expected int) ([][]float64, error) {
	var decoded struct {
		Embeddings json.RawMessage `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, Wrap(ErrorInvalidResponse, "decode embedding response", err)
	}
	rowsJSON := bytes.TrimSpace(decoded.Embeddings)
	if len(rowsJSON) == 0 {
		return nil, New(ErrorInvalidResponse, "embedding response is empty")
	}
	if rowsJSON[0] == '{' {
		var byType struct {
			Float json.RawMessage `json:"float"`
		}
		if err := json.Unmarshal(rowsJSON, &byType); err != nil {
			return nil, Wrap(ErrorInvalidResponse, "decode float embeddings", err)
		}
		rowsJSON = byType.Float
	}
	// encoding/json 会把向量中的 null 数值静默解码成 0。
	if bytes.Contains(rowsJSON, []byte("null")) {
		return nil, New(ErrorInvalidResponse, "embedding response contains null values")
	}
	var vectors [][]float64
	if err := json.Unmarshal(rowsJSON, &vectors); err != nil {
		return nil, Wrap(ErrorInvalidResponse, "decode float embeddings", err)
	}
	if len(vectors) != expected {
		return nil, Errorf(ErrorInvalidResponse, "embedding count is %d, want %d", len(vectors), expected)
	}
	for _, vector := range vectors {
		if len(vector) != EmbeddingDimension {
			return nil, Errorf(ErrorInvalidResponse, "embedding dimension is %d, want %d", len(vector), EmbeddingDimension)
		}
	}
	return vectors, nil
}

// EmbeddingEvent 统一单条、批量和图片的回包结构。
func EmbeddingEvent(vectors [][]float64) (*modelhubv2.GenerateEvent, error) {
	body, err := json.Marshal(struct {
		Embeddings [][]float64 `json:"embeddings"`
	}{Embeddings: vectors})
	if err != nil {
		return nil, Wrap(ErrorInvalidResponse, "encode embeddings", err)
	}
	return TextFinalEvent(string(body), nil, "", "", nil), nil
}
