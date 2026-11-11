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

func TestGenerateVeoUsesInstancesPayload(t *testing.T) {
	const model = models.Veo31Generate001
	const taskID = "veo-task"
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
	}))
	defer imageServer.Close()

	var createPayload map[string]any
	var apiBase string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, model):
			_ = json.NewDecoder(r.Body).Decode(&createPayload)
			_, _ = w.Write([]byte(`{"name":"` + taskID + `"}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/query/"):
			_, _ = w.Write([]byte(`{"done":true,"response":{"videos":[{"gcsUri":"` + apiBase + `/veo.mp4","mimeType":"video/mp4"}]}}`))
		case r.URL.Path == "/veo.mp4":
			_, _ = w.Write([]byte("mp4"))
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}))
	defer apiServer.Close()
	apiBase = apiServer.URL

	p := newTestProvider(apiServer)
	req := testGenRequest(imageServer.URL + "/frame.png")
	if err := submitGetRead(context.Background(), p, model, req, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := createPayload["instances"]; !ok {
		t.Fatalf("payload=%v", createPayload)
	}
}

func TestSubmitVeoInlineBytesSkipsFetch(t *testing.T) {
	const png = "\x89PNG"
	const model = models.Veo31Generate001
	var createPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&createPayload)
		_, _ = w.Write([]byte(`{"name":"veo-inline"}`))
	}))
	defer server.Close()
	p := newTestProvider(server)
	id, err := p.SubmitVideo(context.Background(), model, testInlineImageRequest([]byte(png), "image/png"))
	if err != nil {
		t.Fatal(err)
	}
	if id != "veo-inline" {
		t.Fatalf("id=%s", id)
	}
	instances, _ := createPayload["instances"].([]any)
	if len(instances) != 1 {
		t.Fatalf("instances=%v", createPayload["instances"])
	}
	inst, _ := instances[0].(map[string]any)
	image, _ := inst["image"].(map[string]any)
	if image["mimeType"] != "image/png" {
		t.Fatalf("image=%v", image)
	}
	got, _ := image["bytesBase64Encoded"].(string)
	decoded, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != png {
		t.Fatalf("decoded=%q", decoded)
	}
}
