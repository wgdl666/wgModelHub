package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

func embeddingConfig(t *testing.T, baseURL, timeout string) config.Config {
	t.Helper()
	field := ""
	if timeout != "" {
		field = fmt.Sprintf("      http_timeout: %q\n", timeout)
	}
	cfg, err := config.ParseAndValidateYAML(fmt.Sprintf(`database: {dsn: "postgres://unused/unused"}
logfire: {token: unused}
providers:
  embedding:
    models: [qwen3-vl-embedding]
    dashscope_embedding:
      base_url: %q
      api_key: unused
%s`, baseURL, field))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func embeddingCall(t *testing.T, ctx context.Context, cfg config.Config) error {
	t.Helper()
	sets, err := Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sets["embedding"].Text.Generate(ctx, models.Qwen3VLEmbedding, &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
			Role: modelhubv2.Role_ROLE_USER,
			Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
				MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte{1, 2, 3}},
			}}}},
		}}}}},
	})
	return err
}

func embeddingResponse() string {
	vector := make([]float64, 1024)
	vector[0] = 0.5
	body, _ := json.Marshal(map[string]any{"output": map[string]any{"embeddings": []any{map[string]any{"embedding": vector}}}})
	return string(body)
}

func TestDashScopeEmbeddingHTTPTimeoutCancelsRealTransport(t *testing.T) {
	for _, stage := range []string{"headers", "body"} {
		t.Run(stage, func(t *testing.T) {
			canceled := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if stage == "body" {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				select {
				case <-r.Context().Done():
					canceled <- struct{}{}
				case <-time.After(250 * time.Millisecond):
					_, _ = io.WriteString(w, embeddingResponse())
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := embeddingCall(t, ctx, embeddingConfig(t, server.URL, "50ms"))
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("configured HTTP timeout must cancel %s wait, got %v", stage, err)
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("HTTP timeout did not cancel upstream request")
			}
		})
	}
}

func TestDashScopeEmbeddingCallerCancellationWins(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-started
		cancel()
	}()
	err := embeddingCall(t, ctx, embeddingConfig(t, server.URL, "120s"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation must win before provider timeout, got %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("caller cancellation did not cancel upstream request")
	}
}

type embeddingRoundTrip func(*http.Request) (*http.Response, error)

func (f embeddingRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDashScopeEmbeddingDefaultAndOverrideKeepWire(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        time.Duration
	}{{"default", "", 20 * time.Second}, {"test_override", "120s", 120 * time.Second}} {
		t.Run(tc.name, func(t *testing.T) {
			original := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = original })
			http.DefaultTransport = embeddingRoundTrip(func(r *http.Request) (*http.Response, error) {
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) < tc.want-time.Second || time.Until(deadline) > tc.want {
					t.Errorf("actual transport deadline=%v, want %v", time.Until(deadline), tc.want)
				}
				if r.Method != "POST" || r.URL.String() != "https://embedding.invalid/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding" || r.Header.Get("Authorization") != "Bearer unused" {
					t.Error("embedding endpoint/auth/method changed")
				}
				body, _ := io.ReadAll(r.Body)
				var got any
				_ = json.Unmarshal(body, &got)
				want := `{"model":"qwen3-vl-embedding","input":{"contents":[{"image":"data:image/png;base64,AQID"}]},"parameters":{"dimension":1024,"output_type":"dense"}}`
				var expected any
				_ = json.Unmarshal([]byte(want), &expected)
				actualJSON, _ := json.Marshal(got)
				expectedJSON, _ := json.Marshal(expected)
				if string(actualJSON) != string(expectedJSON) {
					t.Errorf("original embedding request wire changed")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(embeddingResponse())), Header: make(http.Header)}, nil
			})
			if err := embeddingCall(t, context.Background(), embeddingConfig(t, "https://embedding.invalid/api/v1", tc.value)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
