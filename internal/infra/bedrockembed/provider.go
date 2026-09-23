// Package bedrockembed 把 Bedrock Cohere Embed v4 收成 1024 维文本结果。
// role_arn 为空时用任务角色；有值时假定该角色，向量请求落到角色所在账号。
package bedrockembed

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

const vectorDim = 1024

type invokeAPI interface {
	InvokeModel(ctx context.Context, params *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error)
}

type Provider struct {
	name   string
	client invokeAPI
}

func New(ctx context.Context, name, region, roleARN string) (*Provider, error) {
	name = strings.TrimSpace(name)
	region = strings.TrimSpace(region)
	roleARN = strings.TrimSpace(roleARN)
	if name == "" || region == "" || strings.HasPrefix(strings.ToLower(region), "cn-") {
		return nil, provider.New(provider.ErrorConfiguration, "bedrock embedding region is required and cannot be cn-*")
	}
	if roleARN != "" && !strings.HasPrefix(roleARN, "arn:aws:iam::") {
		return nil, provider.New(provider.ErrorConfiguration, "bedrock embedding role_arn must be an IAM role ARN")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	httpClient := telemetry.NewHTTPClient()
	httpClient.Timeout = 20 * time.Second
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region), awsconfig.WithHTTPClient(httpClient))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorConfiguration, "load bedrock embedding config", err)
	}
	if roleARN != "" {
		cfg.Credentials = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), roleARN))
	}
	return &Provider{name: name, client: bedrockruntime.NewFromConfig(cfg)}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.CohereEmbedV4Bedrock {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "bedrock embedding does not serve model %q", model)
	}
	payload, err := bedrockPayload(request)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" encode embedding", err)
	}
	output, err := p.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId: aws.String(model), ContentType: aws.String("application/json"), Accept: aws.String("application/json"), Body: body,
	})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" embedding", err)
	}
	if output == nil || len(output.Body) == 0 {
		return nil, provider.New(provider.ErrorInvalidResponse, "bedrock embedding response is empty")
	}
	vector, err := decodeFloatEmbedding(output.Body)
	if err != nil {
		return nil, err
	}
	if len(vector) != vectorDim {
		return nil, provider.Errorf(provider.ErrorInvalidResponse, "bedrock embedding dimension is %d, want %d", len(vector), vectorDim)
	}
	encoded, err := json.Marshal(map[string]any{"embedding": vector})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" encode embedding", err)
	}
	return provider.TextFinalEvent(string(encoded), nil, "", "", nil), nil
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

func bedrockPayload(request *modelhubv2.GenerateRequest) (map[string]any, error) {
	var input *modelhubv2.Input
	if request != nil {
		input = request.GetInput()
	}
	if image := provider.FirstImageMedia(input); image != nil {
		data, ok := provider.InlineImageBytes(image)
		if !ok {
			return nil, provider.New(provider.ErrorInvalidArgument, "embedding image must be inline bytes")
		}
		mime := image.GetMimeType()
		if mime == "" {
			mime = "image/jpeg"
		}
		return map[string]any{
			"input_type": "search_document",
			"images":     []string{"data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)},
			"embedding_types": []string{"float"}, "output_dimension": vectorDim,
		}, nil
	}
	text := strings.TrimSpace(provider.UserText(input))
	if text == "" {
		return nil, provider.New(provider.ErrorInvalidArgument, "embedding requires an image or text")
	}
	inputType := "search_query"
	if strings.HasPrefix(text, "{") {
		inputType = "search_document"
	}
	return map[string]any{
		"input_type": inputType, "texts": []string{text},
		"embedding_types": []string{"float"}, "output_dimension": vectorDim,
	}, nil
}

func decodeFloatEmbedding(raw []byte) ([]float64, error) {
	var decoded struct {
		Embeddings json.RawMessage `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, "decode bedrock embedding", err)
	}
	if bytes.HasPrefix(bytes.TrimSpace(decoded.Embeddings), []byte("[")) {
		var rows [][]float64
		if err := json.Unmarshal(decoded.Embeddings, &rows); err != nil || len(rows) == 0 {
			return nil, provider.New(provider.ErrorInvalidResponse, "bedrock embedding response is empty")
		}
		return rows[0], nil
	}
	var byType struct {
		Float [][]float64 `json:"float"`
	}
	if err := json.Unmarshal(decoded.Embeddings, &byType); err != nil || len(byType.Float) == 0 {
		return nil, provider.New(provider.ErrorInvalidResponse, "bedrock embedding response is empty")
	}
	return byType.Float[0], nil
}
