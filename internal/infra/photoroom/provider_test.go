package photoroom

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"github.com/wgdl666/wgModelHub/protocol"
)

const tinyPNG = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x0cIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82"

func imageRequest(images ...*modelhubv2.Media) *modelhubv2.GenerateRequest {
	parts := make([]*modelhubv2.ContentPart, 0, len(images))
	for _, image := range images {
		parts = append(parts, &modelhubv2.ContentPart{Content: &modelhubv2.ContentPart_Image{Image: image}})
	}
	return &modelhubv2.GenerateRequest{
		Model: models.PhotoroomSegment,
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role:  modelhubv2.Role_ROLE_USER,
				Parts: parts,
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
	}
}

func newTestProvider(t *testing.T, baseURL string) *Provider {
	t.Helper()
	p, err := New("photoroom_test", "test-api-key", baseURL, "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewDefaultsBaseURL(t *testing.T) {
	p, err := New("photoroom", "key", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.baseURL != DefaultBaseURL {
		t.Fatalf("baseURL=%q, want %q", p.baseURL, DefaultBaseURL)
	}
	if p.segmentURL() != DefaultBaseURL+segmentPath {
		t.Fatalf("segmentURL=%q", p.segmentURL())
	}
}

func TestNewRejectsInvalidProxyURL(t *testing.T) {
	_, err := New("photoroom", "key", "", "http://[")
	if err == nil || !strings.Contains(err.Error(), "proxy URL is invalid") {
		t.Fatalf("err=%v", err)
	}
	assertKind(t, err, provider.ErrorConfiguration)
}

func TestNewRoutesHTTPThroughProxy(t *testing.T) {
	var proxied atomic.Bool
	// handler 不得 t.Fatal/FailNow：会在服务端 goroutine 里立刻结束测试，客户端可能挂死。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("direct upstream hit; request must go through proxy")
		http.Error(w, "direct hit", http.StatusBadGateway)
	}))
	defer upstream.Close()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Store(true)
		if r.Method != http.MethodPost {
			t.Errorf("proxy method=%s", r.Method)
			http.Error(w, "bad method", http.StatusBadRequest)
			return
		}
		// Go HTTP 客户端经明文代理时使用绝对 URL；确认仍指向上游 segment。
		wantTarget := upstream.URL + segmentPath
		if r.RequestURI != wantTarget && r.URL.String() != wantTarget {
			t.Errorf("proxy target requestURI=%q url=%q, want %q", r.RequestURI, r.URL.String(), wantTarget)
			http.Error(w, "bad target", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte(tinyPNG))
	}))
	defer proxy.Close()

	p, err := New("photoroom_test", "test-api-key", upstream.URL, proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	event, err := p.GenerateImage(context.Background(), models.PhotoroomSegment, imageRequest(
		&modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte(tinyPNG)}},
	))
	if err != nil {
		t.Fatal(err)
	}
	if !proxied.Load() {
		t.Fatal("request did not go through proxy")
	}
	if string(event.GetItems()[0].GetImage().GetData()) != tinyPNG {
		t.Fatalf("unexpected output")
	}
}

func TestGenerateImageInlinePNGRoundTrip(t *testing.T) {
	var gotAPIKey, gotContentType string
	var gotFileName, gotFileType string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != segmentPath || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		gotAPIKey = r.Header.Get("x-api-key")
		gotContentType = r.Header.Get("Content-Type")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("image_file")
		if err != nil {
			t.Errorf("FormFile: %v", err)
			http.Error(w, "missing image_file", http.StatusBadRequest)
			return
		}
		defer file.Close()
		gotFileName = header.Filename
		gotFileType = header.Header.Get("Content-Type")
		gotBody, err = io.ReadAll(file)
		if err != nil {
			t.Errorf("ReadAll: %v", err)
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte(tinyPNG))
	}))
	defer server.Close()

	// 未配置 proxy_url：走 New 默认 client；成功即验证未配置路径仍可完成真实请求。
	p := newTestProvider(t, server.URL)
	event, err := p.GenerateImage(context.Background(), models.PhotoroomSegment, imageRequest(
		&modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte(tinyPNG)}},
	))
	if err != nil {
		t.Fatal(err)
	}
	if gotAPIKey != "test-api-key" {
		t.Fatalf("x-api-key=%q", gotAPIKey)
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data;") {
		t.Fatalf("Content-Type=%q", gotContentType)
	}
	if gotFileName != "image.png" || gotFileType != "image/png" || string(gotBody) != tinyPNG {
		t.Fatalf("file name=%q type=%q body=%q", gotFileName, gotFileType, gotBody)
	}
	if !event.GetFinal() || len(event.GetItems()) != 1 {
		t.Fatalf("event=%#v", event)
	}
	image := event.GetItems()[0].GetImage()
	if image == nil || image.GetMimeType() != "image/png" || string(image.GetData()) != tinyPNG {
		t.Fatalf("image=%#v", image)
	}
}

func TestGenerateImageURIInput(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/source.jpg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xff, 0xd8, 0xff, 0xd9})
	})
	mux.HandleFunc(segmentPath, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("image_file")
		if err != nil {
			t.Errorf("FormFile: %v", err)
			http.Error(w, "missing image_file", http.StatusBadRequest)
			return
		}
		defer file.Close()
		if header.Filename != "image.jpg" || header.Header.Get("Content-Type") != "image/jpeg" {
			t.Errorf("header=%#v", header)
			http.Error(w, "bad file header", http.StatusBadRequest)
			return
		}
		data, _ := io.ReadAll(file)
		if !bytes.Equal(data, []byte{0xff, 0xd8, 0xff, 0xd9}) {
			t.Errorf("uploaded=%v", data)
			http.Error(w, "bad upload", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(tinyPNG))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	p := newTestProvider(t, server.URL)
	event, err := p.GenerateImage(context.Background(), models.PhotoroomSegment, imageRequest(
		&modelhubv2.Media{Source: &modelhubv2.Media_Uri{Uri: server.URL + "/source.jpg"}},
	))
	if err != nil {
		t.Fatal(err)
	}
	if string(event.GetItems()[0].GetImage().GetData()) != tinyPNG {
		t.Fatalf("unexpected output")
	}
}

func TestGenerateImageRejectsNoImage(t *testing.T) {
	p := newTestProvider(t, "http://127.0.0.1:1")
	_, err := p.GenerateImage(context.Background(), models.PhotoroomSegment, &modelhubv2.GenerateRequest{
		Input:  &modelhubv2.Input{},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
	})
	assertKind(t, err, provider.ErrorInvalidArgument)
}

func TestGenerateImageRejectsMultipleImages(t *testing.T) {
	p := newTestProvider(t, "http://127.0.0.1:1")
	_, err := p.GenerateImage(context.Background(), models.PhotoroomSegment, imageRequest(
		&modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte(tinyPNG)}},
		&modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte(tinyPNG)}},
	))
	assertKind(t, err, provider.ErrorInvalidArgument)
}

func TestGenerateImageRejectsNonImageMIME(t *testing.T) {
	p := newTestProvider(t, "http://127.0.0.1:1")
	_, err := p.GenerateImage(context.Background(), models.PhotoroomSegment, imageRequest(
		&modelhubv2.Media{MimeType: "application/pdf", Source: &modelhubv2.Media_Data{Data: []byte("%PDF")}},
	))
	assertKind(t, err, provider.ErrorInvalidArgument)
}

func TestGenerateImageRejectsUnsupportedURIScheme(t *testing.T) {
	p := newTestProvider(t, "http://127.0.0.1:1")
	_, err := p.GenerateImage(context.Background(), models.PhotoroomSegment, imageRequest(
		&modelhubv2.Media{Source: &modelhubv2.Media_Uri{Uri: "file:///tmp/x.png"}},
	))
	assertKind(t, err, provider.ErrorInvalidArgument)
}

func TestGenerateImageRejectsEmptyURIContent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/empty", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
	})
	mux.HandleFunc(segmentPath, func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("segment must not be called")
		http.Error(w, "unexpected segment", http.StatusBadGateway)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	p := newTestProvider(t, server.URL)
	_, err := p.GenerateImage(context.Background(), models.PhotoroomSegment, imageRequest(
		&modelhubv2.Media{Source: &modelhubv2.Media_Uri{Uri: server.URL + "/empty"}},
	))
	assertKind(t, err, provider.ErrorInvalidArgument)
}

func TestGenerateImageRejectsOversizedURIInput(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(bytes.Repeat([]byte{0x01}, protocol.MaxMediaBytes+1))
	})
	mux.HandleFunc(segmentPath, func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("segment must not be called")
		http.Error(w, "unexpected segment", http.StatusBadGateway)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	p := newTestProvider(t, server.URL)
	_, err := p.GenerateImage(context.Background(), models.PhotoroomSegment, imageRequest(
		&modelhubv2.Media{Source: &modelhubv2.Media_Uri{Uri: server.URL + "/big"}},
	))
	assertKind(t, err, provider.ErrorInvalidArgument)
}

func TestGenerateImageRejectsNonImageURIBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/text", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("not-an-image"))
	})
	mux.HandleFunc(segmentPath, func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("segment must not be called")
		http.Error(w, "unexpected segment", http.StatusBadGateway)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	p := newTestProvider(t, server.URL)
	_, err := p.GenerateImage(context.Background(), models.PhotoroomSegment, imageRequest(
		&modelhubv2.Media{Source: &modelhubv2.Media_Uri{Uri: server.URL + "/text"}},
	))
	assertKind(t, err, provider.ErrorInvalidArgument)
}

func TestGenerateImageRejectsNonPNGResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-png"))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	_, err := p.GenerateImage(context.Background(), models.PhotoroomSegment, imageRequest(
		&modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte(tinyPNG)}},
	))
	assertKind(t, err, provider.ErrorInvalidResponse)
}

func TestGenerateImageRejectsCorruptPNGSignatureOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 仅八字节 PNG 签名：能骗过纯 magic-byte 检查，但 DecodeConfig 必须拒绝。
		_, _ = w.Write([]byte(pngSignature))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	_, err := p.GenerateImage(context.Background(), models.PhotoroomSegment, imageRequest(
		&modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte(tinyPNG)}},
	))
	assertKind(t, err, provider.ErrorInvalidResponse)
}

func TestGenerateImageRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		payload := append([]byte(pngSignature), bytes.Repeat([]byte{0x00}, protocol.MaxMediaBytes)...)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	_, err := p.GenerateImage(context.Background(), models.PhotoroomSegment, imageRequest(
		&modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte(tinyPNG)}},
	))
	assertKind(t, err, provider.ErrorInvalidResponse)
}

func TestGenerateImageMapsUpstreamStatus(t *testing.T) {
	cases := []struct {
		status int
		kind   provider.ErrorKind
	}{
		{http.StatusBadRequest, provider.ErrorInvalidArgument},
		{http.StatusUnauthorized, provider.ErrorConfiguration},
		{http.StatusTooManyRequests, provider.ErrorRateLimited},
		{http.StatusGatewayTimeout, provider.ErrorTimeout},
		{http.StatusInternalServerError, provider.ErrorUnavailable},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":{"message":"colorSpace is not supported"}}`))
			}))
			defer server.Close()

			p := newTestProvider(t, server.URL)
			_, err := p.GenerateImage(context.Background(), models.PhotoroomSegment, imageRequest(
				&modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte(tinyPNG)}},
			))
			assertKind(t, err, tc.kind)
			if !strings.Contains(err.Error(), "colorSpace is not supported") {
				t.Fatalf("error dropped vendor body: %v", err)
			}
			if strings.Contains(err.Error(), "test-api-key") {
				t.Fatalf("error leaked api key: %v", err)
			}
		})
	}
}

func TestGenerateImageDoesNotSendModelUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}
		if _, ok := r.MultipartForm.Value["model"]; ok {
			t.Errorf("model must not be sent upstream")
			http.Error(w, "unexpected model", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(tinyPNG))
	}))
	defer server.Close()

	p := newTestProvider(t, server.URL)
	if _, err := p.GenerateImage(context.Background(), models.PhotoroomSegment, imageRequest(
		&modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte(tinyPNG)}},
	)); err != nil {
		t.Fatal(err)
	}
}

func assertKind(t *testing.T, err error, kind provider.ErrorKind) {
	t.Helper()
	var providerErr *provider.Error
	if !errors.As(err, &providerErr) {
		t.Fatalf("err=%v (%T), want provider.Error", err, err)
	}
	if providerErr.Kind != kind {
		t.Fatalf("kind=%s, want %s (%v)", providerErr.Kind, kind, err)
	}
}
