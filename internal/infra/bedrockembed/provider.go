// Package bedrockembed 把 Bedrock Cohere Embed v4 收成 1024 维文本结果。
// role_arn 为空时用任务角色；有值时假定该角色，向量请求落到角色所在账号。
package bedrockembed

import (
	"context"
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

const vectorDim = provider.EmbeddingDimension

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
	input, err := provider.ParseEmbeddingInput(request.GetInput())
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"input_type": input.SearchType(), "embedding_types": []string{"float"}, "output_dimension": vectorDim,
	}
	expected := len(input.Texts)
	if input.ImageDataURI != "" {
		payload["images"] = []string{input.ImageDataURI}
		expected = 1
	} else {
		payload["texts"] = input.Texts
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
	vectors, err := provider.DecodeFloatEmbeddings(output.Body, expected)
	if err != nil {
		return nil, err
	}
	return provider.EmbeddingEvent(vectors)
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
