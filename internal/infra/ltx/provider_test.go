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
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/protocol"
)

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

func TestProviderRetriesSubmitOn404(t *testing.T) {
	var submitCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/vton" {
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
		if submitCalls.Add(1) == 1 {
			http.Error(writer, "temporary", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]string{"job_id": "job-1"})
	}))
	defer server.Close()

	provider, err := New("ltx", server.URL, "token", 4, 24, 42, 0.001)
	if err != nil {
		t.Fatal(err)
	}
	provider.client = server.Client()

	jobID, err := provider.submit(context.Background(), "ltx", []byte("png"), "prompt", "720p", nil)
	if err != nil {
		t.Fatal(err)
	}
	if submitCalls.Load() != 2 || jobID != "job-1" {
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
	_, err = p.submit(context.Background(), "ltx", []byte("png"), "prompt", "720p", nil)
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
		{"done", provider.VideoJobSucceeded},
		{"error", provider.VideoJobFailed},
		{"processing", provider.VideoJobRunning},
	}
	for _, tc := range tests {
		t.Run(tc.status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]string{"status": tc.status})
			}))
			defer server.Close()
			p, err := New("ltx", server.URL, "", 4, 24, 42, 0.001)
			if err != nil {
				t.Fatal(err)
			}
			p.client = server.Client()
			job, err := p.GetVideo(context.Background(), "ltx", "job-1")
			if err != nil {
				t.Fatal(err)
			}
			if job.State != tc.want {
				t.Fatalf("state=%v want=%v", job.State, tc.want)
			}
		})
	}
}

func TestReadVideoResultResolvesRelativeVideoURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/jobs/job-1":
			_ = json.NewEncoder(writer).Encode(map[string]string{
				"status":    "done",
				"video_url": "/assets/result.mp4",
			})
		case "/assets/result.mp4":
			_, _ = writer.Write([]byte("video-bytes"))
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	}))
	defer server.Close()

	p, err := New("ltx", server.URL, "", 4, 24, 42, 0.001)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	var got []byte
	err = p.ReadVideoResult(context.Background(), "ltx", "job-1", func(ev *modelhubv2.GenerateEvent) error {
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
		case "/jobs/job-1":
			_ = json.NewEncoder(writer).Encode(map[string]string{
				"status":    "done",
				"video_url": serverURL + "/big.mp4",
			})
		case "/big.mp4":
			_, _ = writer.Write(make([]byte, protocol.MaxVideoBytes+1))
		default:
			t.Fatalf("path %s", request.URL.Path)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	p, err := New("ltx", server.URL, "", 4, 24, 42, 0.001)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	err = p.ReadVideoResult(context.Background(), "ltx", "job-1", nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversize error, got %v", err)
	}
}

func TestReadVideoResultAcceptsExactlyMaxVideoSize(t *testing.T) {
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/jobs/job-1":
			_ = json.NewEncoder(writer).Encode(map[string]string{
				"status":    "done",
				"video_url": serverURL + "/exact.mp4",
			})
		case "/exact.mp4":
			_, _ = writer.Write(make([]byte, protocol.MaxVideoBytes))
		default:
			t.Fatalf("path %s", request.URL.Path)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	p, err := New("ltx", server.URL, "", 4, 24, 42, 0.001)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	var total int
	err = p.ReadVideoResult(context.Background(), "ltx", "job-1", func(ev *modelhubv2.GenerateEvent) error {
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
		_ = json.NewEncoder(writer).Encode(map[string]string{"job_id": "job-2"})
	}))
	defer server.Close()

	p, err := New("ltx", server.URL, "", 4, 24, 42, 0.001)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	if _, err := p.submit(context.Background(), "ltx", []byte("frame"), "prompt", "720p", nil); err != nil {
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
		case "/jobs/job-1":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "done", "video_url": serverURL + "/empty.mp4"})
		case "/empty.mp4":
			// zero bytes
		}
	}))
	defer server.Close()
	serverURL = server.URL

	p, _ := New("ltx", server.URL, "", 4, 24, 42, 0.001)
	p.client = server.Client()
	err := p.ReadVideoResult(context.Background(), "ltx", "job-1", nil)
	if err == nil || !strings.Contains(err.Error(), "0 bytes") {
		t.Fatalf("err=%v", err)
	}
}

func TestReadVideoResultStreamsMultipleChunks(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), protocol.VideoChunkBytes+50)
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jobs/job-1":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "done", "video_url": serverURL + "/chunked.mp4"})
		case "/chunked.mp4":
			_, _ = w.Write(payload)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	p, _ := New("ltx", server.URL, "", 4, 24, 42, 0.001)
	p.client = server.Client()
	var chunks int
	err := p.ReadVideoResult(context.Background(), "ltx", "job-1", func(ev *modelhubv2.GenerateEvent) error {
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

func TestSubmitKeepsPerRequestTimingAndZeroSeed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("duration") != "6" || r.FormValue("fps") != "30" || r.FormValue("seed") != "0" {
			t.Errorf("lost video parameters: %v", r.MultipartForm.Value)
		}
		w.Write([]byte(`{"job_id":"timing"}`))
	}))
	defer server.Close()
	p, err := New("ltx", server.URL, "", 4, 24, 42, 1)
	if err != nil {
		t.Fatal(err)
	}
	duration, fps, seed := int32(6), int32(30), int32(0)
	_, err = p.submit(context.Background(), "ltx", []byte("image"), "prompt", "720p", &modelhubv2.VideoOutput{DurationSeconds: &duration, Fps: &fps, Seed: &seed})
	if err != nil {
		t.Fatal(err)
	}
}
