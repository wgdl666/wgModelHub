package ominilinkvideo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

func testGenRequest(imageURL string) *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Parts: []*modelhubv2.ContentPart{
					{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
						MimeType: "image/png",
						Source:   &modelhubv2.Media_Uri{Uri: imageURL},
					}}},
					{Content: &modelhubv2.ContentPart_Text{Text: "walk"}},
				},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{
			Resolution: "720p",
		}}},
	}
}

func newTestProvider(server *httptest.Server) *Provider {
	p, err := New("ominilink", "sk-test", server.URL, 0)
	if err != nil {
		panic(err)
	}
	p.client = server.Client()
	return p
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

func TestNewUsesDefaultPollWhenZero(t *testing.T) {
	p, err := New("ominilink", "sk-test", "https://example.com", 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.pollInterval != defaultPollInterval {
		t.Fatalf("poll=%v", p.pollInterval)
	}
}

func testInlineImageRequest(data []byte, mime string) *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Parts: []*modelhubv2.ContentPart{
					{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
						MimeType: mime,
						Source:   &modelhubv2.Media_Data{Data: data},
					}}},
					{Content: &modelhubv2.ContentPart_Text{Text: "walk"}},
				},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{Resolution: "720p"}}},
	}
}

func TestGenerateRejectsVideoPart(t *testing.T) {
	p, err := New("ominilink", "sk", "https://example.com", 1)
	if err != nil {
		t.Fatal(err)
	}
	req := &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Parts: []*modelhubv2.ContentPart{
					{Content: &modelhubv2.ContentPart_Video{Video: &modelhubv2.Media{
						Source: &modelhubv2.Media_Uri{Uri: "https://cdn.example/in.mp4"},
					}}},
					{Content: &modelhubv2.ContentPart_Text{Text: "edit"}},
				},
			}},
		}}},
	}
	err = submitGetRead(context.Background(), p, models.KlingV3, req, nil)
	if err == nil || !strings.Contains(err.Error(), "does not accept video") {
		t.Fatalf("err=%v", err)
	}
}

func TestGeneratePollFailed(t *testing.T) {
	const model = models.KlingV3
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"Response":{"JobId":"fail-1"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"failed","message":"bad"}`))
	}))
	defer server.Close()

	p := newTestProvider(server)
	err := submitGetRead(context.Background(), p, model, testGenRequest("https://cdn.example/frame.png"), nil)
	if err == nil {
		t.Fatal("expected failure")
	}
}

func TestGetVideoStatusMapping(t *testing.T) {
	const model = models.KlingV3
	tests := []struct {
		status string
		want   provider.VideoJobState
	}{
		{"success", provider.VideoJobSucceeded},
		{"failed", provider.VideoJobFailed},
		{"processing", provider.VideoJobRunning},
	}
	for _, tc := range tests {
		t.Run(tc.status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"status":"` + tc.status + `"}`))
			}))
			defer server.Close()
			p := newTestProvider(server)
			job, err := p.GetVideo(context.Background(), model, "tid")
			if err != nil {
				t.Fatal(err)
			}
			if job.State != tc.want {
				t.Fatalf("state=%v want=%v", job.State, tc.want)
			}
		})
	}
}

func TestInlineBase64ModelValidationBeforeHTTP(t *testing.T) {
	for _, model := range []string{models.Veo31Generate001, models.KlingV3} {
		for _, tc := range []struct {
			data []byte
			mime string
		}{{nil, "image/png"}, {[]byte("image"), ""}, {[]byte("image"), "video/mp4"}} {
			p := &Provider{baseURL: "http://unused.invalid"}
			if _, err := p.SubmitVideo(context.Background(), model, testInlineImageRequest(tc.data, tc.mime)); err == nil || provider.Kind(err) != provider.ErrorInvalidArgument {
				t.Fatalf("model=%s MIME=%s err=%v", model, tc.mime, err)
			}
		}
	}
}
