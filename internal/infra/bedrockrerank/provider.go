// Package bedrockrerank 把 Bedrock Cohere Rerank 3.5 收成文本结果。
// 该模型没有 instruct，请求里的 instruct 不拼进 query。
package bedrockrerank

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

type rerankAPI interface {
	Rerank(ctx context.Context, params *bedrockagentruntime.RerankInput, optFns ...func(*bedrockagentruntime.Options)) (*bedrockagentruntime.RerankOutput, error)
}

type Provider struct {
	name   string
	region string
	client rerankAPI
}

func New(ctx context.Context, name, region, roleARN string) (*Provider, error) {
	name = strings.TrimSpace(name)
	region = strings.TrimSpace(region)
	roleARN = strings.TrimSpace(roleARN)
	if name == "" || region == "" || strings.HasPrefix(strings.ToLower(region), "cn-") {
		return nil, provider.New(provider.ErrorConfiguration, "bedrock rerank region is required and cannot be cn-*")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	httpClient := telemetry.NewHTTPClient()
	httpClient.Timeout = 20 * time.Second
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region), awsconfig.WithHTTPClient(httpClient))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorConfiguration, "load bedrock rerank config", err)
	}
	if roleARN != "" {
		cfg.Credentials = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), roleARN))
	}
	return &Provider{name: name, region: region, client: bedrockagentruntime.NewFromConfig(cfg)}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.CohereRerankV35 {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "bedrock rerank does not serve model %q", model)
	}
	var parsed struct {
		Query     string   `json:"query"`
		Documents []string `json:"documents"`
	}
	if err := json.Unmarshal([]byte(provider.UserText(requestInput(request))), &parsed); err != nil || strings.TrimSpace(parsed.Query) == "" || len(parsed.Documents) == 0 || len(parsed.Documents) > 100 {
		return nil, provider.New(provider.ErrorInvalidArgument, "rerank requires a query and 1 to 100 documents")
	}
	sources := make([]types.RerankSource, 0, len(parsed.Documents))
	for _, document := range parsed.Documents {
		if strings.TrimSpace(document) == "" {
			return nil, provider.New(provider.ErrorInvalidArgument, "rerank document text is required")
		}
		text := document
		sources = append(sources, types.RerankSource{
			Type: types.RerankSourceTypeInline,
			InlineDocumentSource: &types.RerankDocument{
				Type: types.RerankDocumentTypeText,
				TextDocument: &types.RerankTextDocument{Text: &text},
			},
		})
	}
	query := parsed.Query
	output, err := p.client.Rerank(ctx, &bedrockagentruntime.RerankInput{
		Queries: []types.RerankQuery{{Type: types.RerankQueryContentTypeText, TextQuery: &types.RerankTextDocument{Text: &query}}},
		Sources: sources,
		RerankingConfiguration: &types.RerankingConfiguration{
			Type: types.RerankingConfigurationTypeBedrockRerankingModel,
			BedrockRerankingConfiguration: &types.BedrockRerankingConfiguration{
				ModelConfiguration: &types.BedrockRerankingModelConfiguration{ModelArn: aws.String(fmt.Sprintf("arn:aws:bedrock:%s::foundation-model/%s", p.region, model))},
				NumberOfResults:    aws.Int32(int32(len(parsed.Documents))),
			},
		},
	})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" rerank", err)
	}
	results := make([]map[string]any, 0)
	if output != nil {
		for _, item := range output.Results {
			score := 0.0
			if item.RelevanceScore != nil {
				score = float64(*item.RelevanceScore)
			}
			results = append(results, map[string]any{"index": int(aws.ToInt32(item.Index)), "relevance_score": score})
		}
	}
	body, err := json.Marshal(map[string]any{"results": results})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" encode rerank", err)
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

func requestInput(request *modelhubv2.GenerateRequest) *modelhubv2.Input {
	if request == nil {
		return nil
	}
	return request.GetInput()
}
