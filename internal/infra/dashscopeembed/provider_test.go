package dashscopeembed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (r roundTrip) Do(req *http.Request) (*http.Response, error) { return r(req) }

func TestEmbeddingForwardsCallerSignedHTTPSImageWithoutFetchingOrChangingIt(t *testing.T) {
	uri := "https://private-bucket.s3.us-east-2.amazonaws.com/image.png?X-Amz-Signature=fixture&X-Amz-Expires=600"
	request := &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Uri{Uri: uri}}}}}}}}}}}
	content, err := embedContent(request)
	if err != nil {
		t.Fatal(err)
	}
	if content["image"] != uri {
		t.Fatal("signed image URL must reach the vendor unchanged")
	}
	for _, invalid := range []string{"file:///tmp/image.png", "http://internal/image.png", "https://user:password@example.com/image.png", "https:///missing-host"} {
		request.Input.Items[0].GetMessage().Parts[0].GetImage().Source = &modelhubv2.Media_Uri{Uri: invalid}
		if _, err := embedContent(request); err == nil {
			t.Errorf("accepted invalid image source %q", invalid)
		}
	}
}

func TestEmbedTextReturns1024(t *testing.T) {
	provider, err := New("qwen", "https://dashscope.example/api/v1", "key", 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	provider.client = roundTrip(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/multimodal-embedding") {
			t.Fatalf("path %s", r.URL.Path)
		}
		vector := make([]float64, vectorDim)
		vector[0] = 0.5
		body, _ := json.Marshal(map[string]any{"output": map[string]any{"embeddings": []any{map[string]any{"embedding": vector}}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})
	event, err := provider.Generate(context.Background(), models.Qwen3VLEmbedding, &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Parts: []*modelhubv2.ContentPart{{
				Content: &modelhubv2.ContentPart_Text{Text: "black jacket"},
			}}}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(event.GetItems()[0].GetText(), "0.5") {
		t.Fatalf("text %s", event.GetItems()[0].GetText())
	}
}
