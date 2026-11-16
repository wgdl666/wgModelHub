package mixedbreadrerank

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/rerankbatch"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (r roundTrip) Do(req *http.Request) (*http.Response, error) { return r(req) }

func TestColorQueryIsAssembled(t *testing.T) {
	payload := capture(t, `{"query":"红色","documents":["a","b"],"instruct":"Given a color query, retrieve items whose main color is that color."}`)
	if payload["query"] != "检索主色是「红色」的单品" {
		t.Fatalf("query %v", payload["query"])
	}
	if _, ok := payload["instruct"]; ok {
		t.Fatalf("instruct must not be sent: %v", payload)
	}
	if payload["rewrite_query"] != false || payload["return_input"] != false {
		t.Fatalf("flags %v", payload)
	}
	if _, ok := payload["top_k"]; ok {
		t.Fatal("top_k must be omitted so every document is scored")
	}
	input, ok := payload["input"].([]any)
	if !ok || len(input) != 2 || input[0] != "a" || input[1] != "b" {
		t.Fatalf("input %v", payload["input"])
	}
	if payload["model"] != models.MixedbreadRerankV31Listwise {
		t.Fatalf("model %v", payload["model"])
	}
}

func TestInstructPresenceDoesNotChangeQuery(t *testing.T) {
	withInstruct := capture(t, `{"query":"红色","documents":["a"],"instruct":"Given a look query, retrieve items that satisfy every condition in the query."}`)
	withoutInstruct := capture(t, `{"query":"红色","documents":["a"]}`)
	if withInstruct["query"] != "检索主色是「红色」的单品" || withoutInstruct["query"] != withInstruct["query"] {
		t.Fatalf("with %v without %v", withInstruct["query"], withoutInstruct["query"])
	}
	if _, ok := withInstruct["instruct"]; ok {
		t.Fatalf("instruct must not be sent: %v", withInstruct)
	}
}

func TestScoresMapFromVendorData(t *testing.T) {
	p := client(t, func(r *http.Request) (*http.Response, error) {
		body := `{"data":[{"index":1,"score":0.2},{"index":0,"score":0.8}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	event, err := p.Generate(context.Background(), models.MixedbreadRerankV31Listwise, request(`{"query":"红色","documents":["a","b"]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := event.GetItems()[0].GetText()
	if !strings.Contains(got, `"relevance_score":0.8`) || strings.Contains(got, `"score"`) {
		t.Fatalf("text %s", got)
	}
}

func TestRejectsOtherModel(t *testing.T) {
	p := client(t, func(r *http.Request) (*http.Response, error) {
		t.Fatal("wrong model must not call the vendor")
		return nil, nil
	})
	if _, err := p.Generate(context.Background(), models.CohereRerankV35Official, request(`{"query":"红色","documents":["a"]}`)); err == nil {
		t.Fatal("cohere model id must not be served")
	}
}

func TestRejectsIncompleteIndexes(t *testing.T) {
	for _, body := range []string{
		`{"data":[{"index":0,"score":0.8}]}`,
		`{"data":[{"index":0,"score":0.8},{"index":0,"score":0.2}]}`,
		`{"data":[{"index":2,"score":0.8},{"index":0,"score":0.2}]}`,
	} {
		p := client(t, func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})
		if _, err := p.Generate(context.Background(), models.MixedbreadRerankV31Listwise, request(`{"query":"红色","documents":["a","b"]}`)); err == nil {
			t.Fatalf("body %s must be rejected", body)
		}
	}
}

func TestListwiseUsesOneRequestAtLogicalLimit(t *testing.T) {
	calls := 0
	p := client(t, func(r *http.Request) (*http.Response, error) {
		calls++
		var payload wireRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Input) != 1000 {
			t.Fatalf("listwise candidate set was split: %d", len(payload.Input))
		}
		items := make([]map[string]any, len(payload.Input))
		for index := range items {
			items[index] = map[string]any{"index": index, "score": 0.5}
		}
		raw, err := json.Marshal(map[string]any{"data": items})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
	})
	documents := make([]string, 1001)
	for index := range documents {
		documents[index] = "document"
	}
	for _, count := range []int{1000, 1001} {
		raw, err := json.Marshal(rerankbatch.Request{Query: "query", Documents: documents[:count]})
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Generate(t.Context(), models.MixedbreadRerankV31Listwise, request(string(raw)))
		if count == 1000 && err != nil {
			t.Fatal(err)
		}
		if count == 1001 && (provider.Kind(err) != provider.ErrorInvalidArgument || !provider.IsNotAttempted(err)) {
			t.Fatalf("oversized listwise request: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("vendor calls=%d, want one complete listwise call", calls)
	}
}

func TestHTTPParameterErrorIsNotTransient(t *testing.T) {
	p := client(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 422, Body: io.NopCloser(strings.NewReader("invalid input")), Header: make(http.Header)}, nil
	})
	_, err := p.Generate(t.Context(), models.MixedbreadRerankV31Listwise, request(`{"query":"q","documents":["doc"]}`))
	if provider.Kind(err) != provider.ErrorInvalidArgument || provider.IsNotAttempted(err) {
		t.Fatalf("classification=%v", err)
	}
}

func capture(t *testing.T, userText string) map[string]any {
	t.Helper()
	var payload map[string]any
	p := client(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.mixedbread.com/v1/reranking" {
			t.Fatalf("url %s", r.URL.String())
		}
		if r.Header.Get("authorization") != "Bearer key" {
			t.Fatalf("authorization %s", r.Header.Get("authorization"))
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		input, _ := payload["input"].([]any)
		items := make([]string, 0, len(input))
		for i := range input {
			items = append(items, `{"index":`+strconv.Itoa(i)+`,"score":0.5}`)
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"data":[` + strings.Join(items, ",") + `]}`)),
			Header:     make(http.Header),
		}, nil
	})
	if _, err := p.Generate(context.Background(), models.MixedbreadRerankV31Listwise, request(userText)); err != nil {
		t.Fatal(err)
	}
	return payload
}

func client(t *testing.T, rt roundTrip) *Provider {
	t.Helper()
	p, err := New("mixedbread", "https://api.mixedbread.com", "key")
	if err != nil {
		t.Fatal(err)
	}
	p.client = rt
	return p
}

func request(body string) *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Parts: []*modelhubv2.ContentPart{{
				Content: &modelhubv2.ContentPart_Text{Text: body},
			}}}},
		}}},
	}
}
