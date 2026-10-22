package humanyolo

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

func TestGenerateUploadsOriginalImageUsingHTTP1ToHTTP2CapableServer(t *testing.T) {
	image := bytes.Repeat([]byte("original-image"), 100000)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			http.Error(w, "HTTP/2 upload stalls on the supplier ingress", http.StatusBadGateway)
			return
		}
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		got, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(got, image) {
			t.Errorf("uploaded image changed: bytes=%d err=%v", len(got), err)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "wgdl" || pass != "secret" || r.URL.Path != "/predict" || r.FormValue("conf") != "0.35" || r.FormValue("iou") != "0.45" {
			t.Error("supplier request contract changed")
		}
		_, _ = io.WriteString(w, `{"success":true,"persons":[]}`)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	previous := http.DefaultTransport
	trusted := server.Client().Transport.(*http.Transport).Clone()
	http.DefaultTransport = trusted
	t.Cleanup(func() { http.DefaultTransport = previous })
	p, err := New("yolo", server.URL, "wgdl", "secret")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Generate(context.Background(), models.HumanYOLO, &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{Source: &modelhubv2.Media_Data{Data: image}}}}},
			}},
		}}},
	})
	if err != nil {
		t.Fatalf("original image upload failed: %v", err)
	}
	control, err := (&http.Client{Transport: trusted}).Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Body.Close()
	if control.ProtoMajor != 2 {
		t.Fatalf("shared transport lost HTTP/2 support: %s", control.Proto)
	}
}

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
