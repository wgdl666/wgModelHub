package ltx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/vmindgateway"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"github.com/wgdl666/wgModelHub/protocol"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// 网关只确认调用方幂等身份，不再分配或返回另一个视频任务 ID。
func acceptGatewayRequest(t *testing.T, w http.ResponseWriter, r *http.Request) string {
	t.Helper()
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		t.Error("missing idempotency key")
	}
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"idempotency_key": key, "state": "queued"})
	return key
}

// submitGetRead 仅测试复用：走正式 Submit→Get→ReadResult；无 deadline 时加 5s 上限，避免无界轮询。
func submitGetRead(ctx context.Context, p provider.VideoProvider, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}
	id, err := p.SubmitVideo(ctx, model, request)
	if err != nil {
		return err
	}
	for {
		job, err := p.GetVideo(ctx, model, id)
		if err != nil {
			return err
		}
		switch job.State {
		case provider.VideoJobSucceeded:
			return p.ReadVideoResult(ctx, model, id, emit)
		case provider.VideoJobFailed:
			if job.Err != nil {
				return job.Err
			}
			return provider.New(provider.ErrorUnavailable, "video job failed")
		default:
			wait := time.Duration(job.PollAfterMs) * time.Millisecond
			if wait <= 0 {
				wait = time.Second
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
}

func TestProviderRetriesRateLimitedSubmitWithSameIdentity(t *testing.T) {
	var submitCalls atomic.Int32
	var firstKey string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/gateway/requests/"+models.VWorldOutfitVideo10 {
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
		if request.URL.RawQuery != "" || request.Header.Get("Authorization") != "Bearer token" {
			t.Error("expected authenticated model-key-only request")
		}
		key := request.Header.Get("Idempotency-Key")
		if key == "" || (firstKey != "" && firstKey != key) {
			t.Error("idempotency key changed during submit retry")
		}
		firstKey = key
		if submitCalls.Add(1) == 1 {
			http.Error(writer, "queue full", http.StatusTooManyRequests)
			return
		}
		acceptGatewayRequest(t, writer, request)
	}))
	defer server.Close()

	provider, err := New("ltx", server.URL, "token", 4, 24, 42, 0.001)
	if err != nil {
		t.Fatal(err)
	}
	provider.client = server.Client()

	jobID, err := provider.submit(context.Background(), []byte("png"), "image/png", "prompt", "")
	if err != nil {
		t.Fatal(err)
	}
	if submitCalls.Load() != 2 || jobID != firstKey {
		t.Fatalf("calls=%d job=%s", submitCalls.Load(), jobID)
	}
}

func TestProviderDoesNotRetrySubmitOn5xx(t *testing.T) {
	var submitCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submitCalls.Add(1)
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer server.Close()

	p, err := New("ltx", server.URL, "token", 4, 24, 42, 0.001)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()
	_, err = p.submit(context.Background(), []byte("png"), "image/png", "prompt", "")
	if err == nil {
		t.Fatal("expected error")
	}
	if submitCalls.Load() != 1 {
		t.Fatalf("calls=%d want 1 (no 5xx auto-retry)", submitCalls.Load())
	}
}

func TestGetVideoStatusMapping(t *testing.T) {
	tests := []struct {
		status string
		want   provider.VideoJobState
	}{
		{"succeeded", provider.VideoJobSucceeded},
		{"failed", provider.VideoJobFailed},
		{"queued", provider.VideoJobRunning},
		{"processing", provider.VideoJobRunning},
	}
	for _, tc := range tests {
		t.Run(tc.status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/gateway/requests" || r.URL.RawQuery != "" || r.Header.Get("Idempotency-Key") != "job-1" {
					t.Error("lookup must use only the original idempotency key")
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"state": tc.status})
			}))
			defer server.Close()
			p, err := New("ltx", server.URL, "token", 4, 24, 42, 0.001)
			if err != nil {
				t.Fatal(err)
			}
			p.client = server.Client()
			job, err := p.GetVideo(context.Background(), models.VWorldOutfitVideo10, "job-1")
			if err != nil {
				t.Fatal(err)
			}
			if job.State != tc.want {
				t.Fatalf("state=%v want=%v", job.State, tc.want)
			}
		})
	}
}

func TestGetVideoRejectsLegacyState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "done"})
	}))
	defer server.Close()
	p, _ := New("ltx", server.URL, "token", 4, 24, 42, 1)
	if _, err := p.GetVideo(context.Background(), models.VWorldOutfitVideo10, "key"); err == nil {
		t.Fatal("legacy status accepted")
	}
}

func TestReadVideoResultResolvesRelativeVideoURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/gateway/requests":
			_ = json.NewEncoder(writer).Encode(map[string]any{"state": "succeeded", "result": map[string]string{"video_url": "/assets/result.mp4"}})
		case "/assets/result.mp4":
			_, _ = writer.Write([]byte("video-bytes"))
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	}))
	defer server.Close()

	p, err := New("ltx", server.URL, "token", 4, 24, 42, 0.001)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	var got []byte
	err = p.ReadVideoResult(context.Background(), models.VWorldOutfitVideo10, "job-1", func(ev *modelhubv2.GenerateEvent) error {
		for _, item := range ev.GetItems() {
			got = append(got, item.GetVideo().GetData()...)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "video-bytes" {
		t.Fatalf("video = %q", got)
	}
}

func TestReadVideoResultRejectsOversizedVideo(t *testing.T) {
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/gateway/requests":
			_ = json.NewEncoder(writer).Encode(map[string]any{"state": "succeeded", "result": map[string]string{"video_url": serverURL + "/big.mp4"}})
		case "/big.mp4":
			_, _ = writer.Write(make([]byte, protocol.MaxVideoBytes+1))
		default:
			t.Fatalf("path %s", request.URL.Path)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	p, err := New("ltx", server.URL, "token", 4, 24, 42, 0.001)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	err = p.ReadVideoResult(context.Background(), models.VWorldOutfitVideo10, "job-1", nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversize error, got %v", err)
	}
}

func TestReadVideoResultAcceptsExactlyMaxVideoSize(t *testing.T) {
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/gateway/requests":
			_ = json.NewEncoder(writer).Encode(map[string]any{"state": "succeeded", "result": map[string]string{"video_url": serverURL + "/exact.mp4"}})
		case "/exact.mp4":
			_, _ = writer.Write(make([]byte, protocol.MaxVideoBytes))
		default:
			t.Fatalf("path %s", request.URL.Path)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	p, err := New("ltx", server.URL, "token", 4, 24, 42, 0.001)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	var total int
	err = p.ReadVideoResult(context.Background(), models.VWorldOutfitVideo10, "job-1", func(ev *modelhubv2.GenerateEvent) error {
		for _, item := range ev.GetItems() {
			total += len(item.GetVideo().GetData())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != protocol.MaxVideoBytes {
		t.Fatalf("len = %d", total)
	}
}

func TestSubmitReadsRequestBodyOnEachRetry(t *testing.T) {
	var bodies []int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		data, _ := io.ReadAll(request.Body)
		bodies = append(bodies, len(data))
		if len(bodies) == 1 {
			http.Error(writer, "retry", http.StatusTooManyRequests)
			return
		}
		acceptGatewayRequest(t, writer, request)
	}))
	defer server.Close()

	p, err := New("ltx", server.URL, "token", 4, 24, 42, 0.001)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	if _, err := p.submit(context.Background(), []byte("frame"), "image/png", "prompt", ""); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] == 0 || bodies[0] != bodies[1] {
		t.Fatalf("bodies = %#v", bodies)
	}
}

func TestReadVideoResultRejectsEmptyBody(t *testing.T) {
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/gateway/requests":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "succeeded", "result": map[string]string{"video_url": serverURL + "/empty.mp4"}})
		case "/empty.mp4":
			// zero bytes
		}
	}))
	defer server.Close()
	serverURL = server.URL

	p, _ := New("ltx", server.URL, "token", 4, 24, 42, 0.001)
	p.client = server.Client()
	err := p.ReadVideoResult(context.Background(), models.VWorldOutfitVideo10, "job-1", nil)
	if err == nil || !strings.Contains(err.Error(), "0 bytes") {
		t.Fatalf("err=%v", err)
	}
}

func TestReadVideoResultStreamsMultipleChunks(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), protocol.VideoChunkBytes+50)
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/gateway/requests":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "succeeded", "result": map[string]string{"video_url": serverURL + "/chunked.mp4"}})
		case "/chunked.mp4":
			_, _ = w.Write(payload)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	p, _ := New("ltx", server.URL, "token", 4, 24, 42, 0.001)
	p.client = server.Client()
	var chunks int
	err := p.ReadVideoResult(context.Background(), models.VWorldOutfitVideo10, "job-1", func(ev *modelhubv2.GenerateEvent) error {
		chunks++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if chunks != 2 {
		t.Fatalf("chunks=%d", chunks)
	}
}

// 停用旧模型必须覆盖提交、轮询和下载，且不能访问上游。
func TestProviderRejectsLegacyModelBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	p, err := New("wgdl_video", server.URL, "token", 4, 24, 42, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.SubmitVideo(context.Background(), "ltx", &modelhubv2.GenerateRequest{})
	if err == nil || !strings.Contains(err.Error(), "unsupported video model") {
		t.Fatalf("error=%v", err)
	}
	_, err = p.GetVideo(context.Background(), "ltx", "legacy-job")
	if err == nil || !strings.Contains(err.Error(), "unsupported video model") {
		t.Fatalf("GetVideo error=%v", err)
	}
	err = p.ReadVideoResult(context.Background(), "ltx", "legacy-job", nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported video model") {
		t.Fatalf("ReadVideoResult error=%v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("unexpected upstream calls: %d", calls.Load())
	}
}

func TestSubmitVideoUsesModelKeyJSONContract(t *testing.T) {
	var submittedKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, images := decodeGatewayRequest(t, r)
		if r.URL.Path != "/v1/gateway/requests/"+models.VWorldOutfitVideo10 {
			t.Fatalf("path=%s", r.URL.Path)
		}
		for _, field := range []string{"model", "resolution", "duration", "fps", "seed", "audio", "inference_profile"} {
			if _, exists := payload.Input[field]; exists {
				t.Errorf("unexpected inference field %s", field)
			}
		}
		if len(images) != 1 || string(images[0].Data) != "png" {
			t.Fatal("missing first frame")
		}
		submittedKey = acceptGatewayRequest(t, w, r)
	}))
	defer server.Close()
	p, err := New("ltx_video", server.URL, "token", 4, 24, 42, 1)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()
	id, err := p.SubmitVideo(context.Background(), models.VWorldOutfitVideo10, &modelhubv2.GenerateRequest{
		Model: models.VWorldOutfitVideo10,
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role: modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{{
					Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
						MimeType: "image/png",
						Source:   &modelhubv2.Media_Data{Data: []byte("png")},
					}},
				}},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{Resolution: "720p"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != submittedKey {
		t.Fatalf("job=%s", id)
	}
}

func TestSubmitSendsTraceIDOnlyWhenSpanIsValid(t *testing.T) {
	var traces []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := decodeGatewayRequest(t, r)
		traces = append(traces, payload.Metadata.TraceID)
		acceptGatewayRequest(t, w, r)
	}))
	defer server.Close()
	p, err := New("ltx", server.URL, "token", 4, 24, 42, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.submit(context.Background(), []byte("image"), "image/png", "prompt", ""); err != nil {
		t.Fatal(err)
	}
	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("test").Start(context.Background(), "modelhub.SubmitVideo")
	defer span.End()
	if _, err := p.submit(ctx, []byte("image"), "image/png", "prompt", ""); err != nil {
		t.Fatal(err)
	}
	want := span.SpanContext().TraceID().String()
	if len(traces) != 2 || traces[0] != "" || traces[1] != want {
		t.Fatalf("traces=%v want [\"\" %s]", traces, want)
	}
}

// 新契约只接受规范模型标识，历史名称不再映射到新模型。
func TestHistoricalModelIDIsRejected(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	p, err := New("ltx", server.URL, "token", 4, 24, 42, 1)
	if err != nil {
		t.Fatal(err)
	}
	const old = "VWorld_outfit_video_1.0"
	if _, err := p.GetVideo(context.Background(), old, "job"); err == nil {
		t.Fatal("old model accepted by status lookup")
	}
	if err := p.ReadVideoResult(context.Background(), old, "job", nil); err == nil {
		t.Fatal("old model accepted by download")
	}
	if calls.Load() != 0 {
		t.Fatalf("old model reached backend: %d", calls.Load())
	}
}

func decodeGatewayRequest(t *testing.T, r *http.Request) (vmindgateway.Request, []vmindgateway.Image) {
	t.Helper()
	if r.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("content-type=%s", r.Header.Get("Content-Type"))
	}
	var payload vmindgateway.Request
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(payload.Input["images"])
	if err != nil {
		t.Fatal(err)
	}
	var images []vmindgateway.Image
	if err := json.Unmarshal(raw, &images); err != nil {
		t.Fatal(err)
	}
	delete(payload.Input, "images")
	return payload, images
}
