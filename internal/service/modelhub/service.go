package modelhub

import (
	"context"
	"strings"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/internal/taskstore"
	"github.com/wgdl666/wgModelHub/protocol"
)

type Service struct {
	modelhubv2.UnimplementedModelHubServiceServer
	// modelRoutes：真实模型 ID -> provider 实例名；启动时由配置唯一确定。
	modelRoutes map[string]string
	providers   map[string]provider.Set
	providerCfg map[string]config.ProviderConfig
	// tasks 仅服务 Submit/Get 视频长任务；前台 Generate 不经过此存储。
	tasks taskstore.Store
}

func New(cfg config.Config, providers map[string]provider.Set, tasks taskstore.Store) *Service {
	return &Service{
		modelRoutes: cfg.ModelRoutes(),
		providers:   providers,
		providerCfg: cfg.Providers,
		tasks:       tasks,
	}
}

// Generate 按 OutputSpec oneof 选择 text/image/video 能力，并以 request.model（真实供应商模型 ID）路由。
func (s *Service) Generate(request *modelhubv2.GenerateRequest, stream modelhubv2.ModelHubService_GenerateServer) error {
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
		return s.generateText(ctx, binding, request, stream)
	case config.CapabilityImage:
		return s.generateImage(ctx, binding, request, stream)
	case config.CapabilityVideo:
		return s.generateVideo(ctx, binding, request, stream)
	default:
		statusErr := provider.ToStatus(provider.Errorf(provider.ErrorInvalidArgument, "unsupported capability %s", capability))
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
}

func (s *Service) generateText(ctx context.Context, binding binding, request *modelhubv2.GenerateRequest, stream modelhubv2.ModelHubService_GenerateServer) error {
	if binding.set.Text == nil {
		err := provider.Errorf(provider.ErrorConfiguration, "model %s does not support text", request.GetModel())
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	// 先记下调用方原始配置模式，再填缺省；否则 default_enabled 会被改写后的 enabled=true 掩盖。
	cachingMode := textCachingMode(request.GetInput())
	applyTextCachingDefault(request)
	if request.GetOutput().GetStream() {
		var sendErr error
		var sequence uint32
		emit := func(event *modelhubv2.GenerateEvent) error {
			// 文本 stream 的唯一 final 由 service 发送；供应商误标 final 的事件只当增量丢弃终态标记。
			if event == nil || event.GetFinal() {
				return nil
			}
			event.Sequence = sequence
			sequence++
			sendErr = stream.Send(event)
			return sendErr
		}
		final, err := binding.set.Text.GenerateStream(ctx, binding.model, request, emit)
		if sendErr != nil {
			telemetry.RecordError(ctx, sendErr)
			return sendErr
		}
		if err != nil {
			statusErr := provider.ToStatus(err)
			telemetry.RecordError(ctx, statusErr)
			return statusErr
		}
		if final == nil {
			final = &modelhubv2.GenerateEvent{}
		}
		// 供应商 usage 先落 span，再 Send；客户端最终 Send 失败也不能丢掉已完成打点。
		recordTextGenerateTelemetry(ctx, binding.model, binding.provider, cachingMode, final.GetUsage())
		// 调用方自行累计增量；final 只携带 response_id/finish_reason/usage 等终态元数据。
		return stream.Send(&modelhubv2.GenerateEvent{
			Sequence:     sequence,
			Final:        true,
			ResponseId:   final.GetResponseId(),
			FinishReason: final.GetFinishReason(),
			Usage:        final.GetUsage(),
			Safety:       final.GetSafety(),
		})
	}

	event, err := binding.set.Text.Generate(ctx, binding.model, request)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	if event == nil {
		event = &modelhubv2.GenerateEvent{}
	}
	event.Sequence = 0
	event.Final = true
	recordTextGenerateTelemetry(ctx, binding.model, binding.provider, cachingMode, event.GetUsage())
	return stream.Send(event)
}

// CreateCachedContent 将 system+tools 前缀落到支持显式缓存的 TextProvider（当前为 Gemini）。
func (s *Service) CreateCachedContent(ctx context.Context, request *modelhubv2.CreateCachedContentRequest) (*modelhubv2.CreateCachedContentResponse, error) {
	ctx, span := telemetry.StartSpan(ctx, "modelhub.CreateCachedContent")
	defer span.End()
	if request == nil || strings.TrimSpace(request.GetModel()) == "" {
		err := provider.New(provider.ErrorInvalidArgument, "model is required")
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

func (s *Service) generateImage(ctx context.Context, binding binding, request *modelhubv2.GenerateRequest, stream modelhubv2.ModelHubService_GenerateServer) error {
	if binding.set.Image == nil {
		err := provider.Errorf(provider.ErrorConfiguration, "model %s does not support image", request.GetModel())
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	event, err := binding.set.Image.GenerateImage(ctx, binding.model, request)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	if err := validateImageEvent(event); err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	event.Sequence = 0
	event.Final = true
	return stream.Send(event)
}

func (s *Service) generateVideo(ctx context.Context, binding binding, request *modelhubv2.GenerateRequest, stream modelhubv2.ModelHubService_GenerateServer) error {
	if binding.set.Video == nil {
		err := provider.Errorf(provider.ErrorConfiguration, "model %s does not support video", request.GetModel())
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	var sendErr error
	emit := func(event *modelhubv2.GenerateEvent) error {
		sendErr = stream.Send(event)
		return sendErr
	}
	if err := binding.set.Video.GenerateVideo(ctx, binding.model, request, emit); err != nil {
		if sendErr != nil {
			telemetry.RecordError(ctx, sendErr)
			return sendErr
		}
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
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
	providerName, ok := s.modelRoutes[model]
	if !ok {
		return binding{}, provider.Errorf(provider.ErrorInvalidArgument, "unknown model %s", model)
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
		return "", provider.New(provider.ErrorInvalidArgument, "output is required")
	}
	switch request.GetOutput().GetKind().(type) {
	case *modelhubv2.OutputSpec_Text:
		return config.CapabilityText, nil
	case *modelhubv2.OutputSpec_Image:
		return config.CapabilityImage, nil
	case *modelhubv2.OutputSpec_Video:
		return config.CapabilityVideo, nil
	default:
		return "", provider.New(provider.ErrorInvalidArgument, "output kind is required")
	}
}

func validateGenerateRequest(request *modelhubv2.GenerateRequest, capability string) error {
	if request == nil {
		return provider.New(provider.ErrorInvalidArgument, "generate request is required")
	}
	if request.GetModel() == "" {
		return provider.New(provider.ErrorInvalidArgument, "model is required")
	}
	if err := validateInput(request.GetInput()); err != nil {
		return err
	}
	if capability == config.CapabilityVideo {
		hasVideo := provider.FirstVideoMedia(request.GetInput()) != nil
		hasImage := provider.FirstImageMedia(request.GetInput()) != nil
		hasText := strings.TrimSpace(provider.JoinedText(request.GetInput())) != ""
		if hasVideo && !hasText {
			return provider.New(provider.ErrorInvalidArgument, "video edit prompt text is required in input")
		}
		// 文生视频只有文本；不能在 service 层一律要求首帧，否则 Seedance 2.5 T2V 进不了 provider。
		if !hasVideo && !hasImage && !hasText {
			return provider.New(provider.ErrorInvalidArgument, "video prompt text or first_frame image is required in input")
		}
	}
	return nil
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
