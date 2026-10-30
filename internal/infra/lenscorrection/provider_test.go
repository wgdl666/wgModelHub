package lenscorrection

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

func fixture(t *testing.T) ([]byte, *modelhubv2.GenerateRequest) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	return data, &modelhubv2.GenerateRequest{Model: models.LensDistortionCorrection, Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Role: modelhubv2.Role_ROLE_USER, Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: data}}}}}}}}}}}
}

// 两个环境仅 URL 前缀不同；默认开关和显式 false 都必须按上游契约传递。
func TestCorrectContractAndDiagnostics(t *testing.T) {
	for _, prefix := range []string{"", "/lens_distortion_correction"} {
		for _, disabled := range []bool{false, true} {
			name := prefix + "-defaults"
			if disabled {
				name = prefix + "-disabled"
			}
			t.Run(name, func(t *testing.T) {
				data, request := fixture(t)
				if disabled {
					request.Input.ModelTraits = `{"mirror_lens":false,"crop":false}`
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "POST" || r.URL.Path != prefix+"/correct" || r.Header.Get("Content-Type") != "application/json" {
						t.Errorf("request=%s %s %s", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if len(body) != 3 || body["base64"] != base64.StdEncoding.EncodeToString(data) || body["mirror_lens"] != !disabled || body["crop"] != !disabled {
						t.Error("request payload or switch mismatch")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"image_base64": base64.StdEncoding.EncodeToString(data), "content_type": "image/png", "bytes": len(data), "in_width": 6000, "in_height": 4000, "processed_width": 4096, "processed_height": 2731, "resized": true, "out_width": 4, "out_height": 3, "mirror_lens": !disabled, "crop": !disabled, "resample_ms": 500.0, "sharpen_ms": 200.0, "elapsed_ms": 700.0})
				}))
				defer server.Close()
				p, err := New("lens", server.URL+prefix)
				if err != nil {
					t.Fatal(err)
				}
				event, err := p.GenerateImage(context.Background(), models.LensDistortionCorrection, request)
				if err != nil {
					t.Fatal(err)
				}
				if !event.Final || len(event.Items) != 2 || !bytes.Equal(event.Items[0].GetImage().GetData(), data) {
					t.Fatal("PNG output changed")
				}
				var meta diagnostics
				if err := json.Unmarshal([]byte(event.Items[1].GetText()), &meta); err != nil {
					t.Fatal(err)
				}
				if !meta.Resized || meta.InWidth != 6000 || meta.ProcessedWidth != 4096 || meta.ElapsedMS != 700 || meta.MirrorLens != !disabled {
					t.Fatalf("diagnostics=%+v", meta)
				}
				if event.GetGenerationElapsedMs() != 0 {
					t.Fatal("upstream compute time presented as complete generation time")
				}
			})
		}
	}
}

func TestRejectsInvalidInputBeforeHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	p, _ := New("lens", server.URL)
	for _, kind := range []string{"empty", "two", "uri", "traits", "model"} {
		_, req := fixture(t)
		model := models.LensDistortionCorrection
		switch kind {
		case "empty":
			req.Input.Items = nil
		case "two":
			req.Input.Items = append(req.Input.Items, req.Input.Items[0])
		case "uri":
			req.Input.Items[0].GetMessage().Parts[0].GetImage().Source = &modelhubv2.Media_Uri{Uri: "https://example.com/private.png"}
		case "traits":
			req.Input.ModelTraits = `{"mirror_lens":"false"}`
		case "model":
			model = "wrong"
		}
		_, err := p.GenerateImage(context.Background(), model, req)
		var pe *provider.Error
		if !errors.As(err, &pe) || pe.Kind != provider.ErrorInvalidArgument || !pe.Local {
			t.Fatalf("%s error=%v", kind, err)
		}
	}
	if calls != 0 {
		t.Fatalf("sent %d invalid calls", calls)
	}
}

func TestRejectsMalformedResponseAndRedactsError(t *testing.T) {
	for _, body := range []string{`{`, `{"image_base64":"bad","content_type":"image/png"}`, `{"image_base64":"","bytes":0}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		p, _ := New("lens", server.URL)
		_, req := fixture(t)
		_, err := p.GenerateImage(context.Background(), models.LensDistortionCorrection, req)
		var pe *provider.Error
		if !errors.As(err, &pe) || pe.Kind != provider.ErrorInvalidResponse {
			t.Fatalf("error=%v", err)
		}
		server.Close()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte("PRIVATE_IMAGE_BASE64"))
	}))
	defer server.Close()
	p, _ := New("lens", server.URL)
	_, req := fixture(t)
	_, err := p.GenerateImage(context.Background(), models.LensDistortionCorrection, req)
	if err == nil || strings.Contains(err.Error(), "PRIVATE_IMAGE_BASE64") {
		t.Fatalf("unsafe error=%v", err)
	}
}

func TestCancellationPropagates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	p, _ := New("lens", server.URL)
	_, req := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.GenerateImage(ctx, models.LensDistortionCorrection, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
