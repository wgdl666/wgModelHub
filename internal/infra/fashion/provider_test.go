package fashion

import (
	"context"
	"encoding/json"
	pb "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFashionPreservesVectorSpaceAndRankContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "user" || p != "secret" {
			t.Error("missing service authentication")
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/v1/embed":
			if input["dim"] != float64(1024) || input["normalize"] != true {
				t.Error(input)
			}
			w.Write([]byte(`{"embeddings":[[0.25,0.75]]}`))
		case "/v1/rank":
			if input["sigmoid"] != true || input["top_k"] != float64(1) {
				t.Error(input)
			}
			w.Write([]byte(`{"scores":[{"index":1,"score":0.95}]}`))
		default:
			t.Error(r.URL.Path)
		}
	}))
	defer server.Close()
	p, err := New("fashion", server.URL, "user", "secret")
	if err != nil {
		t.Fatal(err)
	}
	for model, payload := range map[string]string{"fashion-embedding": `{"inputs":[{"image":"https://image.example/a.png"}],"normalize":true,"dim":1024}`, "fashion-reranker": `{"query":{"text":"red"},"documents":[{"text":"blue"},{"text":"red"}],"sigmoid":true,"top_k":1}`} {
		out, err := p.Generate(context.Background(), model, &pb.GenerateRequest{Input: &pb.Input{Items: []*pb.InputItem{{Item: &pb.InputItem_Message{Message: &pb.Message{Role: pb.Role_ROLE_USER, Parts: []*pb.ContentPart{{Content: &pb.ContentPart_Text{Text: payload}}}}}}}}})
		if err != nil {
			t.Fatal(err)
		}
		if !out.Final || !json.Valid([]byte(out.Items[0].GetText())) {
			t.Fatal(out)
		}
	}
}
