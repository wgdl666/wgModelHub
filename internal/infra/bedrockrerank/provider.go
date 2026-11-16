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
	"github.com/wgdl666/wgModelHub/internal/infra/rerankbatch"
	"github.com/wgdl666/wgModelHub/internal/infra/rerankinstruction"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

type rerankAPI interface {
	Rerank(ctx context.Context, params *bedrockagentruntime.RerankInput, optFns ...func(*bedrockagentruntime.Options)) (*bedrockagentruntime.RerankOutput, error)
}

type Provider struct {
	// Instructions 由启动装配注入；连接不变，模板按请求读取。
	Instructions rerankinstruction.Resolver
	name         string
	region       string
	client       rerankAPI
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
	parsed, err := rerankbatch.Parse(request)
	if err != nil {
		return nil, err
	}
	query, _, err := p.Instructions.Resolve(parsed.Query, parsed.Instruct, parsed.InstructionKey, false)
	if err != nil {
		return nil, err
	}
	// Bedrock accepts 1000 sources; result pagination is handled within each provider call.
	results, err := rerankbatch.Run(ctx, parsed.Documents, 1000, func(batchCtx context.Context, documents []string) ([]rerankbatch.Result, error) {
		return p.rankOnce(batchCtx, model, query, documents)
	})
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{"results": results})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" encode rerank", err)
	}
	return provider.TextFinalEvent(string(body), nil, "", "", nil), nil
}

func (p *Provider) rankOnce(ctx context.Context, model, query string, documents []string) ([]rerankbatch.Result, error) {
	sources := make([]types.RerankSource, 0, len(documents))
	for _, document := range documents {
		text := document
		sources = append(sources, types.RerankSource{
			Type: types.RerankSourceTypeInline,
			InlineDocumentSource: &types.RerankDocument{
				Type:         types.RerankDocumentTypeText,
				TextDocument: &types.RerankTextDocument{Text: &text},
			},
		})
	}
	pages := bedrockagentruntime.NewRerankPaginator(p.client, &bedrockagentruntime.RerankInput{
		Queries: []types.RerankQuery{{Type: types.RerankQueryContentTypeText, TextQuery: &types.RerankTextDocument{Text: &query}}},
		Sources: sources,
		RerankingConfiguration: &types.RerankingConfiguration{
			Type: types.RerankingConfigurationTypeBedrockRerankingModel,
			BedrockRerankingConfiguration: &types.BedrockRerankingConfiguration{
				ModelConfiguration: &types.BedrockRerankingModelConfiguration{ModelArn: aws.String(fmt.Sprintf("arn:aws:bedrock:%s::foundation-model/%s", p.region, model))},
				NumberOfResults:    aws.Int32(int32(len(documents))),
			},
		},
	})
	results := make([]rerankbatch.Result, 0, len(documents))
	for pages.HasMorePages() {
		output, err := pages.NextPage(ctx)
		if err != nil {
			return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" rerank", err)
		}
		for _, item := range output.Results {
			score := 0.0
			if item.RelevanceScore != nil {
				score = float64(*item.RelevanceScore)
			}
			results = append(results, rerankbatch.Result{Index: int(aws.ToInt32(item.Index)), RelevanceScore: score})
		}
	}
	return results, nil
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
