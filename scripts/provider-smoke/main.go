// provider-smoke sends a small set of real supplier calls through this checkout's
// gRPC service. Credentials are read from an external, private provider YAML.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/callledger"
	"github.com/wgdl666/wgModelHub/internal/infra/factory"
	service "github.com/wgdl666/wgModelHub/internal/service/modelhub"
	"github.com/wgdl666/wgModelHub/models"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"gopkg.in/yaml.v3"
)

type httpCapture struct {
	base  http.RoundTripper
	mu    sync.Mutex
	calls []map[string]any
}

func (c *httpCapture) RoundTrip(r *http.Request) (*http.Response, error) {
	start := time.Now()
	res, err := c.base.RoundTrip(r)
	row := map[string]any{"host": r.URL.Host, "path": r.URL.Path, "method": r.Method, "request_bytes": r.ContentLength, "header_ms": time.Since(start).Milliseconds()}
	if res != nil {
		row["status"] = res.StatusCode
		row["request_id"] = res.Header.Get("X-Request-ID")
	}
	if err != nil {
		row["transport_error"] = fmt.Sprintf("%T", err)
	}
	c.mu.Lock()
	c.calls = append(c.calls, row)
	c.mu.Unlock()
	return res, err
}
func (c *httpCapture) take() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows := c.calls
	c.calls = nil
	return rows
}

type ledger struct{ finished chan callledger.Record }

func (*ledger) Record(context.Context, callledger.Record) error { return nil }
func (s *ledger) UpdateResult(_ context.Context, r callledger.Record) error {
	s.finished <- r
	return nil
}
func (*ledger) UpsertByGenerationTask(context.Context, callledger.Record) error {
	return fmt.Errorf("video not part of this smoke")
}
func (*ledger) Delete(context.Context, string) error { return nil }

type objects struct{ root string }

func (o objects) Put(_ context.Context, key, _ string, data []byte) (string, error) {
	path := filepath.Join(o.root, key)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	return "local-smoke://" + key, os.WriteFile(path, data, 0600)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	configFile := flag.String("config", "", "private provider YAML")
	imageFile := flag.String("image", "", "existing JPEG or PNG fixture")
	out := flag.String("out", "", "local result directory")
	selected := flag.String("models", "gpt-5.6-luna,embed-v4.0,human-parser,photoroom-segment,gpt-image-2", "comma-separated models, one call each")
	mode := flag.String("mode", "auto", "auto, text, text-stream, vision, vision-stream, tools, schema")
	flag.Parse()
	switch *mode {
	case "auto", "text", "text-stream", "vision", "vision-stream", "tools", "schema":
	default:
		return fmt.Errorf("unsupported probe mode %q", *mode)
	}
	raw, err := os.ReadFile(*configFile)
	if err != nil {
		return err
	}
	var cfg config.Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	var privateFields map[string]any
	if err := yaml.Unmarshal(raw, &privateFields); err != nil {
		return err
	}
	var secrets []string
	var collect func(map[string]any)
	collect = func(fields map[string]any) {
		for k, v := range fields {
			if nested, ok := v.(map[string]any); ok {
				collect(nested)
			}
			key := strings.ToLower(k)
			if value, ok := v.(string); ok && value != "" && (strings.Contains(key, "key") || strings.Contains(key, "password") || strings.Contains(key, "token") || strings.Contains(key, "secret")) {
				secrets = append(secrets, value)
			}
		}
	}
	collect(privateFields)
	sanitize := func(message string) string {
		for _, secret := range secrets {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
		return message
	}
	imageData, err := os.ReadFile(*imageFile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0700); err != nil {
		return err
	}
	capture := &httpCapture{base: http.DefaultTransport}
	http.DefaultTransport = capture
	sets, err := factory.Build(context.Background(), cfg)
	if err != nil {
		return err
	}
	store := &ledger{finished: make(chan callledger.Record, 16)}
	svc := service.NewWithLedger(config.NewLiveConfig(cfg), sets, nil, store)
	svc.SetObjectStore(objects{root: filepath.Join(*out, "ledger")})
	lis := bufconn.Listen(64 << 20)
	server := grpc.NewServer(grpc.MaxRecvMsgSize(64<<20), grpc.MaxSendMsgSize(64<<20))
	modelhubv2.RegisterModelHubServiceServer(server, svc)
	go server.Serve(lis)
	defer server.Stop()
	conn, err := grpc.NewClient("passthrough:///live-provider-smoke", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(64<<20), grpc.MaxCallSendMsgSize(64<<20)))
	if err != nil {
		return err
	}
	defer conn.Close()
	client := modelhubv2.NewModelHubServiceClient(conn)
	rows := []map[string]any{}
	failed := 0
	for _, model := range strings.Split(*selected, ",") {
		start := time.Now()
		fmt.Println("starting", model)
		part := &modelhubv2.ContentPart{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{MimeType: http.DetectContentType(imageData), Source: &modelhubv2.Media_Data{Data: imageData}}}}
		parts := []*modelhubv2.ContentPart{part}
		isGPTImage := strings.HasPrefix(model, "gpt-image-")
		isImage := isGPTImage || model == models.PhotoroomSegment || model == models.SegmentPersonBria || model == models.SegmentSubjectBria
		isVision := model == models.GPT56Luna
		if isVision || isGPTImage {
			prompt := "Name one visible clothing item. Answer in a short phrase."
			if isImage {
				prompt = "Create a clean studio product photo of the main upper-body clothing visible in the reference. Preserve its colors. Plain white background, no text."
			}
			parts = append([]*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: prompt}}}, parts...)
		}
		if model == models.FaceSim {
			parts = append(parts, part)
		}
		request := &modelhubv2.GenerateRequest{Model: model, Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Role: modelhubv2.Role_ROLE_USER, Parts: parts}}}}}}
		if isImage {
			spec := &modelhubv2.ImageOutput{}
			if isGPTImage {
				size, quality := "1024x1024", "low"
				spec.ImageSize = &size
				spec.Quality = &quality
			}
			request.Output = &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: spec}}
		} else {
			spec := &modelhubv2.TextOutput{}
			if isVision {
				n := int32(1024)
				spec.MaxOutputTokens = &n
			}
			request.Output = &modelhubv2.OutputSpec{Stream: isVision, Kind: &modelhubv2.OutputSpec_Text{Text: spec}}
		}
		if *mode != "auto" {
			request = textProbeRequest(model, *mode, imageData)
			isImage = false
			isVision = strings.HasPrefix(*mode, "vision")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		stream, callErr := client.Generate(ctx, request)
		finals, images := 0, 0
		events, deltas := 0, 0
		var text strings.Builder
		var toolCalls []*modelhubv2.ToolCall
		imageCount, inputBytes := 0, 0
		for _, part := range request.GetInput().GetItems()[0].GetMessage().GetParts() {
			if m := part.GetImage(); m != nil {
				imageCount++
				inputBytes += len(m.GetData())
			}
		}
		row := map[string]any{"model": model, "case": *mode, "stream": request.GetOutput().GetStream(), "image_parts": imageCount, "input_bytes": inputBytes}
		if imageCount > 0 {
			row["input_sha256"] = fmt.Sprintf("%x", sha256.Sum256(imageData))
		}
		if callErr == nil {
			for {
				event, e := stream.Recv()
				if e == io.EOF {
					break
				}
				if e != nil {
					callErr = e
					break
				}
				if finals != 0 {
					callErr = fmt.Errorf("event received after final")
					break
				}
				events++
				if event.GetFinal() {
					finals++
				} else {
					deltas++
				}
				if event.GetUsage() != nil {
					row["usage"] = event.GetUsage()
				}
				for _, item := range event.GetItems() {
					text.WriteString(item.GetText())
					if tc := item.GetToolCall(); tc != nil {
						toolCalls = append(toolCalls, tc)
					}
					if m := item.GetImage(); m != nil {
						images++
						im, format, e := image.DecodeConfig(bytes.NewReader(m.GetData()))
						if e != nil {
							callErr = e
							continue
						}
						path := filepath.Join(*out, fmt.Sprintf("%s-%d.%s", model, images, format))
						if e := os.WriteFile(path, m.GetData(), 0600); e != nil {
							cancel()
							return e
						}
						row["image"] = map[string]any{"file": filepath.Base(path), "bytes": len(m.GetData()), "mime": m.GetMimeType(), "width": im.Width, "height": im.Height, "sha256": fmt.Sprintf("%x", sha256.Sum256(m.GetData()))}
					}
				}
			}
		}
		cancel()
		row["elapsed_ms"] = time.Since(start).Milliseconds()
		row["finals"] = finals
		row["events"], row["delta_events"] = events, deltas
		row["http"] = capture.take()
		if callErr == nil && (finals != 1 || (isImage && images != 1) || (!isImage && text.Len() == 0 && len(toolCalls) == 0) || (request.GetOutput().GetStream() && deltas == 0)) {
			callErr = fmt.Errorf("invalid final output")
		}
		if callErr == nil && *mode != "auto" {
			callErr = validateTextProbe(*mode, text.String(), toolCalls)
		}
		if len(toolCalls) > 0 {
			row["tool_calls"] = toolCalls
		}
		if callErr == nil && model == models.CohereEmbedV4 {
			var embedding struct {
				Embedding []float64 `json:"embedding"`
			}
			if err := json.Unmarshal([]byte(text.String()), &embedding); err != nil || len(embedding.Embedding) != 1024 {
				callErr = fmt.Errorf("invalid embedding")
			}
			row["embedding_dimensions"] = len(embedding.Embedding)
		}
		if text.Len() > 0 {
			if err := os.WriteFile(filepath.Join(*out, model+".txt"), []byte(text.String()), 0600); err != nil {
				return err
			}
			row["text_bytes"] = text.Len()
			if isVision || *mode != "auto" {
				row["text"] = text.String()
			}
		}
		row["success"] = callErr == nil
		if callErr != nil {
			failed++
			row["error_code"] = status.Code(callErr).String()
			row["error_message"] = sanitize(status.Convert(callErr).Message())
		}
		select {
		case rec := <-store.finished:
			row["ledger_status"] = rec.Status
			row["call_id"] = rec.CallID
		case <-time.After(5 * time.Second):
			row["ledger_status"] = "not_observed"
		}
		rows = append(rows, row)
		report, err := json.MarshalIndent(map[string]any{"transport": "local modified ModelHub gRPC -> real supplier HTTP", "results": rows}, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*out, "results.json"), report, 0600); err != nil {
			return err
		}
		line, _ := json.Marshal(row)
		fmt.Println(string(line))
	}
	if failed != 0 {
		return fmt.Errorf("%d supplier calls failed; see results.json", failed)
	}
	return nil
}
