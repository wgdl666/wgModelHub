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

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
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
	var chunks []*modelhubv2.GenerateEvent
	if err := emitVideoChunks(payload, func(chunk *modelhubv2.GenerateEvent) error {
		chunks = append(chunks, chunk)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	first := chunks[0].GetItems()[0].GetVideo().GetData()
	second := chunks[1].GetItems()[0].GetVideo().GetData()
	if chunks[0].Sequence != 0 || chunks[0].Final || len(first) != protocol.VideoChunkBytes {
		t.Fatalf("first chunk = seq=%d final=%v len=%d", chunks[0].Sequence, chunks[0].Final, len(first))
	}
	if chunks[1].Sequence != 1 || !chunks[1].Final || len(second) != 1024 {
		t.Fatalf("second chunk = seq=%d final=%v len=%d", chunks[1].Sequence, chunks[1].Final, len(second))
	}
}

func TestEmitVideoChunksRejectsEmptyDownload(t *testing.T) {
	err := emitVideoChunks(nil, func(*modelhubv2.GenerateEvent) error {
		t.Fatal("empty download must not emit events")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "0 bytes") {
		t.Fatalf("expected 0-byte invalid response, got %v", err)
	}
}

func TestEmitVideoChunksRejectsEmptyEvenWhenEmitNil(t *testing.T) {
	// emit==nil 不得短路掉 0 字节校验，否则调用方会把空下载当成成功。
	err := emitVideoChunks(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "0 bytes") {
		t.Fatalf("expected 0-byte invalid response with nil emit, got %v", err)
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
		done <- provider.GenerateVideo(ctx, "ltx", &modelhubv2.GenerateRequest{
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
