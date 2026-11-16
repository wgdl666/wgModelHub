package ominilinkvideo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/models"
)

func TestGenerateKlingUsesImage2VideoPath(t *testing.T) {
	const model = models.KlingV3
	const taskID = "kling-job"
	var baseURL string
	var createPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/image2video"):
			_ = json.NewDecoder(r.Body).Decode(&createPayload)
			_, _ = w.Write([]byte(`{"Response":{"JobId":"` + taskID + `"}}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/query/"):
			_, _ = w.Write([]byte(`{"Response":{"Status":"DONE","ResultVideoUrl":"` + baseURL + `/kling.mp4"}}`))
		case r.URL.Path == "/kling.mp4":
			_, _ = w.Write([]byte("mp4"))
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	baseURL = server.URL

	p := newTestProvider(server)
	if err := submitGetRead(context.Background(), p, model, testGenRequest("https://cdn.example/frame.png"), nil); err != nil {
		t.Fatal(err)
	}
	if createPayload["Model"] != "v3.0" {
		t.Fatalf("Model=%v", createPayload["Model"])
	}
	image, _ := createPayload["Image"].(map[string]any)
	if image["Url"] != "https://cdn.example/frame.png" || image["Base64"] != nil {
		t.Fatalf("Image=%v", image)
	}
}

func TestSubmitKlingInlineBytesAsBase64(t *testing.T) {
	const png = "fake-png"
	const model = models.KlingV3
	var createPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/image2video") {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&createPayload)
		_, _ = w.Write([]byte(`{"Response":{"JobId":"kling-inline"}}`))
	}))
	defer server.Close()
	p := newTestProvider(server)
	id, err := p.SubmitVideo(context.Background(), model, testInlineImageRequest([]byte(png), "image/png"))
	if err != nil {
		t.Fatal(err)
	}
	if id != "kling-inline" {
		t.Fatalf("id=%s", id)
	}
	image, _ := createPayload["Image"].(map[string]any)
	if _, ok := image["Url"]; ok {
		t.Fatalf("Image=%v", image)
	}
	got, _ := image["Base64"].(string)
	if strings.HasPrefix(got, "data:") {
		t.Fatalf("Base64=%q", got)
	}
	decoded, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != png {
		t.Fatalf("decoded=%q", decoded)
	}
}

func TestSubmitKlingCreateErrorIsSurfaced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/image2video") {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"Response":{"RequestId":"req-1","Error":{"Code":"InvalidParameterValue.UrlIllegal","Message":"URL格式不合法。"}}}`))
	}))
	defer server.Close()
	p := newTestProvider(server)
	_, err := p.SubmitVideo(context.Background(), models.KlingV3, testGenRequest("https://cdn.example/frame.png"))
	if err == nil || !strings.Contains(err.Error(), "InvalidParameterValue.UrlIllegal") || !strings.Contains(err.Error(), "URL格式不合法") {
		t.Fatalf("err=%v", err)
	}
}
