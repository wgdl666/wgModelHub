// Package rerankbatch shares rerank input limits, batching and candidate index mapping.
package rerankbatch

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"go.opentelemetry.io/otel/attribute"
)

// MaxDocuments is ModelHub's limit for one logical rerank request, before provider batching.
const MaxDocuments = 1000

type Request struct {
	Query          string   `json:"query"`
	InstructionKey string   `json:"instruction_key"`
	Instruct       string   `json:"instruct"`
	Documents      []string `json:"documents"`
}

type Result struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

func Parse(request *modelhubv2.GenerateRequest) (*Request, error) {
	var input *modelhubv2.Input
	if request != nil {
		input = request.GetInput()
	}
	var parsed Request
	if err := json.Unmarshal([]byte(provider.UserText(input)), &parsed); err != nil {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "rerank requires JSON query and documents")
	}
	if strings.TrimSpace(parsed.Query) == "" || len(parsed.Documents) == 0 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "rerank requires a query and documents")
	}
	if len(parsed.Documents) > MaxDocuments {
		return nil, provider.NotAttemptedf(provider.ErrorInvalidArgument, "rerank accepts at most %d documents per request (got %d)", MaxDocuments, len(parsed.Documents))
	}
	for _, document := range parsed.Documents {
		if strings.TrimSpace(document) == "" {
			return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "rerank document text is required")
		}
	}
	return &parsed, nil
}

// Run receives documents validated by Parse. It keeps batch order because scores
// from separate provider requests are not guaranteed to be comparable.
// Every batch must finish successfully before any result is returned to the caller.
func Run(ctx context.Context, documents []string, batchSize int, callOnce func(context.Context, []string) ([]Result, error)) ([]Result, error) {
	results := make([]Result, 0, len(documents))
	for start := 0; start < len(documents); start += batchSize {
		if err := ctx.Err(); err != nil {
			if start == 0 {
				return nil, provider.WrapNotAttempted(provider.ErrorTimeout, "rerank canceled before provider call", err)
			}
			return nil, err
		}
		end := min(start+batchSize, len(documents))
		batchCtx, span := telemetry.StartSpan(ctx, "modelhub.rerank_batch")
		span.SetAttributes(attribute.Int("rerank.batch_start", start), attribute.Int("rerank.batch_size", end-start))
		batch, err := callOnce(batchCtx, documents[start:end])
		if err == nil {
			err = validateResults(batch, end-start)
		}
		if err != nil {
			telemetry.RecordError(batchCtx, err)
			span.End()
			return nil, err
		}
		span.End()
		sort.SliceStable(batch, func(i, j int) bool { return batch[i].RelevanceScore > batch[j].RelevanceScore })
		for index := range batch {
			batch[index].Index += start
		}
		results = append(results, batch...)
	}
	return results, nil
}

func validateResults(results []Result, count int) error {
	if len(results) != count {
		return provider.Errorf(provider.ErrorInvalidResponse, "rerank returned %d results, want %d", len(results), count)
	}
	seen := make([]bool, count)
	for _, result := range results {
		if result.Index < 0 || result.Index >= count || seen[result.Index] {
			return provider.New(provider.ErrorInvalidResponse, "rerank returned invalid or duplicate document indexes")
		}
		if math.IsNaN(result.RelevanceScore) || math.IsInf(result.RelevanceScore, 0) {
			return provider.New(provider.ErrorInvalidResponse, "rerank returned a non-finite relevance score")
		}
		seen[result.Index] = true
	}
	return nil
}
