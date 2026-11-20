package openai

import (
	"context"
	"encoding/json"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/wgdl666/wgModelHub/internal/infra/vmindgateway"
	"github.com/wgdl666/wgModelHub/models"
)

func TestVmindEmbeddingGatewayContract(t *testing.T) {
	for _, dimension := range []int{1024, 3} {
		t.Run(strconv.Itoa(dimension), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/gateway/requests/"+models.VmindEmbedding8B || r.Header.Get("Authorization") != "Bearer test" {
					t.Error("wrong gateway request")
				}
				var body struct {
					Input struct {
						Inputs    []string `json:"inputs"`
						Dim       int      `json:"dim"`
						Normalize bool     `json:"normalize"`
					} `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if len(body.Input.Inputs) != 1 || body.Input.Inputs[0] != "蓝色衬衫" || body.Input.Dim != 1024 || !body.Input.Normalize {
					t.Errorf("wrong embedding payload: %+v", body)
				}
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"dim": dimension, "embeddings": [][]float64{make([]float64, dimension)}}})
			}))
			defer server.Close()
			gateway, err := vmindgateway.New("test", server.URL, "test", nil)
			if err != nil {
				t.Fatal(err)
			}
			p := NewGateway("test", gateway)
			event, err := p.Generate(context.Background(), models.VmindEmbedding8B, systemUserRequest("", "蓝色衬衫", nil))
			if dimension != 1024 {
				if err == nil {
					t.Fatal("invalid dimension accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Embedding []float64 `json:"embedding"`
			}
			if err := json.Unmarshal([]byte(event.GetItems()[0].GetText()), &result); err != nil || len(result.Embedding) != 1024 {
				t.Fatalf("bad normalized output: %v", err)
			}
		})
	}
}

func TestVmindEmbeddingImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input struct {
				Inputs []json.RawMessage `json:"inputs"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if len(body.Input.Inputs) != 1 {
			t.Error("incorrect input count")
			return
		}
		var image map[string]string
		if err := json.Unmarshal(body.Input.Inputs[0], &image); err != nil || image["image"] != "data:image/png;base64,AQID" {
			t.Error("image protocol changed")
		}
		vector := make([]float64, 1024)
		vector[0] = 1
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"dim": 1024, "embeddings": [][]float64{vector}}})
	}))
	defer server.Close()
	gateway, err := vmindgateway.New("test", server.URL, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	request := systemUserRequest("", "shirt", nil)
	request.Input.Items = request.Input.Items[1:]
	request.Input.Items[0].GetMessage().Parts = []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte{1, 2, 3}}}}}}
	event, err := NewGateway("test", gateway).Generate(context.Background(), models.VmindEmbedding8B, request)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Embedding []float64 `json:"embedding"`
	}
	if err = json.Unmarshal([]byte(event.GetItems()[0].GetText()), &output); err != nil || len(output.Embedding) != 1024 || output.Embedding[0] != 1 {
		t.Fatal("single image result changed", err)
	}
}
func TestVmindEmbeddingRejectsUnusedBatchProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unsupported batch reached gateway") }))
	defer server.Close()
	gateway, err := vmindgateway.New("test", server.URL, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	request := systemUserRequest("", "shirt", nil)
	request.Input.Items = request.Input.Items[1:]
	request.Input.ModelTraits = `{"embedding_batch":true,"input_type":"search_query"}`
	request.Input.Items = append(request.Input.Items, request.Input.Items[0])
	if _, err = NewGateway("test", gateway).Generate(context.Background(), models.VmindEmbedding8B, request); err == nil {
		t.Fatal("unsupported batch accepted")
	}
}
