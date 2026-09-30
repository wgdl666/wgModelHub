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
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
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

func TestGarmentExtractionContract(t *testing.T) {
	src := pngBytes(t)
	out := pngBytes(t)
	var gotAuth string
	var gotImages [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/garment_extraction" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("model") != upstreamCompatibleModel {
			t.Fatalf("model = %q", r.FormValue("model"))
		}
		if r.FormValue("lora_name") != loraClothGen || r.FormValue("lora_strength") != loraStrength {
			t.Fatalf("lora = %q/%q", r.FormValue("lora_name"), r.FormValue("lora_strength"))
		}
		if r.FormValue("use_crop") != "true" || r.FormValue("size") != garmentSize || r.FormValue("response_format") != responseFormat || r.FormValue("n") != "1" {
			t.Fatalf("form = %#v", r.Form)
		}
		if r.FormValue("garment_name") != "白色衬衫" || r.FormValue("garment_role") != "clothing::top/full" {
			t.Fatalf("garment fields name=%q role=%q", r.FormValue("garment_name"), r.FormValue("garment_role"))
		}
		if r.Form.Has("prompt") || r.Form.Has("parse_mask") || r.Form.Has("parse_labels") {
			t.Fatal("garment_extraction must not send prompt/parse_mask/parse_labels")
		}
		if r.Form.Has("negative_prompt") {
			t.Fatal("unexpected negative_prompt")
		}
		files := r.MultipartForm.File["image"]
		if len(files) != 1 {
			t.Fatalf("image count = %d", len(files))
		}
		f, err := files[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			t.Fatal(err)
		}
		gotImages = append(gotImages, data)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(out)}},
			"crop": map[string]any{"applied": true},
		})
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	event, err := p.GenerateImage(context.Background(), models.VWorldWardrobe10, wardrobeRequest(src, `{"garment_name":"白色衬衫","garment_category":"tops.shirts"}`))
	if err != nil {
		t.Fatal(err)
	}
	user, pass, ok := parseBasicAuth(gotAuth)
	if !ok || user != "wgdl" || pass != "secret" {
		t.Fatalf("basic auth = %q", gotAuth)
	}
	if len(gotImages) != 1 || !bytes.Equal(gotImages[0], src) {
		t.Fatal("outfit image not forwarded")
	}
	if got := event.GetItems()[0].GetImage().GetData(); !bytes.Equal(got, out) {
		t.Fatalf("output len=%d", len(got))
	}
}

func TestGarmentExtractionForwardsParserMask(t *testing.T) {
	person := pngBytes(t)
	mask := []byte{1, 2, 3, 4}
	out := pngBytes(t)
	var gotImage, gotMask []byte
	var gotImageName, gotMaskName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Fatal(err)
		}
		if r.Form.Has("prompt") {
			t.Fatal("prompt must not be sent")
		}
		if r.FormValue("garment_name") != "白色衬衫" || r.FormValue("garment_role") != "clothing::top/full" {
			t.Fatalf("garment fields name=%q role=%q", r.FormValue("garment_name"), r.FormValue("garment_role"))
		}
		if r.FormValue("parse_labels") != `{"1":"top","4":"left shoe"}` {
			t.Fatalf("parse_labels = %q", r.FormValue("parse_labels"))
		}
		images := r.MultipartForm.File["image"]
		masks := r.MultipartForm.File["parse_mask"]
		if len(images) != 1 || len(masks) != 1 {
			t.Fatalf("files image=%d mask=%d", len(images), len(masks))
		}
		gotImageName = images[0].Filename
		gotMaskName = masks[0].Filename
		read := func(file *multipart.FileHeader) []byte {
			t.Helper()
			f, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			data, err := io.ReadAll(f)
			if err != nil {
				t.Fatal(err)
			}
			return data
		}
		gotImage = read(images[0])
		gotMask = read(masks[0])
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(out)}},
			"crop": map[string]any{"applied": true},
		})
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	_, err := p.GenerateImage(context.Background(), models.VWorldWardrobe10, wardrobeRequest(person, `{"garment_name":"白色衬衫","garment_category":"tops.shirts","parse_labels":{"4":"left shoe","1":"top"}}`, mask))
	if err != nil {
		t.Fatal(err)
	}
	if gotImageName != "source.png" || gotMaskName != "human_parse_labels.png" {
		t.Fatalf("filenames image=%q mask=%q", gotImageName, gotMaskName)
	}
	if !bytes.Equal(gotImage, person) || !bytes.Equal(gotMask, mask) {
		t.Fatal("person image or parse mask was not forwarded")
	}
}

func TestGarmentExtractionRejectsMaskWithoutLabels(t *testing.T) {
	p := newTestProvider(t, "http://127.0.0.1:1")
	_, err := p.GenerateImage(context.Background(), models.VWorldWardrobe10, wardrobeRequest(pngBytes(t), `{"garment_name":"白色衬衫"}`, []byte("mask")))
	if err == nil || !strings.Contains(err.Error(), "parse mask and parse_labels") {
		t.Fatalf("err = %v", err)
	}
}

func TestGarmentExtractionFailsWhenCropNotApplied(t *testing.T) {
	src := pngBytes(t)
	out := pngBytes(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(out)}},
			"crop": map[string]any{"applied": false},
		})
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	_, err := p.GenerateImage(context.Background(), models.VWorldWardrobe10, wardrobeRequest(src, `{"garment_name":"白衬衫","garment_category":"tops"}`))
	if err == nil || !strings.Contains(err.Error(), "crop.applied") {
		t.Fatalf("err = %v", err)
	}
}

func TestGarmentExtractionOmitsUnmappedRole(t *testing.T) {
	src := pngBytes(t)
	out := pngBytes(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Fatal(err)
		}
		if r.Form.Has("garment_role") {
			t.Fatalf("unexpected garment_role=%q", r.FormValue("garment_role"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(out)}},
			"crop": map[string]any{"applied": true},
		})
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	if _, err := p.GenerateImage(context.Background(), models.VWorldWardrobe10, wardrobeRequest(src, `{"garment_name":"项链","garment_category":"jewelry.necklaces"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestVTONEditsContract(t *testing.T) {
	person := pngBytes(t)
	garment := pngBytes(t)
	out := pngBytes(t)
	var gotAuth string
	var filenames []string
	var payloads [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/edits" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("model") != upstreamCompatibleModel {
			t.Fatalf("model = %q", r.FormValue("model"))
		}
		if r.FormValue("lora_name") != loraVTON || r.FormValue("lora_strength") != loraStrength {
			t.Fatalf("lora = %q/%q", r.FormValue("lora_name"), r.FormValue("lora_strength"))
		}
		if r.FormValue("size") != vtonSize || r.FormValue("response_format") != responseFormat || r.FormValue("n") != "1" {
			t.Fatalf("form = %#v", r.Form)
		}
		if r.FormValue("prompt") != "try this look" {
			t.Fatalf("prompt = %q", r.FormValue("prompt"))
		}
		if r.Form.Has("negative_prompt") {
			t.Fatal("must not depend on negative_prompt")
		}
		if r.FormValue("size") == "1024x1536" {
			t.Fatal("must not reuse OpenAI 3:4→1024x1536 conversion")
		}
		files := r.MultipartForm.File["image"]
		if len(files) != 2 {
			t.Fatalf("image count = %d", len(files))
		}
		for _, fh := range files {
			filenames = append(filenames, fh.Filename)
			f, err := fh.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(f)
			_ = f.Close()
			if err != nil {
				t.Fatal(err)
			}
			payloads = append(payloads, data)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(out)}},
		})
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	event, err := p.GenerateImage(context.Background(), models.VWorldOutfit10, outfitRequest(person, garment, "try this look"))
	if err != nil {
		t.Fatal(err)
	}
	user, pass, ok := parseBasicAuth(gotAuth)
	if !ok || user != "wgdl" || pass != "secret" {
		t.Fatalf("basic auth = %q", gotAuth)
	}
	if len(payloads) != 2 || !bytes.Equal(payloads[0], person) || !bytes.Equal(payloads[1], garment) {
		t.Fatalf("image order/content wrong filenames=%v", filenames)
	}
	if got := event.GetItems()[0].GetImage().GetData(); !bytes.Equal(got, out) {
		t.Fatalf("output len=%d", len(got))
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
}

func TestNewRequiresAuth(t *testing.T) {
	_, err := New("vworld", "https://vmind-image.example", "wgdl", "")
	var providerErr *provider.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != provider.ErrorConfiguration {
		t.Fatalf("err = %v", err)
	}
}

func TestGarmentRoleMapping(t *testing.T) {
	cases := map[string]string{
		"tops.shirts":       "clothing::top/full",
		"dresses.day":       "clothing::top/full",
		"bottoms.pants":     "clothing::bottom",
		"footwear.sneakers": "shoes",
		"accessories.bags":  "bag",
		"accessories.belts": "belt",
		"outfit":            "outfit",
		"jewelry.necklaces": "",
		"socks":             "",
		"jumpsuits":         "",
	}
	for category, want := range cases {
		if got := garmentRoleFromCategory(category); got != want {
			t.Fatalf("category %q => %q, want %q", category, got, want)
		}
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
