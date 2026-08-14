package ltx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	modelhubv1 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v1"
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

func TestProviderResolvesRelativeVideoURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/assets/result.mp4":
			_, _ = writer.Write([]byte("video-bytes"))
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider, err := New("ltx", server.URL, "", 4, 24, 42, 0.001, 1)
	if err != nil {
		t.Fatal(err)
	}
	provider.client = server.Client()

	data, err := provider.download(context.Background(), map[string]any{"video_url": "/assets/result.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "video-bytes" {
		t.Fatalf("video = %q", data)
	}
}

func TestDownloadRejectsOversizedVideo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(make([]byte, protocol.MaxVideoBytes+1))
	}))
	defer server.Close()

	provider, err := New("ltx", server.URL, "", 4, 24, 42, 0.001, 1)
	if err != nil {
		t.Fatal(err)
	}
	provider.client = server.Client()

	_, err = provider.download(context.Background(), map[string]any{"video_url": server.URL + "/big.mp4"})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversize error, got %v", err)
	}
}

func TestDownloadAcceptsExactlyMaxVideoSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(make([]byte, protocol.MaxVideoBytes))
	}))
	defer server.Close()

	provider, err := New("ltx", server.URL, "", 4, 24, 42, 0.001, 1)
	if err != nil {
		t.Fatal(err)
	}
	provider.client = server.Client()

	data, err := provider.download(context.Background(), map[string]any{"video_url": server.URL + "/exact.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != protocol.MaxVideoBytes {
		t.Fatalf("len = %d", len(data))
	}
}

func TestEmitVideoChunksSequenceAndFinal(t *testing.T) {
	payload := make([]byte, protocol.VideoChunkBytes+1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	var chunks []*modelhubv1.VideoChunk
	if err := emitVideoChunks(payload, func(chunk *modelhubv1.VideoChunk) error {
		chunks = append(chunks, chunk)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	if chunks[0].Sequence != 0 || chunks[0].Final || len(chunks[0].Data) != protocol.VideoChunkBytes {
		t.Fatalf("first chunk = seq=%d final=%v len=%d", chunks[0].Sequence, chunks[0].Final, len(chunks[0].Data))
	}
	if chunks[1].Sequence != 1 || !chunks[1].Final || len(chunks[1].Data) != 1024 {
		t.Fatalf("second chunk = seq=%d final=%v len=%d", chunks[1].Sequence, chunks[1].Final, len(chunks[1].Data))
	}
}

func TestGenerateVideoHonorsContextCancel(t *testing.T) {
	block := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/vton":
			_ = json.NewEncoder(writer).Encode(map[string]string{"job_id": "job-1"})
		case "/jobs/job-1":
			<-block
		}
	}))
	defer server.Close()

	provider, err := New("ltx", server.URL, "", 4, 24, 42, 0.001, 5)
	if err != nil {
		t.Fatal(err)
	}
	provider.client = server.Client()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- provider.GenerateVideo(ctx, "ltx", &modelhubv1.GenerateVideoRequest{
			FirstFrame: &modelhubv1.Media{
				MimeType: "image/png",
				Source:   &modelhubv1.Media_Data{Data: []byte("png")},
			},
			Prompt: "video",
		}, func(chunk *modelhubv1.VideoChunk) error {
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

	provider, err := New("ltx", server.URL, "", 4, 24, 42, 0.001, 1)
	if err != nil {
		t.Fatal(err)
	}
	provider.client = server.Client()

	if _, err := provider.submit(context.Background(), "ltx", []byte("frame"), "prompt", "720p"); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] == 0 || bodies[0] != bodies[1] {
		t.Fatalf("bodies = %#v", bodies)
	}
}
