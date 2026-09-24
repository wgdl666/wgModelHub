package coherererank

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (r roundTrip) Do(req *http.Request) (*http.Response, error) { return r(req) }

func TestRerankDropsInstructAndReturnsScores(t *testing.T) {
	p, err := New("cohere", "https://api.cohere.com", "key")
	if err != nil {
		t.Fatal(err)
	}
	p.client = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.cohere.com/v2/rerank" {
			t.Fatalf("url %s", r.URL.String())
		}
		if r.Header.Get("authorization") != "Bearer key" {
			t.Fatalf("authorization %s", r.Header.Get("authorization"))
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		if _, ok := payload["instruct"]; ok {
			t.Fatalf("instruct must not be sent: %s", raw)
		}
		if payload["model"] != models.CohereRerankV35Official || payload["query"] != "红色" || payload["top_n"] != float64(2) {
			t.Fatalf("payload %s", raw)
		}
		body := `{"results":[{"index":1,"relevance_score":0.2},{"index":0,"relevance_score":0.9}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	event, err := p.Generate(context.Background(), models.CohereRerankV35Official, request(`{"query":"红色","documents":["a","b"],"instruct":"Given a color query"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := event.GetItems()[0].GetText(); !strings.Contains(got, `"relevance_score":0.9`) || strings.Contains(got, "instruct") {
		t.Fatalf("text %s", got)
	}
}

func TestRerankRejectsBedrockModel(t *testing.T) {
	p, err := New("cohere", "https://api.cohere.com", "key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Generate(context.Background(), models.CohereRerankV35, request(`{"query":"红色","documents":["a"]}`)); err == nil {
		t.Fatal("bedrock model id must not be served by the official client")
	}
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
