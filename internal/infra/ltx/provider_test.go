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

	provider, err := New("ltx", server.URL, "token", 4, 24, 42, 0.001, 1)
	if err != nil {
		t.Fatal(err)
	}
	provider.client = server.Client()

	jobID, err := provider.submit(context.Background(), "ltx", []byte("png"), "prompt", "720p")
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

	p, err := New("ltx", server.URL, "token", 4, 24, 42, 0.001, 1)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()
	_, err = p.submit(context.Background(), "ltx", []byte("png"), "prompt", "720p")
	if err == nil {
		t.Fatal("expected error")
	}
	if submitCalls.Load() != 1 {
		t.Fatalf("calls=%d want 1 (no 5xx auto-retry)", submitCalls.Load())
	}
}

func TestGenerateVideoHonorsMaxPollTime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vton":
			_ = json.NewEncoder(w).Encode(map[string]string{"job_id": "job-1"})
		case "/jobs/job-1":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "processing"})
		}
	}))
	defer server.Close()

	p, err := New("ltx", server.URL, "", 4, 24, 42, 0.05, 0.15)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	start := time.Now()
	err = p.GenerateVideo(context.Background(), "ltx", &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role: modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{
					{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
						MimeType: "image/png",
						Source:   &modelhubv2.Media_Data{Data: []byte("png")},
					}}},
					{Content: &modelhubv2.ContentPart_Text{Text: "video"}},
				},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{Resolution: "720p"}}},
	}, nil)
	elapsed := time.Since(start)
	if err == nil || provider.Kind(err) != provider.ErrorTimeout {
		t.Fatalf("err=%v kind=%v", err, provider.Kind(err))
	}
	if elapsed > 2*time.Second {
		t.Fatalf("maxPollTime ineffective, elapsed=%v", elapsed)
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
			p, err := New("ltx", server.URL, "", 4, 24, 42, 0.001, 1)
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

	p, err := New("ltx", server.URL, "", 4, 24, 42, 0.001, 1)
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

	p, err := New("ltx", server.URL, "", 4, 24, 42, 0.001, 1)
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

	p, err := New("ltx", server.URL, "", 4, 24, 42, 0.001, 1)
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

func TestGenerateVideoHonorsContextCancel(t *testing.T) {
	block := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/vton":
			_ = json.NewEncoder(writer).Encode(map[string]string{"job_id": "job-1"})
		case "/jobs/job-1":
			_ = json.NewEncoder(writer).Encode(map[string]string{"status": "processing"})
			<-block
		}
	}))
	defer server.Close()

	p, err := New("ltx", server.URL, "", 4, 24, 42, 0.001, 5)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- p.GenerateVideo(ctx, "ltx", &modelhubv2.GenerateRequest{
			Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
				Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role: modelhubv2.Role_ROLE_USER,
					Parts: []*modelhubv2.ContentPart{
						{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
							MimeType: "image/png",
							Source:   &modelhubv2.Media_Data{Data: []byte("png")},
						}}},
						{Content: &modelhubv2.ContentPart_Text{Text: "video"}},
					},
				}},
			}}},
			Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{Resolution: "720p"}}},
		}, func(chunk *modelhubv2.GenerateEvent) error {
			return nil
		})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancel error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("generate video did not stop after cancel")
	}
	close(block)
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

	p, err := New("ltx", server.URL, "", 4, 24, 42, 0.001, 1)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	if _, err := p.submit(context.Background(), "ltx", []byte("frame"), "prompt", "720p"); err != nil {
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

	p, _ := New("ltx", server.URL, "", 4, 24, 42, 0.001, 1)
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

	p, _ := New("ltx", server.URL, "", 4, 24, 42, 0.001, 1)
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
