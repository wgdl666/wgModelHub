package modelhub

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wgdl666/kangaroo/logs"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/callledger"
	"github.com/wgdl666/wgModelHub/internal/infra/llmmetric"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/internal/taskstore"
	"github.com/wgdl666/wgModelHub/protocol"
)

type Service struct {
	modelhubv2.UnimplementedModelHubServiceServer
	live        *config.LiveConfig
	providers   map[string]provider.Set
	providerCfg map[string]config.ProviderConfig
	tasks       taskstore.Store
	ledger      callledger.Store
}

func New(live *config.LiveConfig, providers map[string]provider.Set, tasks taskstore.Store) *Service {
	return NewWithLedger(live, providers, tasks, nil)
}

// NewWithLedger 注入调用账本；ledger 可为 nil（不落库），测试可传 Memory。
func NewWithLedger(live *config.LiveConfig, providers map[string]provider.Set, tasks taskstore.Store, ledger callledger.Store) *Service {
	cfg := config.Config{}
	if live != nil {
		cfg = live.Load()
	}
	return &Service{
		live:        live,
		providers:   providers,
		providerCfg: cfg.Providers,
		tasks:       tasks,
		ledger:      ledger,
	}
}

// Generate 按 OutputSpec oneof 选择 text/image/video 能力，并以 request.model（真实供应商模型 ID）路由。
func (s *Service) Generate(request *modelhubv2.GenerateRequest, stream modelhubv2.ModelHubService_GenerateServer) error {
	// TTFT/总耗时起点：ModelHub 收到 Generate；路由失败的请求不进入 LLM 业务指标。
	startedAt := time.Now()
	ctx := stream.Context()
	ctx, span := telemetry.StartSpan(ctx, "modelhub.Generate")
	defer span.End()

	capability, err := capabilityOf(request)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	if err := validateGenerateRequest(request, capability); err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	binding, err := s.resolve(request.GetModel(), capability)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}

	switch capability {
	case config.CapabilityText:
		return s.generateText(ctx, binding, request, stream, startedAt)
	case config.CapabilityImage:
		return s.generateImage(ctx, binding, request, stream)
	case config.CapabilityVideo:
		return s.generateVideo(ctx, binding, request, stream)
	default:
		statusErr := provider.ToStatus(provider.NotAttemptedf(provider.ErrorInvalidArgument, "unsupported capability %s", capability))
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
}

func (s *Service) generateText(ctx context.Context, binding binding, request *modelhubv2.GenerateRequest, stream modelhubv2.ModelHubService_GenerateServer, metricStarted time.Time) (retErr error) {
	if binding.set.Text == nil {
		err := provider.Errorf(provider.ErrorConfiguration, "model %s does not support text", request.GetModel())
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	// StartedAt 在 provider 调用前由 stampCallTiming 写入；此处占位。
	rec := s.baseRecord(ctx, callledger.OperationGenerateText, callledger.CapabilityText, binding.model, binding.provider, time.Time{})
	// 输入快照必须在缓存策略改写请求之前，保留调用方原始参数。
	rec.InputPayload = callledger.BuildGenerateInput(request)
	// 应用文本缓存策略（普通缺省开启 / endpoint-bound Ark 隐式自动）；返回实际生效遥测模式。
	cachingMode := applyTextCachingPolicy(request, s.providerCfg[binding.provider])
	streamMode := request.GetOutput().GetStream()
	labels := llmmetric.Labels{Model: binding.model, Provider: binding.provider, Stream: streamMode}
	// 文本能力已路由成功：真实供应商调用前计一次 request。
	llmmetric.RecordRequest(ctx, labels)
	defer func() {
		labels.Outcome = llmmetric.MapOutcome(retErr)
		llmmetric.RecordTerminal(ctx, labels, time.Since(metricStarted))
	}()

	if streamMode {
		var sendErr error
		var sequence uint32
		var ttftRecorded bool
		acc := callledger.NewTextAccumulator()
		emit := func(event *modelhubv2.GenerateEvent) error {
			// 文本 stream 的唯一 final 由 service 发送；供应商误标 final 的事件只当增量丢弃终态标记。
			if event == nil || event.GetFinal() {
				return nil
			}
			acc.Consume(event)
			applyEventUsage(&rec, event)
			// TTFT：仅流式；空 chunk / 只有 arguments 的 tool delta 不算；一次请求最多一个样本。
			if !ttftRecorded && llmmetric.IsFirstModelOutput(event) {
				llmmetric.RecordTTFT(ctx, labels, time.Since(metricStarted))
				ttftRecorded = true
			}
			event.Sequence = sequence
			sequence++
			sendErr = stream.Send(event)
			return sendErr
		}
		// 流式：provider 执行内同步 emit 的 Send 背压无法从模型耗时拆开，见 receipt。
		// 限流和 5xx 在还没往外吐字时重试；已经发出增量就不能再打，否则客户端会看到两段。
		var emitted bool
		rawEmit := emit
		emit = func(event *modelhubv2.GenerateEvent) error {
			if event != nil && !event.GetFinal() {
				emitted = true
			}
			return rawEmit(event)
		}
		callStarted := time.Now()
		var final *modelhubv2.GenerateEvent
		var err error
		for attempt := 0; attempt <= textTransientRetries; attempt++ {
			if attempt > 0 {
				if waitErr := waitTextTransientRetry(ctx, attempt); waitErr != nil {
					err = waitErr
					break
				}
				sequence = 0
				ttftRecorded = false
				acc = callledger.NewTextAccumulator()
				sendErr = nil
			}
			final, err = binding.set.Text.GenerateStream(ctx, binding.model, request, emit)
			if err == nil || sendErr != nil || emitted || !retryableTextProviderError(err) || attempt == textTransientRetries {
				break
			}
			logs.Default().WarnContext(ctx, "modelhub_text_retry",
				"model", binding.model,
				"provider", binding.provider,
				"attempt", attempt+1,
				"error", err.Error(),
			)
		}
		callFinished := time.Now()
		stampCallTiming(&rec, callStarted, callFinished)
		if final == nil {
			final = &modelhubv2.GenerateEvent{}
		}
		// 取消/失败也保留已知增量与最新 usage；最终 Send 失败不得丢 usage。
		applyEventUsage(&rec, final)
		if rec.Usage == nil {
			_, rec.UsageDetail = callledger.UsageFromProto(nil)
		}
		rec.OutputPayload = callledger.BuildTextOutput(acc, final)
		if sendErr != nil {
			if s.shouldRecord(err) || err == nil {
				s.finishRecord(&rec, err, sendErr)
				callledger.BestEffort(ctx, s.ledger, rec)
			}
			telemetry.RecordError(ctx, sendErr)
			return sendErr
		}
		if err != nil {
			if s.shouldRecord(err) {
				s.finishRecord(&rec, err, nil)
				callledger.BestEffort(ctx, s.ledger, rec)
			}
			statusErr := provider.ToStatus(err)
			telemetry.RecordError(ctx, statusErr)
			return statusErr
		}
		llmmetric.RecordUsageAndCache(ctx, labels, cachingMode, final.GetUsage())
		finalSendErr := stream.Send(&modelhubv2.GenerateEvent{
			Sequence:     sequence,
			Final:        true,
			ResponseId:   final.GetResponseId(),
			FinishReason: final.GetFinishReason(),
			Usage:        final.GetUsage(),
			Safety:       final.GetSafety(),
		})
		s.finishRecord(&rec, nil, finalSendErr)
		callledger.BestEffort(ctx, s.ledger, rec)
		return finalSendErr
	}

	callStarted := time.Now()
	var event *modelhubv2.GenerateEvent
	var err error
	for attempt := 0; attempt <= textTransientRetries; attempt++ {
		if attempt > 0 {
			if waitErr := waitTextTransientRetry(ctx, attempt); waitErr != nil {
				err = waitErr
				break
			}
		}
		event, err = binding.set.Text.Generate(ctx, binding.model, request)
		if err == nil || !retryableTextProviderError(err) || attempt == textTransientRetries {
			break
		}
		logs.Default().WarnContext(ctx, "modelhub_text_retry",
			"model", binding.model,
			"provider", binding.provider,
			"attempt", attempt+1,
			"error", err.Error(),
		)
	}
	callFinished := time.Now()
	stampCallTiming(&rec, callStarted, callFinished)
	if event == nil {
		event = &modelhubv2.GenerateEvent{}
	}
	acc := callledger.NewTextAccumulator()
	acc.Consume(event)
	applyEventUsage(&rec, event)
	if rec.Usage == nil {
		_, rec.UsageDetail = callledger.UsageFromProto(nil)
	}
	rec.OutputPayload = callledger.BuildTextOutput(acc, event)
	if err != nil {
		if s.shouldRecord(err) {
			s.finishRecord(&rec, err, nil)
			callledger.BestEffort(ctx, s.ledger, rec)
		}
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	event.Sequence = 0
	event.Final = true
	llmmetric.RecordUsageAndCache(ctx, labels, cachingMode, event.GetUsage())
	sendErr := stream.Send(event)
	s.finishRecord(&rec, nil, sendErr)
	callledger.BestEffort(ctx, s.ledger, rec)
	return sendErr
}

// textTransientRetries 与视频状态查询同一口径：限流和内部错误再打三次，仍失败才交回调用方。
// 超时先不重试。只覆盖同步文本。视频提交可能已经受理，不走这里。
const textTransientRetries = 3

var textTransientRetryDelay = 300 * time.Millisecond

func retryableTextProviderError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var providerError *provider.Error
	if !errors.As(err, &providerError) {
		return false
	}
	switch providerError.Kind {
	case provider.ErrorRateLimited, provider.ErrorUnavailable:
		return true
	default:
		return false
	}
}

func waitTextTransientRetry(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(attempt) * textTransientRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// CreateCachedContent 将 system+tools 前缀落到支持显式缓存的 TextProvider（当前为 Gemini）。
func (s *Service) CreateCachedContent(ctx context.Context, request *modelhubv2.CreateCachedContentRequest) (*modelhubv2.CreateCachedContentResponse, error) {
	ctx, span := telemetry.StartSpan(ctx, "modelhub.CreateCachedContent")
	defer span.End()
	if request == nil || strings.TrimSpace(request.GetModel()) == "" {
		err := provider.NotAttempted(provider.ErrorInvalidArgument, "model is required")
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	binding, err := s.resolve(request.GetModel(), config.CapabilityText)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	creator, ok := binding.set.Text.(provider.CachedContentCreator)
	if !ok || creator == nil {
		err := provider.Errorf(provider.ErrorConfiguration, "model %s does not support explicit cached content", request.GetModel())
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	resp, err := creator.CreateCachedContent(ctx, binding.model, request)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	return resp, nil
}

// SynthesizeSpeech 同步一次性 TTS：路由到 Speech capability，成功仅表示完整音频已收集。
func (s *Service) SynthesizeSpeech(ctx context.Context, request *modelhubv2.SynthesizeSpeechRequest) (*modelhubv2.SynthesizeSpeechResponse, error) {
	ctx, span := telemetry.StartSpan(ctx, "modelhub.SynthesizeSpeech")
	defer span.End()
	if err := validateSynthesizeSpeechRequest(request); err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	binding, err := s.resolve(request.GetModel(), config.CapabilitySpeech)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	if binding.set.Speech == nil {
		err := provider.Errorf(provider.ErrorConfiguration, "model %s does not support speech", request.GetModel())
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	rec := s.baseRecord(ctx, callledger.OperationSynthesizeSpeech, callledger.CapabilitySpeech, binding.model, binding.provider, time.Time{})
	rec.InputPayload = callledger.BuildSpeechInput(request)
	callStarted := time.Now()
	resp, err := binding.set.Speech.SynthesizeSpeech(ctx, binding.model, request)
	callFinished := time.Now()
	stampCallTiming(&rec, callStarted, callFinished)
	if err != nil {
		if s.shouldRecord(err) {
			s.finishRecord(&rec, err, nil)
			callledger.BestEffort(ctx, s.ledger, rec)
		}
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	if err := validateSpeechResponse(resp); err != nil {
		// 供应商已返回但响应不合约：记为真实调用失败。
		rec.OutputPayload = callledger.BuildSpeechOutput(resp)
		s.finishRecord(&rec, err, nil)
		callledger.BestEffort(ctx, s.ledger, rec)
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	rec.OutputPayload = callledger.BuildSpeechOutput(resp)
	s.finishRecord(&rec, nil, nil)
	callledger.BestEffort(ctx, s.ledger, rec)
	return resp, nil
}

func (s *Service) generateImage(ctx context.Context, binding binding, request *modelhubv2.GenerateRequest, stream modelhubv2.ModelHubService_GenerateServer) error {
	if binding.set.Image == nil {
		err := provider.Errorf(provider.ErrorConfiguration, "model %s does not support image", request.GetModel())
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	rec := s.baseRecord(ctx, callledger.OperationGenerateImage, callledger.CapabilityImage, binding.model, binding.provider, time.Time{})
	rec.InputPayload = callledger.BuildGenerateInput(request)
	rec.ImageSize, rec.ImageAspectRatio = callledger.RequestImageSpec(request)
	callStarted := time.Now()
	event, err := binding.set.Image.GenerateImage(ctx, binding.model, request)
	callFinished := time.Now()
	stampCallTiming(&rec, callStarted, callFinished)
	// event+err 也保留已知输出/usage，不只成功支路。
	if event != nil {
		out, n := callledger.BuildImageOutput(event)
		rec.OutputPayload = out
		rec.ImageCount = intPtr(n)
		applyEventUsage(&rec, event)
	}
	if err != nil {
		if s.shouldRecord(err) {
			if rec.Usage == nil {
				_, rec.UsageDetail = callledger.UsageFromProto(nil)
			}
			s.finishRecord(&rec, err, nil)
			callledger.BestEffort(ctx, s.ledger, rec)
		}
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	if err := validateImageEvent(event); err != nil {
		if rec.Usage == nil {
			_, rec.UsageDetail = callledger.UsageFromProto(nil)
		}
		s.finishRecord(&rec, err, nil)
		callledger.BestEffort(ctx, s.ledger, rec)
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	if rec.Usage == nil {
		_, rec.UsageDetail = callledger.UsageFromProto(nil)
	}
	event.Sequence = 0
	event.Final = true
	sendErr := stream.Send(event)
	s.finishRecord(&rec, nil, sendErr)
	callledger.BestEffort(ctx, s.ledger, rec)
	return sendErr
}

func (s *Service) generateVideo(ctx context.Context, binding binding, request *modelhubv2.GenerateRequest, stream modelhubv2.ModelHubService_GenerateServer) error {
	if binding.set.Video == nil {
		err := provider.Errorf(provider.ErrorConfiguration, "model %s does not support video", request.GetModel())
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	rec := s.baseRecord(ctx, callledger.OperationGenerateVideo, callledger.CapabilityVideo, binding.model, binding.provider, time.Time{})
	rec.InputPayload = callledger.BuildGenerateInput(request)
	resolution, durationSec, aspect := callledger.RequestVideoSpec(request)
	rec.VideoResolution, rec.VideoDurationSec, rec.VideoAspectRatio = resolution, durationSec, aspect
	var sendErr error
	var totalBytes int64
	var chunkCount int
	var mimeType string
	var sawVideo bool
	emit := func(event *modelhubv2.GenerateEvent) error {
		if event != nil {
			applyEventUsage(&rec, event)
			for _, item := range event.GetItems() {
				if video := item.GetVideo(); video != nil {
					sawVideo = true
					mimeType = video.GetMimeType()
					if data := video.GetData(); len(data) > 0 {
						totalBytes += int64(len(data))
						chunkCount++
					}
				}
			}
		}
		sendErr = stream.Send(event)
		return sendErr
	}
	// 流式视频：emit 内 Send 背压计入 provider 耗时，无法拆开。
	callStarted := time.Now()
	err := binding.set.Video.GenerateVideo(ctx, binding.model, request, emit)
	callFinished := time.Now()
	stampCallTiming(&rec, callStarted, callFinished)
	videoCount := 0
	if sawVideo {
		videoCount = 1
	}
	rec.VideoCount = intPtr(videoCount)
	rec.OutputPayload = callledger.BuildVideoOutputSummary(videoCount, totalBytes, mimeType, chunkCount)
	if sendErr != nil {
		if s.shouldRecord(err) || err == nil {
			s.finishRecord(&rec, err, sendErr)
			callledger.BestEffort(ctx, s.ledger, rec)
		}
		telemetry.RecordError(ctx, sendErr)
		return sendErr
	}
	if err != nil {
		if s.shouldRecord(err) {
			s.finishRecord(&rec, err, nil)
			callledger.BestEffort(ctx, s.ledger, rec)
		}
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	s.finishRecord(&rec, nil, nil)
	callledger.BestEffort(ctx, s.ledger, rec)
	return nil
}

type binding struct {
	set provider.Set
	// model 是真实供应商模型 ID，必须全局唯一；完整清单见 models 包，调用方可直接引用。
	model string
	// provider 是配置里的实例名，写入 span 供按供应商对照缓存命中，不是业务别名。
	provider string
}

// resolve 用真实模型 ID 找到唯一 provider，并校验该实例能力与 OutputSpec 一致；model 原样下发供应商。
func (s *Service) resolve(model, capability string) (binding, error) {
	routes := map[string]string(nil)
	if s.live != nil {
		routes = s.live.Load().ModelRoutes()
	}
	providerName, ok := routes[model]
	if !ok {
		return binding{}, provider.NotAttemptedf(provider.ErrorInvalidArgument, "unknown model %s", model)
	}
	providerCfg, ok := s.providerCfg[providerName]
	if !ok {
		return binding{}, provider.Errorf(provider.ErrorConfiguration, "model %s references unknown provider %s", model, providerName)
	}
	if !config.ProviderSupports(providerCfg, capability) {
		return binding{}, provider.Errorf(
			provider.ErrorInvalidArgument,
			"model %s provider %s does not support output %s",
			model,
			providerName,
			capability,
		)
	}
	providerSet, ok := s.providers[providerName]
	if !ok {
		return binding{}, provider.Errorf(provider.ErrorConfiguration, "model %s references unknown provider %s", model, providerName)
	}
	return binding{set: providerSet, model: model, provider: providerName}, nil
}

func capabilityOf(request *modelhubv2.GenerateRequest) (string, error) {
	if request == nil || request.GetOutput() == nil {
		return "", provider.NotAttempted(provider.ErrorInvalidArgument, "output is required")
	}
	switch request.GetOutput().GetKind().(type) {
	case *modelhubv2.OutputSpec_Text:
		return config.CapabilityText, nil
	case *modelhubv2.OutputSpec_Image:
		return config.CapabilityImage, nil
	case *modelhubv2.OutputSpec_Video:
		return config.CapabilityVideo, nil
	default:
		return "", provider.NotAttempted(provider.ErrorInvalidArgument, "output kind is required")
	}
}

func validateGenerateRequest(request *modelhubv2.GenerateRequest, capability string) error {
	if request == nil {
		return provider.NotAttempted(provider.ErrorInvalidArgument, "generate request is required")
	}
	if request.GetModel() == "" {
		return provider.NotAttempted(provider.ErrorInvalidArgument, "model is required")
	}
	if err := validateInput(request.GetInput()); err != nil {
		return err
	}
	if capability == config.CapabilityVideo {
		hasVideo := provider.FirstVideoMedia(request.GetInput()) != nil
		hasImage := provider.FirstImageMedia(request.GetInput()) != nil
		hasText := strings.TrimSpace(provider.JoinedText(request.GetInput())) != ""
		if hasVideo && !hasText {
			return provider.NotAttempted(provider.ErrorInvalidArgument, "video edit prompt text is required in input")
		}
		// 文生视频只有文本；不能在 service 层一律要求首帧，否则 Seedance 2.5 T2V 进不了 provider。
		if !hasVideo && !hasImage && !hasText {
			return provider.NotAttempted(provider.ErrorInvalidArgument, "video prompt text or first_frame image is required in input")
		}
	}
	return nil
}

func validateSynthesizeSpeechRequest(request *modelhubv2.SynthesizeSpeechRequest) error {
	if request == nil {
		return provider.NotAttempted(provider.ErrorInvalidArgument, "synthesize speech request is required")
	}
	if strings.TrimSpace(request.GetModel()) == "" {
		return provider.NotAttempted(provider.ErrorInvalidArgument, "model is required")
	}
	text := strings.TrimSpace(request.GetText())
	if text == "" {
		return provider.NotAttempted(provider.ErrorInvalidArgument, "text is required")
	}
	if utf8.RuneCountInString(text) >= protocol.MaxSpeechTextChars {
		return provider.NotAttemptedf(provider.ErrorInvalidArgument, "text exceeds %d characters", protocol.MaxSpeechTextChars)
	}
	return nil
}

func validateSpeechResponse(resp *modelhubv2.SynthesizeSpeechResponse) error {
	if resp == nil || resp.GetAudio() == nil {
		return provider.New(provider.ErrorInvalidResponse, "speech provider returned an empty response")
	}
	return validateMedia(resp.GetAudio(), provider.ErrorInvalidResponse)
}

func validateInput(input *modelhubv2.Input) error {
	if input == nil {
		return nil
	}
	for _, item := range input.GetItems() {
		switch value := item.GetItem().(type) {
		case *modelhubv2.InputItem_Message:
			for _, part := range value.Message.GetParts() {
				if err := validateContentPart(part, provider.ErrorInvalidArgument); err != nil {
					return err
				}
			}
		case *modelhubv2.InputItem_ToolOutput:
			for _, image := range value.ToolOutput.GetImages() {
				if err := validateMedia(image, provider.ErrorInvalidArgument); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateImageEvent(event *modelhubv2.GenerateEvent) error {
	if event == nil {
		return provider.New(provider.ErrorInvalidResponse, "image provider returned an empty response")
	}
	hasText := false
	hasImage := false
	for _, item := range event.GetItems() {
		switch value := item.GetItem().(type) {
		case *modelhubv2.OutputItem_Text:
			if strings.TrimSpace(value.Text) != "" {
				hasText = true
			}
		case *modelhubv2.OutputItem_Image:
			hasImage = true
			if err := validateMedia(value.Image, provider.ErrorInvalidResponse); err != nil {
				return err
			}
		case *modelhubv2.OutputItem_Video:
			if err := validateMedia(value.Video, provider.ErrorInvalidResponse); err != nil {
				return err
			}
		}
	}
	// 诊断文本或 safety blocked 是合法 final；完全空才算 INVALID_RESPONSE。
	if hasImage || hasText || event.GetSafety().GetBlocked() {
		return nil
	}
	return provider.New(provider.ErrorInvalidResponse, "image provider returned an empty response")
}

func validateContentPart(part *modelhubv2.ContentPart, kind provider.ErrorKind) error {
	if part == nil {
		return provider.New(kind, "content part is required")
	}
	switch value := part.GetContent().(type) {
	case *modelhubv2.ContentPart_Text:
		return nil
	case *modelhubv2.ContentPart_Image:
		return validateMedia(value.Image, kind)
	case *modelhubv2.ContentPart_Video:
		return validateMedia(value.Video, kind)
	case *modelhubv2.ContentPart_Audio:
		return validateMedia(value.Audio, kind)
	case *modelhubv2.ContentPart_File:
		return validateMedia(value.File, kind)
	default:
		return provider.New(kind, "content part value is required")
	}
}

func validateMedia(media *modelhubv2.Media, kind provider.ErrorKind) error {
	if media == nil {
		return provider.New(kind, "media is required")
	}
	switch source := media.GetSource().(type) {
	case *modelhubv2.Media_Data:
		// 内联字节在 service 边界必须声明 MIME，避免 provider 无法构造 data URI 或 multipart。
		if strings.TrimSpace(media.GetMimeType()) == "" {
			return provider.New(kind, "media mime_type is required for inline data")
		}
		if len(source.Data) == 0 {
			return provider.New(kind, "media data is empty")
		}
		if len(source.Data) > protocol.MaxMediaBytes {
			return provider.Errorf(kind, "media exceeds %d bytes", protocol.MaxMediaBytes)
		}
	case *modelhubv2.Media_Uri:
		if strings.TrimSpace(source.Uri) == "" {
			return provider.New(kind, "media uri is empty")
		}
		// URI 的 mime_type 为可选提示；是否必需由下游 provider 协议决定。
	default:
		return provider.New(kind, "media source is required")
	}
	return nil
}
