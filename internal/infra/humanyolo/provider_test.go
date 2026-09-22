package humanyolo

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

func TestGeneratePostsPredictWithBasicAuth(t *testing.T) {
	var gotAuth, gotConf string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "wgdl" || pass != "secret" || r.URL.Path != "/predict" {
			t.Errorf("auth/path = %s %s %s", user, pass, r.URL.Path)
		}
		gotAuth = user
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		gotConf = r.FormValue("conf")
		_, _ = io.WriteString(w, `{"success":true,"persons":[]}`)
	}))
	defer server.Close()
	p, err := New("yolo", server.URL, "wgdl", "secret")
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()
	event, err := p.Generate(context.Background(), models.HumanYOLO, &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Parts: []*modelhubv2.ContentPart{{
					Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
						Source: &modelhubv2.Media_Data{Data: []byte("png")},
					}},
				}},
			}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "wgdl" || gotConf != "0.35" {
		t.Fatalf("auth=%s conf=%s", gotAuth, gotConf)
	}
	if event.GetItems()[0].GetText() == "" || base64.StdEncoding.EncodeToString([]byte("png")) == "" {
		t.Fatal("empty response")
	}
}
