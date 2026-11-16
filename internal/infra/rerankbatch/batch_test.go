package rerankbatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

func inputRequest(text string) *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: text}}}}}}}}}
}

func TestParseCapsLogicalInputWithoutChangingText(t *testing.T) {
	documents := make([]string, MaxDocuments+1)
	for index := range documents {
		documents[index] = " document "
	}
	documents[0] = strings.Repeat("long input", 4000)
	for _, count := range []int{MaxDocuments, MaxDocuments + 1} {
		raw, err := json.Marshal(Request{Query: "query", InstructionKey: "look", Documents: documents[:count]})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := Parse(inputRequest(string(raw)))
		if count > MaxDocuments {
			if provider.Kind(err) != provider.ErrorInvalidArgument || !provider.IsNotAttempted(err) || !strings.Contains(err.Error(), "1001") {
				t.Fatalf("oversized input: %v", err)
			}
			continue
		}
		if err != nil || parsed.Query != "query" || parsed.InstructionKey != "look" || !reflect.DeepEqual(parsed.Documents, documents[:count]) {
			t.Fatalf("input changed: parsed=%v err=%v", parsed, err)
		}
	}
	for _, raw := range []string{"", `{}`, `{"query":"q","documents":[]}`, `{"query":"q","documents":[" "]}`} {
		if _, err := Parse(inputRequest(raw)); provider.Kind(err) != provider.ErrorInvalidArgument || !provider.IsNotAttempted(err) {
			t.Fatalf("input=%s err=%v", raw, err)
		}
	}
}

func TestRunPreservesBatchOrderAndOriginalIndexes(t *testing.T) {
	documents := make([]string, 1000)
	for index := range documents {
		documents[index] = fmt.Sprint(index)
	}
	var sizes []int
	results, err := Run(context.Background(), documents, 500, func(_ context.Context, batch []string) ([]Result, error) {
		sizes = append(sizes, len(batch))
		out := make([]Result, len(batch))
		for index := range batch {
			// The second batch deliberately has higher scores. It must not move ahead of the first.
			out[index] = Result{Index: index, RelevanceScore: float64(index) + float64(len(sizes))*1000}
		}
		return out, nil
	})
	if err != nil || !reflect.DeepEqual(sizes, []int{500, 500}) || len(results) != len(documents) {
		t.Fatalf("sizes=%v results=%d err=%v", sizes, len(results), err)
	}
	for position, result := range results {
		want := (position/500)*500 + 499 - position%500
		if result.Index != want {
			t.Fatalf("position=%d index=%d want=%d", position, result.Index, want)
		}
	}
}

func TestRunReturnsNoPartialResultsOnInvalidBatchOrFailure(t *testing.T) {
	failure := provider.New(provider.ErrorUnavailable, "upstream failed")
	for _, test := range []struct {
		name    string
		results []Result
		err     error
	}{
		{name: "upstream", err: failure},
		{name: "incomplete", results: []Result{{Index: 0, RelevanceScore: 1}}},
		{name: "duplicate", results: []Result{{Index: 0}, {Index: 0}}},
		{name: "out of bounds", results: []Result{{Index: 0}, {Index: 2}}},
		{name: "non-finite", results: []Result{{Index: 0}, {Index: 1, RelevanceScore: math.NaN()}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			results, err := Run(context.Background(), []string{"a", "b", "c", "d", "e"}, 2, func(context.Context, []string) ([]Result, error) {
				calls++
				if calls == 1 {
					return []Result{{Index: 0}, {Index: 1}}, nil
				}
				return test.results, test.err
			})
			if err == nil || results != nil || calls != 2 {
				t.Fatalf("calls=%d results=%v err=%v", calls, results, err)
			}
			if test.err != nil && !errors.Is(err, failure) {
				t.Fatalf("lost upstream error: %v", err)
			}
		})
	}
}

func TestRunStopsBeforeStartingAnotherBatchOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	results, err := Run(ctx, []string{"a", "b"}, 1, func(context.Context, []string) ([]Result, error) {
		calls++
		cancel()
		return []Result{{Index: 0}}, nil
	})
	if !errors.Is(err, context.Canceled) || results != nil || calls != 1 {
		t.Fatalf("calls=%d results=%v err=%v", calls, results, err)
	}
}
