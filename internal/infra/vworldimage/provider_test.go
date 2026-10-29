package vworldimage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestMinimalBusinessContracts(t *testing.T) {
	source := pngBytes(t)
	prompt := "  try this look\nkeep whitespace  "
	cases := []struct {
		name, model, path string
		request           *modelhubv2.GenerateRequest
		fields            map[string]string
		files             map[string][]byte
	}{
		{"product", models.VWorldWardrobe10, productGenerationPath,
			wardrobeRequest(source, `{"garment_name":"White shirt","garment_role":"clothing::top/full"}`),
			map[string]string{"garment_name": "White shirt", "garment_role": "clothing::top/full"}, map[string][]byte{"image": source}},
		{"extraction", models.VWorldWardrobe10, garmentExtractionPath,
			wardrobeRequest(source, `{"garment_name":"shoes","garment_role":"shoes","parse_labels":{"15":"feet"}}`, []byte("mask")),
			map[string]string{"garment_name": "shoes", "garment_role": "shoes", "parse_labels": `{"15":"feet"}`}, map[string][]byte{"image": source, "parse_mask": []byte("mask")}},
		{"bbox", models.VWorldWardrobe10, garmentExtractionPath,
			wardrobeRequest(source, `{"garment_name":"白色衬衫","bbox":[0.1,0.2,0.8,0.9]}`),
			map[string]string{"garment_name": "白色衬衫", "bbox": "[0.1,0.2,0.8,0.9]"}, map[string][]byte{"image": source}},
		{"bbox pixel with role", models.VWorldWardrobe10, garmentExtractionPath,
			wardrobeRequest(source, `{"garment_name":"pants","garment_role":"clothing::bottom","bbox":[100,200,500,800],"bbox_format":"pixel_xyxy"}`),
			map[string]string{"garment_name": "pants", "garment_role": "clothing::bottom", "bbox": "[100,200,500,800]", "bbox_format": "pixel_xyxy"}, map[string][]byte{"image": source}},
		{"bbox empty role omitted", models.VWorldWardrobe10, garmentExtractionPath,
			wardrobeRequest(source, `{"garment_name":"shirt","garment_role":"","bbox":[0,0,1,1]}`),
			map[string]string{"garment_name": "shirt", "bbox": "[0,0,1,1]"}, map[string][]byte{"image": source}},
		{"vton", models.VWorldOutfit10, vtonPath, outfitRequest(source, []byte("garment"), prompt),
			map[string]string{"prompt": prompt}, map[string][]byte{"person_image": source, "garment_image": []byte("garment")}},
	}
	body := wardrobeRequest(source, "caller prompt must not be sent")
	body.Input.ModelTraits = `{"gender":"female"}`
	cases = append(cases, struct {
		name, model, path string
		request           *modelhubv2.GenerateRequest
		fields            map[string]string
		files             map[string][]byte
	}{
		"body", models.VWorldBodyModel10, bodyModelPath, body, map[string]string{"gender": "female"}, map[string][]byte{"image": source},
	})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Output hints from generic clients must not become model-side controls.
			tc.request.Output = &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{AspectRatio: strPtr("1:1"), ImageSize: strPtr("4K")}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("path=%s want %s", r.URL.Path, tc.path)
				}
				user, pass, ok := r.BasicAuth()
				if !ok || user != "wgdl" || pass != "secret" {
					t.Error("missing upstream auth")
				}
				if err := r.ParseMultipartForm(8 << 20); err != nil {
					t.Error(err)
					return
				}
				if len(r.MultipartForm.Value) != len(tc.fields) {
					t.Errorf("unexpected parameters: %#v", r.MultipartForm.Value)
				}
				for field, want := range tc.fields {
					if r.FormValue(field) != want {
						t.Errorf("%s=%q want %q", field, r.FormValue(field), want)
					}
				}
				if len(r.MultipartForm.File) != len(tc.files) {
					t.Errorf("unexpected uploads: %#v", r.MultipartForm.File)
				}
				for field, want := range tc.files {
					files := r.MultipartForm.File[field]
					if len(files) != 1 {
						t.Errorf("%s count=%d", field, len(files))
						continue
					}
					f, err := files[0].Open()
					if err != nil {
						t.Error(err)
						continue
					}
					got, err := io.ReadAll(f)
					f.Close()
					if err != nil || !bytes.Equal(got, want) {
						t.Errorf("%s bytes mismatch: %v", field, err)
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(source)}}})
			}))
			defer server.Close()
			event, err := newTestProvider(t, server.URL).GenerateImage(context.Background(), tc.model, tc.request)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(event.GetItems()[0].GetImage().GetData(), source) {
				t.Fatal("output mismatch")
			}
		})
	}
}

func TestServerOwnsMissingAndInvalidBusinessFields(t *testing.T) {
	tests := []struct {
		model   string
		request *modelhubv2.GenerateRequest
	}{
		{models.VWorldWardrobe10, wardrobeRequest(pngBytes(t), `{"garment_name":"","garment_role":"future-role"}`)},
		{models.VWorldWardrobe10, wardrobeRequest(pngBytes(t), `{"garment_name":"shoe"}`, []byte("mask"))},
		{models.VWorldWardrobe10, wardrobeRequest(pngBytes(t), `{"garment_name":"shoe","parse_labels":{"15":"feet"}}`)},
		{models.VWorldBodyModel10, wardrobeRequest(pngBytes(t), "")},
		{models.VWorldOutfit10, outfitRequest(pngBytes(t), pngBytes(t), " ")},
	}
	for _, tc := range tests {
		hits := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"code":"invalid_request","field":"test","message":"model validation rejected"}}`))
		}))
		_, err := newTestProvider(t, server.URL).GenerateImage(context.Background(), tc.model, tc.request)
		server.Close()
		if hits != 1 || err == nil || provider.IsNotAttempted(err) || !strings.Contains(err.Error(), "model validation rejected") {
			t.Fatalf("hits=%d err=%v", hits, err)
		}
	}
}

func TestRejectsWrongImageCountsAndUnknownModel(t *testing.T) {
	pngData := pngBytes(t)
	p := newTestProvider(t, "https://vmind-image.example")
	if _, err := p.GenerateImage(context.Background(), models.VWorldWardrobe10, wardrobeRequest(pngData, `{"garment_name":"x"}`, pngData, pngData)); err == nil {
		t.Fatal("expected extra image error")
	}
	if _, err := p.GenerateImage(context.Background(), models.VWorldOutfit10, wardrobeRequest(pngData, "prompt")); err == nil {
		t.Fatal("expected missing second image error")
	}
	if _, err := p.GenerateImage(context.Background(), models.Flux2Klein9B, wardrobeRequest(pngData, `{"garment_name":"x"}`)); err == nil {
		t.Fatal("expected unknown model error")
	}
	if _, err := p.GenerateImage(context.Background(), models.VWorldBodyModel10, wardrobeRequest(pngData, "prompt", pngData)); err == nil {
		t.Fatal("expected body model extra image error")
	}
	if _, err := p.GenerateImage(context.Background(), models.VWorldBodyModel10, &modelhubv2.GenerateRequest{}); err == nil {
		t.Fatal("expected body model missing image error")
	}
}

func TestNewRequiresAuth(t *testing.T) {
	_, err := New("vworld", "https://vmind-image.example", "wgdl", "")
	var providerErr *provider.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != provider.ErrorConfiguration {
		t.Fatalf("err = %v", err)
	}
}

func wardrobeRequest(first []byte, text string, extra ...[]byte) *modelhubv2.GenerateRequest {
	parts := []*modelhubv2.ContentPart{
		{Content: &modelhubv2.ContentPart_Image{Image: inlinePNG(first)}},
	}
	for _, img := range extra {
		parts = append(parts, &modelhubv2.ContentPart{Content: &modelhubv2.ContentPart_Image{Image: inlinePNG(img)}})
	}
	if strings.TrimSpace(text) != "" {
		parts = append(parts, &modelhubv2.ContentPart{Content: &modelhubv2.ContentPart_Text{Text: text}})
	}
	return &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role:  modelhubv2.Role_ROLE_USER,
				Parts: parts,
			}},
		}}},
	}
}

func outfitRequest(person, garment []byte, prompt string) *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role: modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{
					{Content: &modelhubv2.ContentPart_Image{Image: inlinePNG(person)}},
					{Content: &modelhubv2.ContentPart_Image{Image: inlinePNG(garment)}},
					{Content: &modelhubv2.ContentPart_Text{Text: prompt}},
				},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{
			AspectRatio: strPtr("3:4"),
			ImageSize:   strPtr("1K"),
		}}},
	}
}

func inlinePNG(data []byte) *modelhubv2.Media {
	return &modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: data}}
}

func strPtr(v string) *string { return &v }

func TestBaseURLFollowsResolverOnNextRequest(t *testing.T) {
	src := pngBytes(t)
	out := pngBytes(t)
	var hits []string
	newServer := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits = append(hits, name)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(out)}},
				"crop": map[string]any{"applied": true},
			})
		}))
	}
	first := newServer("startup")
	defer first.Close()
	second := newServer("hot")
	defer second.Close()

	current := first.URL
	p, err := NewResolving("vworld_flux_image", "wgdl", "secret", func() string { return current })
	if err != nil {
		t.Fatal(err)
	}
	p.client = http.DefaultClient
	req := wardrobeRequest(src, `{"garment_name":"白色衬衫","garment_category":"tops.shirts"}`)
	if _, err := p.GenerateImage(context.Background(), models.VWorldWardrobe10, req); err != nil {
		t.Fatal(err)
	}
	current = second.URL
	if _, err := p.GenerateImage(context.Background(), models.VWorldWardrobe10, req); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0] != "startup" || hits[1] != "hot" {
		t.Fatalf("hits=%v", hits)
	}
}

func TestDoMultipartSendsTraceIDOnlyWhenSpanIsValid(t *testing.T) {
	var got string
	var present bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		present = r.Form.Has("trace_id")
		got = r.FormValue("trace_id")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	p := newTestProvider(t, server.URL)

	if _, err := p.doMultipart(context.Background(), "/v1/images/edits", map[string]string{"n": "1"}, nil); err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatalf("trace_id=%q without span", got)
	}

	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("test").Start(context.Background(), "modelhub.Generate")
	defer span.End()
	want := span.SpanContext().TraceID().String()
	if _, err := p.doMultipart(ctx, "/v1/images/edits", map[string]string{"n": "1"}, nil); err != nil {
		t.Fatal(err)
	}
	if !present || got != want {
		t.Fatalf("trace_id=%q present=%v want %s", got, present, want)
	}
}

func newTestProvider(t *testing.T, baseURL string) *Provider {
	t.Helper()
	p, err := New("vworld_flux_image", baseURL, "wgdl", "secret")
	if err != nil {
		t.Fatal(err)
	}
	p.client = http.DefaultClient
	return p
}

func parseBasicAuth(header string) (user, pass string, ok bool) {
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header[len(prefix):]))
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}
