package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

const tinyPNG = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x0cIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82"

func imageRequest(prompt, ratio, size string, images ...*modelhubv2.Media) *modelhubv2.GenerateRequest {
	parts := []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: prompt}}}
	for _, image := range images {
		parts = append(parts, &modelhubv2.ContentPart{Content: &modelhubv2.ContentPart_Image{Image: image}})
	}
	out := &modelhubv2.ImageOutput{}
	if ratio != "" {
		out.AspectRatio = &ratio
	}
	if size != "" {
		out.ImageSize = &size
	}
	return &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role:  modelhubv2.Role_ROLE_USER,
				Parts: parts,
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: out}},
	}
}

func TestBuildImageGenerationsBodyMapsRatioWithoutReference(t *testing.T) {
	body := buildImageGenerationsBody(models.GPTImage2, "red apple", imageRequest("red apple", "3:4", "2K"))
	if body["model"] != models.GPTImage2 || body["prompt"] != "red apple" || body["n"] != 1 {
		t.Fatalf("body=%#v", body)
	}
	if _, ok := body["response_format"]; ok {
		t.Fatalf("gpt-image-2 rejects response_format: %#v", body)
	}
	if body["size"] != "1024x1536" {
		t.Fatalf("size=%#v", body["size"])
	}
	if _, ok := body["image"]; ok {
		t.Fatalf("generations must not send image field: %#v", body)
	}
}

func TestBuildImageGenerationsBodyPrefersPixelImageSize(t *testing.T) {
	body := buildImageGenerationsBody(models.GPTImage2, "hi", imageRequest("hi", "3:4", "1024x1024"))
	if body["size"] != "1024x1024" {
		t.Fatalf("size=%#v", body["size"])
	}
}

func TestBuildImageGenerationsBodyOmitsSizeWhenUnset(t *testing.T) {
	body := buildImageGenerationsBody(models.GPTImage2, "hi", imageRequest("hi", "", ""))
	if _, ok := body["size"]; ok {
		t.Fatalf("size=%#v", body["size"])
	}
}

func TestImagesAPIURLsAppendV1(t *testing.T) {
	p := &Provider{baseURL: "https://api.ominilink.ai"}
	if got := p.imagesAPIURL("generations"); got != "https://api.ominilink.ai/v1/images/generations" {
		t.Fatalf("generations url=%q", got)
	}
	if got := p.imagesAPIURL("edits"); got != "https://api.ominilink.ai/v1/images/edits" {
		t.Fatalf("edits url=%q", got)
	}
	p.baseURL = "https://api.ominilink.ai/v1"
	if got := p.imagesAPIURL("generations"); got != "https://api.ominilink.ai/v1/images/generations" {
		t.Fatalf("generations url=%q", got)
	}
	if got := p.imagesAPIURL("edits"); got != "https://api.ominilink.ai/v1/images/edits" {
		t.Fatalf("edits url=%q", got)
	}
}

func TestReferenceImageFilenameStripsMIMEParameters(t *testing.T) {
	if got := referenceImageFilename("image/jpeg; charset=binary", 0); got != "reference-1.jpg" {
		t.Fatalf("filename=%q", got)
	}
}

func TestGenerateImagePostsGenerationsJSON(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/images/generations" {
			t.Fatalf("path=%s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("auth=%q", request.Header.Get("Authorization"))
		}
		if ct := request.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("content-type=%q", ct)
		}
		raw, _ := io.ReadAll(request.Body)
		if err := json.Unmarshal(raw, &captured); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"data": []map[string]string{{
				"b64_json":       base64.StdEncoding.EncodeToString([]byte(tinyPNG)),
				"revised_prompt": "a red apple",
			}},
			"usage": map[string]int{"prompt_tokens": 8, "completion_tokens": 2, "total_tokens": 10},
		})
	}))
	defer server.Close()

	p, err := New("ominilink_gpt_image", "secret", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	event, err := p.GenerateImage(context.Background(), models.GPTImage2, imageRequest("red apple", "1:1", ""))
	if err != nil {
		t.Fatal(err)
	}
	if captured["model"] != models.GPTImage2 || captured["size"] != "1024x1024" {
		t.Fatalf("captured=%#v", captured)
	}
	if _, ok := captured["image"]; ok {
		t.Fatalf("generations must not send image field: %#v", captured)
	}
	if len(event.GetItems()) != 2 {
		t.Fatalf("items=%#v", event.GetItems())
	}
	image := event.GetItems()[0].GetImage()
	if image == nil || image.GetMimeType() != "image/png" || string(image.GetData()) != tinyPNG {
		t.Fatalf("image=%#v", image)
	}
	if event.GetItems()[1].GetText() != "a red apple" {
		t.Fatalf("text=%#v", event.GetItems()[1])
	}
	if event.GetUsage().GetTotalTokens() != 10 {
		t.Fatalf("usage=%#v", event.GetUsage())
	}
}

func TestGenerateImageEditsWithInlineReference(t *testing.T) {
	var gotPath string
	var gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		gotContentType = request.Header.Get("Content-Type")
		if !strings.HasPrefix(gotContentType, "multipart/form-data;") {
			t.Fatalf("content-type=%q", gotContentType)
		}
		if err := request.ParseMultipartForm(8 << 20); err != nil {
			t.Fatal(err)
		}
		if got := request.FormValue("model"); got != models.GPTImage2 {
			t.Fatalf("model=%q", got)
		}
		if got := request.FormValue("prompt"); got != "make this cleaner" {
			t.Fatalf("prompt=%q", got)
		}
		if got := request.FormValue("n"); got != "1" {
			t.Fatalf("n=%q", got)
		}
		if got := request.FormValue("size"); got != "1024x1536" {
			t.Fatalf("size=%q", got)
		}
		files := request.MultipartForm.File["image"]
		if len(files) != 1 {
			t.Fatalf("image files=%d", len(files))
		}
		file, err := files[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "source-image" {
			t.Fatalf("data=%q", data)
		}
		if got := files[0].Header.Get("Content-Type"); got != "image/png" {
			t.Fatalf("content-type=%q", got)
		}
		if !strings.HasSuffix(files[0].Filename, ".png") {
			t.Fatalf("filename=%q", files[0].Filename)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString([]byte(tinyPNG))}},
		})
	}))
	defer server.Close()

	p, err := New("ominilink_gpt_image", "secret", server.URL+"/v1")
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	event, err := p.GenerateImage(context.Background(), models.GPTImage2, imageRequest("make this cleaner", "3:4", "",
		&modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte("source-image")}},
	))
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/images/edits" {
		t.Fatalf("path=%q", gotPath)
	}
	image := event.GetItems()[0].GetImage()
	if image == nil || string(image.GetData()) != tinyPNG {
		t.Fatalf("image=%#v", image)
	}
}

func TestGenerateImageEditsWithMultipleReferencesPreservesOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/images/edits" {
			t.Fatalf("path=%s", request.URL.Path)
		}
		if err := request.ParseMultipartForm(8 << 20); err != nil {
			t.Fatal(err)
		}
		files := request.MultipartForm.File["image"]
		if len(files) != 2 {
			t.Fatalf("image files=%d", len(files))
		}
		for i, want := range []struct {
			data string
			mime string
			ext  string
		}{
			{"first-ref", "image/png", ".png"},
			{"second-ref", "image/jpeg", ".jpg"},
		} {
			file, err := files[i].Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(file)
			file.Close()
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != want.data {
				t.Fatalf("file[%d] data=%q", i, data)
			}
			if got := files[i].Header.Get("Content-Type"); got != want.mime {
				t.Fatalf("file[%d] content-type=%q", i, got)
			}
			if !strings.HasSuffix(files[i].Filename, want.ext) {
				t.Fatalf("file[%d] filename=%q", i, files[i].Filename)
			}
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString([]byte(tinyPNG))}},
		})
	}))
	defer server.Close()

	p, err := New("ominilink_gpt_image", "secret", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	_, err = p.GenerateImage(context.Background(), models.GPTImage2, imageRequest("edit", "", "",
		&modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte("first-ref")}},
		&modelhubv2.Media{MimeType: "image/jpeg", Source: &modelhubv2.Media_Data{Data: []byte("second-ref")}},
	))
	if err != nil {
		t.Fatal(err)
	}
}

func TestGenerateImageEditsFetchesURIReference(t *testing.T) {
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/ref.png", func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(tinyPNG))
	})
	mux.HandleFunc("/v1/images/edits", func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseMultipartForm(8 << 20); err != nil {
			t.Fatal(err)
		}
		files := request.MultipartForm.File["image"]
		if len(files) != 1 {
			t.Fatalf("image files=%d", len(files))
		}
		file, err := files[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(file)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != tinyPNG {
			t.Fatalf("data=%q", data)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString([]byte(tinyPNG))}},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	p, err := New("ominilink_gpt_image", "secret", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	_, err = p.GenerateImage(context.Background(), models.GPTImage2, imageRequest("edit", "", "",
		&modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Uri{Uri: serverURL + "/ref.png"}},
	))
	if err != nil {
		t.Fatal(err)
	}
}

func TestGenerateImageEditsUsesHTTPContentTypeForURIWithoutMIME(t *testing.T) {
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/assets/proxy", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "image/png")
		_, _ = writer.Write([]byte(tinyPNG))
	})
	mux.HandleFunc("/v1/images/edits", func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseMultipartForm(8 << 20); err != nil {
			t.Fatal(err)
		}
		files := request.MultipartForm.File["image"]
		if len(files) != 1 {
			t.Fatalf("image files=%d", len(files))
		}
		if got := files[0].Header.Get("Content-Type"); got != "image/png" {
			t.Fatalf("content-type=%q", got)
		}
		if !strings.HasSuffix(files[0].Filename, ".png") {
			t.Fatalf("filename=%q", files[0].Filename)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString([]byte(tinyPNG))}},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	p, err := New("ominilink_gpt_image", "secret", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	_, err = p.GenerateImage(context.Background(), models.GPTImage2, imageRequest("edit", "", "",
		&modelhubv2.Media{Source: &modelhubv2.Media_Uri{Uri: serverURL + "/v1/assets/proxy?key=foo"}},
	))
	if err != nil {
		t.Fatal(err)
	}
}

func TestGenerateImageEditsSniffsURIWhenHTTPContentTypeMissing(t *testing.T) {
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/assets/proxy", func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(tinyPNG))
	})
	mux.HandleFunc("/v1/images/edits", func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseMultipartForm(8 << 20); err != nil {
			t.Fatal(err)
		}
		files := request.MultipartForm.File["image"]
		if len(files) != 1 {
			t.Fatalf("image files=%d", len(files))
		}
		if got := files[0].Header.Get("Content-Type"); got != "image/png" {
			t.Fatalf("content-type=%q", got)
		}
		if !strings.HasSuffix(files[0].Filename, ".png") {
			t.Fatalf("filename=%q", files[0].Filename)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString([]byte(tinyPNG))}},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	p, err := New("ominilink_gpt_image", "secret", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	_, err = p.GenerateImage(context.Background(), models.GPTImage2, imageRequest("edit", "", "",
		&modelhubv2.Media{Source: &modelhubv2.Media_Uri{Uri: serverURL + "/v1/assets/proxy?key=foo"}},
	))
	if err != nil {
		t.Fatal(err)
	}
}

func TestGenerateImageEditsIgnoresNonImageHTTPContentType(t *testing.T) {
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/assets/proxy", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write([]byte(tinyPNG))
	})
	mux.HandleFunc("/v1/images/edits", func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseMultipartForm(8 << 20); err != nil {
			t.Fatal(err)
		}
		files := request.MultipartForm.File["image"]
		if len(files) != 1 {
			t.Fatalf("image files=%d", len(files))
		}
		if got := files[0].Header.Get("Content-Type"); got != "image/png" {
			t.Fatalf("content-type=%q", got)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString([]byte(tinyPNG))}},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	p, err := New("ominilink_gpt_image", "secret", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	_, err = p.GenerateImage(context.Background(), models.GPTImage2, imageRequest("edit", "", "",
		&modelhubv2.Media{Source: &modelhubv2.Media_Uri{Uri: serverURL + "/v1/assets/proxy?key=foo"}},
	))
	if err != nil {
		t.Fatal(err)
	}
}

func TestGenerateImageEditsRejectsURIReferenceWithNonImageContent(t *testing.T) {
	var serverURL string
	var editsCalled bool
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/assets/proxy", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write([]byte("<html><body>not an image</body></html>"))
	})
	mux.HandleFunc("/v1/images/edits", func(writer http.ResponseWriter, _ *http.Request) {
		editsCalled = true
		writer.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	p, err := New("ominilink_gpt_image", "secret", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	_, err = p.GenerateImage(context.Background(), models.GPTImage2, imageRequest("edit", "", "",
		&modelhubv2.Media{Source: &modelhubv2.Media_Uri{Uri: serverURL + "/v1/assets/proxy?key=foo"}},
	))
	if err == nil || provider.Kind(err) != provider.ErrorInvalidArgument {
		t.Fatalf("err=%v kind=%s", err, provider.Kind(err))
	}
	if editsCalled {
		t.Fatal("edits endpoint should not be called")
	}
}

func TestGenerateImageEditsRejectsURIReferenceWhenContentTypeAndBodyNotImage(t *testing.T) {
	var serverURL string
	var editsCalled bool
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/assets/proxy", func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("plain-text-not-an-image"))
	})
	mux.HandleFunc("/v1/images/edits", func(writer http.ResponseWriter, _ *http.Request) {
		editsCalled = true
		writer.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	p, err := New("ominilink_gpt_image", "secret", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	_, err = p.GenerateImage(context.Background(), models.GPTImage2, imageRequest("edit", "", "",
		&modelhubv2.Media{Source: &modelhubv2.Media_Uri{Uri: serverURL + "/v1/assets/proxy"}},
	))
	if err == nil || provider.Kind(err) != provider.ErrorInvalidArgument {
		t.Fatalf("err=%v kind=%s", err, provider.Kind(err))
	}
	if editsCalled {
		t.Fatal("edits endpoint should not be called")
	}
}

func TestGenerateImageRejectsEmptyPrompt(t *testing.T) {
	p := &Provider{name: "test"}
	_, err := p.GenerateImage(context.Background(), models.GPTImage2, imageRequest("  ", "", ""))
	if err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("err=%v", err)
	}
}

func TestGenerateImageRejectsReferenceWithoutUploadableContent(t *testing.T) {
	p := &Provider{name: "test", client: http.DefaultClient}
	_, err := p.GenerateImage(context.Background(), models.GPTImage2, imageRequest("edit", "", "",
		&modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: nil}},
	))
	if err == nil || provider.Kind(err) != provider.ErrorInvalidArgument {
		t.Fatalf("err=%v kind=%s", err, provider.Kind(err))
	}
}

func TestGenerateImageRejectsEmptyURIReferenceBeforeEdits(t *testing.T) {
	var serverURL string
	var editsCalled bool
	mux := http.NewServeMux()
	mux.HandleFunc("/empty-ref", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/images/edits", func(writer http.ResponseWriter, _ *http.Request) {
		editsCalled = true
		writer.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	p, err := New("ominilink_gpt_image", "secret", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	_, err = p.GenerateImage(context.Background(), models.GPTImage2, imageRequest("edit", "", "",
		&modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Uri{Uri: serverURL + "/empty-ref"}},
		&modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte("valid-inline")}},
	))
	if err == nil || provider.Kind(err) != provider.ErrorInvalidArgument {
		t.Fatalf("err=%v kind=%s", err, provider.Kind(err))
	}
	if editsCalled {
		t.Fatal("edits endpoint should not be called")
	}
}

func TestGenerateImageFetchesURLResult(t *testing.T) {
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/images/generations", func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"data": []map[string]string{{"url": serverURL + "/asset.png"}},
		})
	})
	mux.HandleFunc("/asset.png", func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(tinyPNG))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	p, err := New("ominilink_gpt_image", "secret", server.URL+"/v1")
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	event, err := p.GenerateImage(context.Background(), models.GPTImage2, imageRequest("hi", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	image := event.GetItems()[0].GetImage()
	if image == nil || string(image.GetData()) != tinyPNG {
		t.Fatalf("image=%#v", image)
	}
}

func TestGenerateImageHTTPErrorMapped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	p, err := New("ominilink_gpt_image", "secret", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	_, err = p.GenerateImage(context.Background(), models.GPTImage2, imageRequest("hi", "", ""))
	if err == nil || provider.Kind(err) != provider.ErrorRateLimited {
		t.Fatalf("err=%v kind=%s", err, provider.Kind(err))
	}
}
