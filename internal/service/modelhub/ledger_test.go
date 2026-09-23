package modelhub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/callledger"
	"github.com/wgdl666/wgModelHub/internal/infra/openai"
	"github.com/wgdl666/wgModelHub/internal/infra/photoroom"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/internal/taskstore"
	"github.com/wgdl666/wgModelHub/models"
	"github.com/wgdl666/wgModelHub/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func newLedgerService(cfg config.Config, providers map[string]provider.Set, tasks taskstore.Store, ledger callledger.Store) *Service {
	return NewWithLedger(config.NewLiveConfig(cfg), providers, tasks, ledger)
}

func textLedgerCFG(model string) config.Config {
	return config.Config{
		Providers: map[string]config.ProviderConfig{
			"p": {Models: []string{model}, OpenAI: &config.OpenAIProviderConfig{APIKey: "k", BaseURL: "https://x"}},
		},
	}
}

type ledgerTextProvider struct {
	calls  int
	event  *modelhubv2.GenerateEvent
	err    error
	chunks []*modelhubv2.GenerateEvent
	stream bool
}

func (t *ledgerTextProvider) Generate(_ context.Context, _ string, _ *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	t.calls++
	return t.event, t.err
}

func (t *ledgerTextProvider) GenerateStream(_ context.Context, _ string, _ *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	t.calls++
	for _, chunk := range t.chunks {
		if err := emit(chunk); err != nil {
			return nil, err
		}
	}
	return t.event, t.err
}

type ledgerImageProvider struct {
	err   error
	event *modelhubv2.GenerateEvent
}

func (i *ledgerImageProvider) GenerateImage(_ context.Context, _ string, _ *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	return i.event, i.err
}

type sendFailRecorder struct {
	grpc.ServerStream
	ctx context.Context
	err error
}

func (r *sendFailRecorder) Context() context.Context { return r.ctx }
func (r *sendFailRecorder) Send(*modelhubv2.GenerateEvent) error {
	return r.err
}

type slowSendRecorder struct {
	grpc.ServerStream
	ctx   context.Context
	delay time.Duration
}

func (r *slowSendRecorder) Context() context.Context { return r.ctx }
func (r *slowSendRecorder) Send(*modelhubv2.GenerateEvent) error {
	time.Sleep(r.delay)
	return nil
}

func TestLedgerSkipsValidationBeforeProvider(t *testing.T) {
	mem := &callledger.Memory{}
	text := &ledgerTextProvider{event: &modelhubv2.GenerateEvent{Items: []*modelhubv2.OutputItem{{Item: &modelhubv2.OutputItem_Text{Text: "x"}}}}}
	svc := newLedgerService(textLedgerCFG("m"), map[string]provider.Set{"p": {Text: text}}, nil, mem)
	err := svc.Generate(&modelhubv2.GenerateRequest{
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}, &generateRecorder{ctx: context.Background()})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if text.calls != 0 {
		t.Fatalf("provider calls=%d", text.calls)
	}
	if len(mem.Records) != 0 {
		t.Fatalf("ledger records=%d", len(mem.Records))
	}
}

func TestLedgerSkipsProviderLocalRejection(t *testing.T) {
	mem := &callledger.Memory{}
	img := &ledgerImageProvider{err: provider.NotAttempted(provider.ErrorInvalidArgument, "image prompt is required")}
	svc := newLedgerService(textLedgerCFG("m"), map[string]provider.Set{"p": {Image: img}}, nil, mem)
	err := svc.Generate(&modelhubv2.GenerateRequest{
		Model:  "m",
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{ImageSize: strPtr("1K")}}},
	}, &generateRecorder{ctx: context.Background()})
	if err == nil {
		t.Fatal("expected local rejection")
	}
	if len(mem.Records) != 0 {
		t.Fatalf("local rejection must not record, got %d", len(mem.Records))
	}
}

func TestLedgerRecordsStreamSuccessCancelAndProviderFailure(t *testing.T) {
	mem := &callledger.Memory{}
	text := &ledgerTextProvider{
		chunks: []*modelhubv2.GenerateEvent{
			{Items: []*modelhubv2.OutputItem{{Item: &modelhubv2.OutputItem_Text{Text: "hel"}}}},
			{Items: []*modelhubv2.OutputItem{{Item: &modelhubv2.OutputItem_Text{Text: "lo"}}}},
		},
		event: &modelhubv2.GenerateEvent{Usage: &modelhubv2.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}},
	}
	svc := newLedgerService(textLedgerCFG("m"), map[string]provider.Set{"p": {Text: text}}, nil, mem)
	md := metadata.Pairs(protocol.CallerMetadataKey, "wgHub", protocol.BusinessSceneMetadataKey, "agent_chat")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	req := &modelhubv2.GenerateRequest{
		Model: "m",
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
			Role:  modelhubv2.Role_ROLE_USER,
			Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "hi"}}},
		}}}}},
		Output: &modelhubv2.OutputSpec{Stream: true, Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}
	if err := svc.Generate(req, &generateRecorder{ctx: ctx}); err != nil {
		t.Fatal(err)
	}
	if len(mem.Records) != 1 {
		t.Fatalf("records=%d", len(mem.Records))
	}
	rec := mem.Records[0]
	if rec.CallerService != "wgHub" || rec.BusinessScene != "agent_chat" {
		t.Fatalf("caller/scene=%q/%q", rec.CallerService, rec.BusinessScene)
	}
	if rec.Status != callledger.StatusSucceeded || rec.Usage == nil || rec.Usage.TotalTokens != 5 {
		t.Fatalf("rec=%+v", rec)
	}
	texts, _ := rec.OutputPayload["texts"].([]string)
	if len(texts) != 2 || texts[0]+texts[1] != "hello" {
		t.Fatalf("texts=%v", texts)
	}

	memFail := &callledger.Memory{}
	fail := &ledgerTextProvider{err: provider.FromHTTPDetail("p", 500, "boom")}
	svcFail := newLedgerService(textLedgerCFG("m"), map[string]provider.Set{"p": {Text: fail}}, nil, memFail)
	_ = svcFail.Generate(&modelhubv2.GenerateRequest{
		Model:  "m",
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}, &generateRecorder{ctx: context.Background()})
	if len(memFail.Records) != 1 || memFail.Records[0].Status != callledger.StatusFailed {
		t.Fatalf("fail=%v", memFail.Records)
	}
	if memFail.Records[0].Usage != nil {
		t.Fatal("missing usage must stay nil")
	}
	if memFail.Records[0].ErrorCategory != callledger.ErrorCategoryProvider {
		t.Fatalf("category=%q", memFail.Records[0].ErrorCategory)
	}

	memCancel := &callledger.Memory{}
	cancel := &ledgerTextProvider{
		chunks: []*modelhubv2.GenerateEvent{{Items: []*modelhubv2.OutputItem{{Item: &modelhubv2.OutputItem_Text{Text: "partial"}}}}},
		event:  &modelhubv2.GenerateEvent{Usage: &modelhubv2.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}},
		err:    context.Canceled,
	}
	svcCancel := newLedgerService(textLedgerCFG("m"), map[string]provider.Set{"p": {Text: cancel}}, nil, memCancel)
	_ = svcCancel.Generate(&modelhubv2.GenerateRequest{
		Model:  "m",
		Output: &modelhubv2.OutputSpec{Stream: true, Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}, &generateRecorder{ctx: context.Background()})
	if len(memCancel.Records) != 1 || memCancel.Records[0].Status != callledger.StatusCancelled {
		t.Fatalf("cancel=%v", memCancel.Records)
	}
	if memCancel.Records[0].Usage == nil || memCancel.Records[0].Usage.TotalTokens != 2 {
		t.Fatalf("cancel usage=%v", memCancel.Records[0].Usage)
	}
}

func TestLedgerClientSendFailureKeepsUsage(t *testing.T) {
	mem := &callledger.Memory{}
	text := &ledgerTextProvider{event: &modelhubv2.GenerateEvent{
		Items: []*modelhubv2.OutputItem{{Item: &modelhubv2.OutputItem_Text{Text: "ok"}}},
		Usage: &modelhubv2.Usage{InputTokens: 9, OutputTokens: 1, TotalTokens: 10},
	}}
	svc := newLedgerService(textLedgerCFG("m"), map[string]provider.Set{"p": {Text: text}}, nil, mem)
	stream := &sendFailRecorder{ctx: context.Background(), err: errors.New("transport: stream send failed")}
	_ = svc.Generate(&modelhubv2.GenerateRequest{
		Model:  "m",
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}, stream)
	if len(mem.Records) != 1 {
		t.Fatalf("records=%d", len(mem.Records))
	}
	if mem.Records[0].DeliveryStatus != callledger.DeliveryClientSendFailed {
		t.Fatalf("delivery=%q", mem.Records[0].DeliveryStatus)
	}
	if mem.Records[0].Usage == nil || mem.Records[0].Usage.TotalTokens != 10 {
		t.Fatalf("usage lost: %+v", mem.Records[0].Usage)
	}
	if mem.Records[0].Status != callledger.StatusSucceeded {
		t.Fatalf("status=%q", mem.Records[0].Status)
	}
}

func TestLedgerVideoSubmitGetIdempotentOneRecord(t *testing.T) {
	mem := &callledger.Memory{}
	store := newMemoryStore()
	video := &fakeVideo{
		job:    provider.VideoJob{State: provider.VideoJobSucceeded},
		result: []byte("abc"),
	}
	duration := int32(4)
	svc := newLedgerService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ltx": {Models: []string{"ltx"}, LTX: &config.LTXProviderConfig{
				BaseURL: "https://x", Duration: 1, FPS: 1, PollInterval: 1, MaxPollTime: 1,
			}},
		},
	}, map[string]provider.Set{"ltx": {Video: video}}, store, mem)

	aspect := "16:9"
	req := &modelhubv2.SubmitGenerationRequest{
		RequestId: "req-1",
		Request: &modelhubv2.GenerateRequest{
			Model: "ltx",
			Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role:  modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "clip"}}},
			}}}}},
			Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{
				Resolution:      "720p",
				DurationSeconds: &duration,
				AspectRatio:     &aspect,
			}}},
		},
	}
	first, err := svc.SubmitGeneration(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.SubmitGeneration(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.GetTaskId() != second.GetTaskId() {
		t.Fatal("task ids differ")
	}
	if video.submitCount != 1 {
		t.Fatalf("submits=%d", video.submitCount)
	}
	if len(mem.Records) != 1 {
		t.Fatalf("after submit records=%d", len(mem.Records))
	}
	if mem.Records[0].VideoResolution != "720p" || mem.Records[0].Status != callledger.StatusPending {
		t.Fatalf("submit rec=%+v", mem.Records[0])
	}

	for i := 0; i < 2; i++ {
		if err := svc.GetGeneration(&modelhubv2.GetGenerationRequest{TaskId: first.GetTaskId()}, &generationStreamRecorder{ctx: context.Background()}); err != nil {
			t.Fatal(err)
		}
	}
	if len(mem.Records) != 1 {
		t.Fatalf("after get records=%d want 1", len(mem.Records))
	}
	if mem.Records[0].Status != callledger.StatusSucceeded || mem.Records[0].VideoCount == nil || *mem.Records[0].VideoCount != 1 {
		t.Fatalf("final rec=%+v", mem.Records[0])
	}
}

func strPtr(v string) *string { return &v }

func TestLedgerRecordsRealPhotoroomInvalidResponseAfterHTTP(t *testing.T) {
	mem := &callledger.Memory{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-a-png"))
	}))
	t.Cleanup(server.Close)
	img, err := photoroom.New("photoroom_test", "k", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	svc := newLedgerService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"photo": {Models: []string{models.PhotoroomSegment}, Photoroom: &config.PhotoroomProviderConfig{APIKey: "k", BaseURL: server.URL}},
		},
	}, map[string]provider.Set{"photo": {Image: img}}, nil, mem)

	err = svc.Generate(&modelhubv2.GenerateRequest{
		Model: models.PhotoroomSegment,
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
			Role: modelhubv2.Role_ROLE_USER,
			Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
				MimeType: "image/png",
				Source:   &modelhubv2.Media_Data{Data: []byte("\x89PNG\r\n\x1a\n")},
			}}}},
		}}}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
	}, &generateRecorder{ctx: context.Background()})
	if err == nil {
		t.Fatal("expected invalid response")
	}
	if provider.IsNotAttempted(err) {
		t.Fatal("post-HTTP invalid PNG must count as attempted")
	}
	if len(mem.Records) != 1 || mem.Records[0].Status != callledger.StatusFailed {
		t.Fatalf("records=%v", mem.Records)
	}
	if mem.Records[0].ErrorCategory != callledger.ErrorCategoryProvider {
		t.Fatalf("category=%q", mem.Records[0].ErrorCategory)
	}
}

func TestLedgerSkipsRealOpenAIEmptyPromptBeforeHTTP(t *testing.T) {
	mem := &callledger.Memory{}
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	img, err := openai.New("openai_img", "k", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	modelID := models.GPTImage2
	svc := newLedgerService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"oai": {Models: []string{modelID}, OpenAI: &config.OpenAIProviderConfig{APIKey: "k", BaseURL: server.URL}},
		},
	}, map[string]provider.Set{"oai": {Image: img}}, nil, mem)
	err = svc.Generate(&modelhubv2.GenerateRequest{
		Model: modelID,
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
			Role:  modelhubv2.Role_ROLE_USER,
			Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "   "}}},
		}}}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
	}, &generateRecorder{ctx: context.Background()})
	if err == nil {
		t.Fatal("expected empty prompt rejection")
	}
	if hits != 0 {
		t.Fatalf("HTTP hits=%d", hits)
	}
	if len(mem.Records) != 0 {
		t.Fatalf("must not record: %d", len(mem.Records))
	}
}

func TestLedgerStreamKeepsTwoToolsAndMidstreamUsage(t *testing.T) {
	mem := &callledger.Memory{}
	idx0, idx1 := int32(0), int32(1)
	text := &ledgerTextProvider{
		chunks: []*modelhubv2.GenerateEvent{
			{Items: []*modelhubv2.OutputItem{
				{Item: &modelhubv2.OutputItem_ToolCall{ToolCall: &modelhubv2.ToolCall{Id: "a", Name: "search", Index: &idx0, ArgumentsJson: []byte(`{"q":`)}}},
				{Item: &modelhubv2.OutputItem_ToolCall{ToolCall: &modelhubv2.ToolCall{Id: "b", Name: "lookup", Index: &idx1, ArgumentsJson: []byte(`{"id":`)}}},
			}, Usage: &modelhubv2.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}},
			{Items: []*modelhubv2.OutputItem{
				{Item: &modelhubv2.OutputItem_ToolCall{ToolCall: &modelhubv2.ToolCall{Id: "a", Index: &idx0, ArgumentsJson: []byte(`"x"}`)}}},
				{Item: &modelhubv2.OutputItem_ToolCall{ToolCall: &modelhubv2.ToolCall{Id: "b", Index: &idx1, ArgumentsJson: []byte(`2}`)}}},
			}},
		},
		event: &modelhubv2.GenerateEvent{Usage: &modelhubv2.Usage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7}},
		err:   context.Canceled,
	}
	svc := newLedgerService(textLedgerCFG("m"), map[string]provider.Set{"p": {Text: text}}, nil, mem)
	_ = svc.Generate(&modelhubv2.GenerateRequest{
		Model:  "m",
		Output: &modelhubv2.OutputSpec{Stream: true, Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}, &generateRecorder{ctx: context.Background()})
	if len(mem.Records) != 1 {
		t.Fatalf("records=%d", len(mem.Records))
	}
	rec := mem.Records[0]
	if rec.Status != callledger.StatusCancelled {
		t.Fatalf("status=%s", rec.Status)
	}
	if rec.Usage == nil || rec.Usage.TotalTokens != 7 {
		t.Fatalf("must keep latest usage, got %+v", rec.Usage)
	}
	tools, _ := rec.OutputPayload["tool_calls"].([]map[string]any)
	if len(tools) != 2 {
		t.Fatalf("tools=%v", rec.OutputPayload["tool_calls"])
	}
}

func TestLedgerVideoDownloadFailureKeepsSucceeded(t *testing.T) {
	mem := &callledger.Memory{}
	store := newMemoryStore()
	video := &fakeVideo{
		job:       provider.VideoJob{State: provider.VideoJobSucceeded},
		result:    []byte("abc"),
		readError: provider.New(provider.ErrorUnavailable, "download failed"),
	}
	svc := newLedgerService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ltx": {Models: []string{"ltx"}, LTX: &config.LTXProviderConfig{
				BaseURL: "https://x", Duration: 1, FPS: 1, PollInterval: 1, MaxPollTime: 1,
			}},
		},
	}, map[string]provider.Set{"ltx": {Video: video}}, store, mem)
	task, err := svc.SubmitGeneration(context.Background(), &modelhubv2.SubmitGenerationRequest{
		RequestId: "req-dl",
		Request: &modelhubv2.GenerateRequest{
			Model: "ltx",
			Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role:  modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "clip"}}},
			}}}}},
			Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{Resolution: "720p"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = svc.GetGeneration(&modelhubv2.GetGenerationRequest{TaskId: task.GetTaskId()}, &generationStreamRecorder{ctx: context.Background()})
	if len(mem.Records) != 1 {
		t.Fatalf("records=%d", len(mem.Records))
	}
	if mem.Records[0].Status != callledger.StatusSucceeded {
		t.Fatalf("download failure must not regress model status: %+v", mem.Records[0])
	}
	firstLatency := mem.Records[0].LatencyMS
	_ = svc.GetGeneration(&modelhubv2.GetGenerationRequest{TaskId: task.GetTaskId()}, &generationStreamRecorder{ctx: context.Background()})
	if mem.Records[0].LatencyMS != firstLatency {
		t.Fatalf("repeat get extended latency %d -> %d", firstLatency, mem.Records[0].LatencyMS)
	}
}

func TestLedgerSendDelayExcludedFromLatency(t *testing.T) {
	mem := &callledger.Memory{}
	text := &ledgerTextProvider{event: &modelhubv2.GenerateEvent{
		Items: []*modelhubv2.OutputItem{{Item: &modelhubv2.OutputItem_Text{Text: "ok"}}},
		Usage: &modelhubv2.Usage{TotalTokens: 1},
	}}
	svc := newLedgerService(textLedgerCFG("m"), map[string]provider.Set{"p": {Text: text}}, nil, mem)
	const sendDelay = 200 * time.Millisecond
	stream := &slowSendRecorder{ctx: context.Background(), delay: sendDelay}
	if err := svc.Generate(&modelhubv2.GenerateRequest{
		Model:  "m",
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}, stream); err != nil {
		t.Fatal(err)
	}
	if len(mem.Records) != 1 {
		t.Fatalf("records=%d", len(mem.Records))
	}
	// Send 阻塞不得计入最终模型调用耗时。
	if mem.Records[0].LatencyMS >= sendDelay.Milliseconds()/2 {
		t.Fatalf("latency=%d ms includes send delay %v", mem.Records[0].LatencyMS, sendDelay)
	}
}

func TestLedgerImageEventWithErrorKeepsUsage(t *testing.T) {
	mem := &callledger.Memory{}
	img := &ledgerImageProvider{
		event: &modelhubv2.GenerateEvent{
			Items: []*modelhubv2.OutputItem{{Item: &modelhubv2.OutputItem_Image{Image: &modelhubv2.Media{
				MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte("png")},
			}}}},
			Usage: &modelhubv2.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5},
		},
		err: provider.New(provider.ErrorInvalidResponse, "partial image then fail"),
	}
	svc := newLedgerService(textLedgerCFG("m"), map[string]provider.Set{"p": {Image: img}}, nil, mem)
	_ = svc.Generate(&modelhubv2.GenerateRequest{
		Model:  "m",
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
	}, &generateRecorder{ctx: context.Background()})
	if len(mem.Records) != 1 {
		t.Fatalf("records=%d", len(mem.Records))
	}
	rec := mem.Records[0]
	if rec.Status != callledger.StatusFailed {
		t.Fatalf("status=%s", rec.Status)
	}
	if rec.Usage == nil || rec.Usage.TotalTokens != 5 {
		t.Fatalf("must keep event usage on err: %+v", rec.Usage)
	}
	if rec.ImageCount == nil || *rec.ImageCount != 1 {
		t.Fatalf("image_count=%v", rec.ImageCount)
	}
}

func TestLedgerSkipsRequestConstructionFailure(t *testing.T) {
	mem := &callledger.Memory{}
	img := &ledgerImageProvider{err: provider.WrapNotAttempted(provider.ErrorInvalidArgument, "openai create request failed", errors.New("invalid URL"))}
	svc := newLedgerService(textLedgerCFG("m"), map[string]provider.Set{"p": {Image: img}}, nil, mem)
	err := svc.Generate(&modelhubv2.GenerateRequest{
		Model:  "m",
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
	}, &generateRecorder{ctx: context.Background()})
	if err == nil {
		t.Fatal("expected construction error")
	}
	if len(mem.Records) != 0 {
		t.Fatalf("pre-send construction must not record: %d", len(mem.Records))
	}
}

func TestLedgerStreamVideoResultKeepsUsage(t *testing.T) {
	mem := &callledger.Memory{}
	store := newMemoryStore()
	video := &fakeVideoWithUsage{
		fakeVideo: fakeVideo{job: provider.VideoJob{State: provider.VideoJobSucceeded}, result: []byte("abc")},
		usage:     &modelhubv2.Usage{InputTokens: 11, OutputTokens: 22, TotalTokens: 33},
	}
	svc := newLedgerService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ltx": {Models: []string{"ltx"}, LTX: &config.LTXProviderConfig{
				BaseURL: "https://x", Duration: 1, FPS: 1, PollInterval: 1, MaxPollTime: 1,
			}},
		},
	}, map[string]provider.Set{"ltx": {Video: video}}, store, mem)
	task, err := svc.SubmitGeneration(context.Background(), &modelhubv2.SubmitGenerationRequest{
		RequestId: "req-usage",
		Request: &modelhubv2.GenerateRequest{
			Model: "ltx",
			Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role:  modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "clip"}}},
			}}}}},
			Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{Resolution: "720p"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.GetGeneration(&modelhubv2.GetGenerationRequest{TaskId: task.GetTaskId()}, &generationStreamRecorder{ctx: context.Background()}); err != nil {
		t.Fatal(err)
	}
	if len(mem.Records) != 1 {
		t.Fatalf("records=%d", len(mem.Records))
	}
	if mem.Records[0].Usage == nil || mem.Records[0].Usage.TotalTokens != 33 {
		t.Fatalf("streamVideoResult must keep usage: %+v", mem.Records[0].Usage)
	}
}

// fakeVideoWithUsage 在 ReadVideoResult 事件中附带 usage，验证异步交付补写。
type fakeVideoWithUsage struct {
	fakeVideo
	usage *modelhubv2.Usage
}

func (f *fakeVideoWithUsage) ReadVideoResult(ctx context.Context, model, providerTaskID string, emit provider.EmitEvent) error {
	if err := f.fakeVideo.ReadVideoResult(ctx, model, providerTaskID, emit); err != nil {
		return err
	}
	if f.usage != nil {
		return emit(&modelhubv2.GenerateEvent{Usage: f.usage, Final: true})
	}
	return nil
}

func ledgerVideoGenerateReq(model string) *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{
		Model: model,
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
			Role:  modelhubv2.Role_ROLE_USER,
			Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "clip"}}},
		}}}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{Resolution: "720p"}}},
	}
}

func ledgerVideoService(video provider.VideoProvider, ledger callledger.Store) *Service {
	return newLedgerService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ltx": {Models: []string{"ltx"}, LTX: &config.LTXProviderConfig{
				BaseURL: "https://x", Duration: 1, FPS: 1, PollInterval: 1, MaxPollTime: 1,
			}},
		},
	}, map[string]provider.Set{"ltx": {Video: video}}, nil, ledger)
}

// TestLedgerRunVideoJobPostSubmitFailureStillRecords：旧 Generate(video) 走 RunVideoJob，
// Submit 成功后 poll/download 普通失败仍须落一条失败账本，不得被误标 NotAttempted 丢弃。
func TestLedgerRunVideoJobPostSubmitFailureStillRecords(t *testing.T) {
	cases := []struct {
		name  string
		video *fakeVideo
	}{
		{
			name: "poll after submit",
			video: &fakeVideo{
				getErr: provider.Wrap(provider.ErrorInvalidArgument, "create poll request", errors.New("bad poll url")),
			},
		},
		{
			name: "download after submit",
			video: &fakeVideo{
				job:       provider.VideoJob{State: provider.VideoJobSucceeded},
				readError: provider.Wrap(provider.ErrorInvalidArgument, "create download request", errors.New("bad download url")),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mem := &callledger.Memory{}
			svc := ledgerVideoService(tc.video, mem)
			err := svc.Generate(ledgerVideoGenerateReq("ltx"), &generateRecorder{ctx: context.Background()})
			if err == nil {
				t.Fatal("expected post-submit failure")
			}
			if provider.IsNotAttempted(err) {
				t.Fatal("post-submit poll/download must count as attempted")
			}
			if tc.video.submitCount != 1 {
				t.Fatalf("submitCount=%d", tc.video.submitCount)
			}
			if len(mem.Records) != 1 || mem.Records[0].Status != callledger.StatusFailed {
				t.Fatalf("want one failed record, got %v", mem.Records)
			}
		})
	}
}

// TestLedgerRunVideoJobInputPrepCancelSkips：输入准备阶段取消标 NotAttempted，整次未提交生成，不落账本。
func TestLedgerRunVideoJobInputPrepCancelSkips(t *testing.T) {
	mem := &callledger.Memory{}
	video := &fakeVideo{submitErr: provider.AsNotAttempted(context.Canceled)}
	svc := ledgerVideoService(video, mem)
	err := svc.Generate(ledgerVideoGenerateReq("ltx"), &generateRecorder{ctx: context.Background()})
	if err == nil {
		t.Fatal("expected input prep cancel")
	}
	// ToStatus 会剥掉 Local；以 shouldRecord 语义与账本为空为准。
	if svc.shouldRecord(provider.AsNotAttempted(context.Canceled)) {
		t.Fatal("AsNotAttempted cancel must not be recorded")
	}
	if len(mem.Records) != 0 {
		t.Fatalf("input prep cancel must not record: %d", len(mem.Records))
	}
}
