package lenscorrection

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(body)) }))
		p, _ := New("lens", server.URL)
		_, req := fixture(t)
		_, err := p.GenerateImage(context.Background(), models.LensDistortionCorrection, req)
		var pe *provider.Error
		if !errors.As(err, &pe) || pe.Kind != provider.ErrorInvalidResponse || calls != 1 {
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

// 模型以 Base64 返回 JPEG，适配层只解码 Base64 并透传 MIME 和诊断。
func TestJPEGBase64PassThrough(t *testing.T) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 4, 3)), nil); err != nil {
		t.Fatal(err)
	}
	data := encoded.Bytes()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"image_base64": base64.StdEncoding.EncodeToString(data), "content_type": "image/jpeg", "bytes": len(data), "out_width": 4, "out_height": 3})
	}))
	defer server.Close()
	p, _ := New("lens", server.URL)
	_, req := fixture(t)
	event, err := p.GenerateImage(context.Background(), models.LensDistortionCorrection, req)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(event.Items[0].GetImage().GetData(), data) || event.Items[0].GetImage().GetMimeType() != "image/jpeg" {
		t.Fatal("JPEG bytes or MIME changed")
	}
}

// 用真实 HTTP 响应覆盖恢复、耗尽与不可重试错误，并核对每次仍发送完整原图。
func TestTransientHTTPRetries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		statuses  []int
		wantCalls int
		wantOK    bool
	}{
		{"first_success", []int{200}, 1, true},
		{"second_success", []int{502, 200}, 2, true},
		{"third_success", []int{502, 503, 200}, 3, true},
		{"exhausted", []int{502, 502, 502, 200}, 3, false},
		{"rate_limit", []int{429, 200}, 2, true},
		{"internal_error", []int{500, 200}, 2, true},
		{"gateway_timeout", []int{504, 200}, 2, true},
		{"bad_request", []int{400, 200}, 1, false},
		{"unauthorized", []int{401, 200}, 1, false},
		{"forbidden", []int{403, 200}, 1, false},
		{"not_found", []int{404, 200}, 1, false},
		{"not_implemented", []int{501, 200}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data, req := fixture(t)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Base64 string `json:"base64"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Base64 != base64.StdEncoding.EncodeToString(data) {
					t.Error("retry did not send original image")
				}
				status := tc.statuses[calls%len(tc.statuses)]
				calls++
				w.WriteHeader(status)
				if status == 200 {
					_ = json.NewEncoder(w).Encode(map[string]any{"image_base64": base64.StdEncoding.EncodeToString(data), "content_type": "image/png"})
				}
			}))
			defer server.Close()
			p, _ := New("lens", server.URL)
			event, err := p.GenerateImage(context.Background(), models.LensDistortionCorrection, req)
			if calls != tc.wantCalls || (err == nil) != tc.wantOK {
				t.Fatalf("calls=%d want=%d err=%v", calls, tc.wantCalls, err)
			}
			if tc.wantOK && !bytes.Equal(event.Items[0].GetImage().GetData(), data) {
				t.Fatal("recovered output changed")
			}
			if tc.name == "exhausted" && !strings.Contains(err.Error(), "502") {
				t.Fatalf("lost final upstream error: %v", err)
			}
		})
	}
}

type retryTransport func(*http.Request) (*http.Response, error)

func (f retryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNetworkFailureRetries(t *testing.T) {
	_, req := fixture(t)
	p, _ := New("lens", "http://lens.invalid")
	calls := 0
	p.client.Transport = retryTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, io.ErrUnexpectedEOF
	})
	_, err := p.GenerateImage(context.Background(), models.LensDistortionCorrection, req)
	if calls != 3 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestRetryBackoffStopsOnContext(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint(deadline), func(t *testing.T) {
			_, req := fixture(t)
			p, _ := New("lens", "http://lens.invalid")
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
				want = context.DeadlineExceeded
			}
			defer cancel()
			calls := 0
			p.client.Transport = retryTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if !deadline {
					time.AfterFunc(50*time.Millisecond, cancel)
				}
				return &http.Response{StatusCode: 502, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: r}, nil
			})
			_, err := p.GenerateImage(ctx, models.LensDistortionCorrection, req)
			if calls != 1 || !errors.Is(err, want) {
				t.Fatalf("calls=%d err=%v want=%v", calls, err, want)
			}
		})
	}
}
