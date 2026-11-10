package modelhub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/callledger"
	"github.com/wgdl666/wgModelHub/internal/infra/factory"
	"github.com/wgdl666/wgModelHub/models"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// These buffers are accessed only by the ordered input/output ledger tasks;
// assertions run after both tasks have exited.
type mediaTransportObjects struct{ uploads [][]byte }

func (o *mediaTransportObjects) Put(_ context.Context, key, _ string, data []byte) (string, error) {
	o.uploads = append(o.uploads, bytes.Clone(data))
	return "s3://test/" + key, nil
}

func TestMediaRequestGRPCRoundTrip(t *testing.T) {
	encodePNG := func(c color.NRGBA) []byte {
		img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
		img.SetNRGBA(0, 0, c)
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	inputPNG := encodePNG(color.NRGBA{R: 255, A: 255})
	outputPNG := encodePNG(color.NRGBA{B: 255, A: 255})
	inputBase64 := base64.StdEncoding.EncodeToString(inputPNG)
	outputBase64 := base64.StdEncoding.EncodeToString(outputPNG)
	const prompt = "识别衣服 <&>"
	const parserResponse = `{"success":true,"labels":{"upper-clothes":5}}`
	for _, tc := range []struct {
		name, model, path, response, wantText string
		image, stream                         bool
	}{
		{name: "GPT_image_multipart", model: models.GPTImage2, path: "/v1/images/edits", image: true, response: `{"data":[{"b64_json":"` + outputBase64 + `"}]}`},
		{name: "segment_base64_form", model: models.SegmentPersonBria, path: "/api/segment", image: true, response: `{"refined_png_b64":"` + outputBase64 + `"}`},
		{name: "parser_base64_JSON", model: models.HumanParse, path: "/human_parser/predict", response: parserResponse, wantText: parserResponse},
		{name: "OpenAI_vision_stream", model: models.GPT56Sol, path: "/v1/chat/completions", stream: true, wantText: "识别成功", response: "data: {\"choices\":[{\"delta\":{\"content\":\"识别成功\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\ndata: [DONE]\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != tc.path {
					t.Errorf("upstream method/path: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(400)
					return
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil || int64(len(raw)) != r.ContentLength {
					t.Errorf("incomplete body: bytes=%d length=%d err=%v", len(raw), r.ContentLength, err)
					w.WriteHeader(400)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(raw))
				if tc.image {
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					defer r.MultipartForm.RemoveAll()
					if tc.model == models.GPTImage2 {
						file, header, err := r.FormFile("image")
						if err != nil {
							t.Error(err)
							w.WriteHeader(400)
							return
						}
						defer file.Close()
						data, err := io.ReadAll(file)
						if err != nil || !bytes.Equal(data, inputPNG) || header.Header.Get("Content-Type") != "image/png" || r.FormValue("prompt") != prompt || r.FormValue("model") != tc.model || r.Header.Get("Authorization") != "Bearer local" {
							t.Error("GPT multipart fields or media changed")
							w.WriteHeader(400)
							return
						}
					} else {
						user, pass, ok := r.BasicAuth()
						if r.FormValue("image") != inputBase64 || r.FormValue("method") != "birefnet" || r.FormValue("output") != "b64" || r.FormValue("preprocess") != "false" || !ok || user != "local" || pass != "local" {
							t.Error("segment form fields or media changed")
							w.WriteHeader(400)
							return
						}
					}
				} else {
					var payload map[string]any
					if err := json.Unmarshal(raw, &payload); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					if tc.stream {
						if payload["model"] != tc.model || payload["stream"] != true || !bytes.Contains(raw, []byte("data:image/png;base64,"+inputBase64)) || r.Header.Get("Authorization") != "Bearer local" {
							t.Error("vision JSON fields or media changed")
							w.WriteHeader(400)
							return
						}
					} else {
						if payload["image"] != inputBase64 {
							t.Error("parser image changed")
							w.WriteHeader(400)
							return
						}
					}
				}
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				if _, err := io.WriteString(w, tc.response); err != nil {
					t.Error(err)
				}
			}))
			defer upstream.Close()
			cfg := config.Config{Providers: map[string]config.ProviderConfig{
				"openai":  {Models: []string{models.GPTImage2, models.GPT56Sol}, OpenAI: &config.OpenAIProviderConfig{BaseURL: upstream.URL + "/v1", APIKey: "local"}},
				"segment": {Models: []string{models.SegmentPersonBria}, SegmentPerson: &config.SegmentPersonProviderConfig{BaseURL: upstream.URL, Username: "local", Password: "local", Method: "birefnet"}},
				"parser":  {Models: []string{models.HumanParse}, HumanParser: &config.HumanParserProviderConfig{BaseURL: upstream.URL}},
			}}
			live := config.NewLiveConfig(cfg)
			sets, err := factory.BuildLive(context.Background(), live)
			if err != nil {
				t.Fatal(err)
			}
			ledger := &callledger.Memory{}
			objects := &mediaTransportObjects{}
			svc := NewWithLedger(live, sets, nil, ledger)
			svc.SetObjectStore(objects)
			var tasks sync.WaitGroup
			svc.ledgerGo = func(fn func()) { tasks.Add(1); go func() { defer tasks.Done(); fn() }() }
			listener := bufconn.Listen(1 << 20)
			server := grpc.NewServer()
			modelhubv2.RegisterModelHubServiceServer(server, svc)
			go func() {
				if err := server.Serve(listener); err != nil {
					t.Error(err)
				}
			}()
			defer server.Stop()
			conn, err := grpc.NewClient("passthrough:///media-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			req := textRequest(tc.model, prompt)
			req.Input.Items[0].GetMessage().Parts = append(req.Input.Items[0].GetMessage().Parts, &modelhubv2.ContentPart{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: inputPNG}}}})
			if tc.image {
				req.Output.Kind = &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}
			}
			req.Output.Stream = tc.stream
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err := modelhubv2.NewModelHubServiceClient(conn).Generate(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			var images [][]byte
			finals := 0
			for {
				event, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if finals != 0 {
					t.Fatal("event received after final")
				}
				if event.GetFinal() {
					finals++
				}
				for _, item := range event.GetItems() {
					text.WriteString(item.GetText())
					if media := item.GetImage(); media != nil {
						if media.GetMimeType() != "image/png" {
							t.Fatalf("MIME=%s", media.GetMimeType())
						}
						images = append(images, media.GetData())
					}
				}
				if tc.stream && event.GetFinal() && event.GetUsage().GetTotalTokens() != 7 {
					t.Fatal("stream usage was lost")
				}
			}
			tasks.Wait()
			if hits.Load() != 1 || finals != 1 || text.String() != tc.wantText {
				t.Fatalf("hits=%d finals=%d text=%s", hits.Load(), finals, text.String())
			}
			wantUploads := 1
			if tc.image {
				wantUploads++
				if len(images) != 1 || !bytes.Equal(images[0], outputPNG) {
					t.Fatal("upstream output PNG was changed before reaching the gRPC client")
				}
			} else if len(images) != 0 {
				t.Fatal("unexpected image output")
			}
			if len(objects.uploads) != wantUploads || !bytes.Equal(objects.uploads[0], inputPNG) {
				t.Fatal("ledger input media was lost")
			}
			if tc.image && !bytes.Equal(objects.uploads[1], outputPNG) {
				t.Fatal("ledger output media was lost")
			}
			if len(ledger.Records) != 1 || ledger.Records[0].Status != callledger.StatusSucceeded || ledger.Records[0].Model != tc.model {
				t.Fatalf("ledger=%v", ledger.Records)
			}
			t.Logf("gRPC → %s → mock upstream → gRPC: PNG/text/final and async ledger verified", tc.path)
		})
	}
}
