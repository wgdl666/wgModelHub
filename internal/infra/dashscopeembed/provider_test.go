package dashscopeembed

import (
	"bytes"
	"context"
	"encoding/base64"
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

func TestEmbedTextReturns1024(t *testing.T) {
	provider, err := New("qwen", "https://dashscope.example/api/v1", "key")
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

func TestEmbedImageRequestAndReplay(t *testing.T) {
	p, err := New("qwen", "https://dashscope.example/api/v1", "key")
	if err != nil {
		t.Fatal(err)
	}
	image := bytes.Repeat([]byte{0, 255, 34, 92}, 2048)
	p.client = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatal("request method or headers changed")
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(map[string]any{
			"model": models.Qwen3VLEmbedding,
			"input": map[string]any{"contents": []map[string]string{{
				"image": "data:image/png;base64," + base64.StdEncoding.EncodeToString(image),
			}}},
			"parameters": map[string]any{"dimension": vectorDim, "output_type": "dense"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, want) || r.ContentLength != int64(len(raw)) {
			t.Fatal("image request differs from the existing JSON contract")
		}
		if r.GetBody == nil {
			t.Fatal("request cannot be replayed")
		}
		replay, err := r.GetBody()
		if err != nil {
			t.Fatal(err)
		}
		defer replay.Close()
		again, err := io.ReadAll(replay)
		if err != nil || !bytes.Equal(raw, again) {
			t.Fatal("replayed image request differs")
		}
		response, err := json.Marshal(map[string]any{"output": map[string]any{
			"embeddings": []any{map[string]any{"embedding": make([]float64, vectorDim)}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(response)), Header: make(http.Header)}, nil
	})
	_, err = p.Generate(context.Background(), models.Qwen3VLEmbedding, &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Parts: []*modelhubv2.ContentPart{{
				Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
					MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: image},
				}},
			}}}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
}
