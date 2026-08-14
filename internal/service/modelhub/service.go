package modelhub

import (
	"context"
	"fmt"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv1 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v1"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/protocol"
)

type Service struct {
	modelhubv1.UnimplementedModelHubServiceServer
	profiles  map[string]config.ProfileConfig
	providers map[string]provider.Set
}

func New(cfg config.Config, providers map[string]provider.Set) *Service {
	return &Service{
		profiles:  cfg.Profiles,
		providers: providers,
	}
}

func (s *Service) GenerateText(ctx context.Context, request *modelhubv1.GenerateTextRequest) (*modelhubv1.GenerateTextResponse, error) {
	ctx, span := telemetry.StartSpan(ctx, "modelhub.GenerateText")
	defer span.End()

	if err := validateTextRequest(request); err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	binding, err := s.resolve(request.GetModelProfile(), config.CapabilityText)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	if binding.set.Text == nil {
		err = provider.Errorf(provider.ErrorConfiguration, "profile %s does not support text", request.GetModelProfile())
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	response, err := binding.set.Text.Generate(ctx, binding.model, request)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	return response, nil
}

func (s *Service) GenerateTextStream(request *modelhubv1.GenerateTextRequest, stream modelhubv1.ModelHubService_GenerateTextStreamServer) error {
	ctx := stream.Context()
	ctx, span := telemetry.StartSpan(ctx, "modelhub.GenerateTextStream")
	defer span.End()

	if err := validateTextRequest(request); err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	binding, err := s.resolve(request.GetModelProfile(), config.CapabilityText)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	if binding.set.Text == nil {
		err = provider.Errorf(provider.ErrorConfiguration, "profile %s does not support text", request.GetModelProfile())
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	var sendErr error
	emit := func(event *modelhubv1.TextStreamEvent) error {
		// completed 由 service 统一发送一次，供应商只能产生增量事件。
		if event != nil && event.GetCompleted() != nil {
			return nil
		}
		sendErr = stream.Send(event)
		return sendErr
	}
	response, err := binding.set.Text.GenerateStream(ctx, binding.model, request, emit)
	if sendErr != nil {
		telemetry.RecordError(ctx, sendErr)
		return sendErr
	}
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	if response == nil {
		response = &modelhubv1.GenerateTextResponse{}
	}
	return stream.Send(&modelhubv1.TextStreamEvent{
		Event: &modelhubv1.TextStreamEvent_Completed{Completed: response},
	})
}

func (s *Service) GenerateImage(ctx context.Context, request *modelhubv1.GenerateImageRequest) (*modelhubv1.GenerateImageResponse, error) {
	ctx, span := telemetry.StartSpan(ctx, "modelhub.GenerateImage")
	defer span.End()

	if err := validateImageRequest(request); err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	binding, err := s.resolve(request.GetModelProfile(), config.CapabilityImage)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	if binding.set.Image == nil {
		err = provider.Errorf(provider.ErrorConfiguration, "profile %s does not support image", request.GetModelProfile())
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	response, err := binding.set.Image.GenerateImage(ctx, binding.model, request)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	if err := validateImageResponse(response); err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	return response, nil
}

func (s *Service) GenerateVideo(request *modelhubv1.GenerateVideoRequest, stream modelhubv1.ModelHubService_GenerateVideoServer) error {
	ctx := stream.Context()
	ctx, span := telemetry.StartSpan(ctx, "modelhub.GenerateVideo")
	defer span.End()

	if request == nil {
		statusErr := provider.ToStatus(provider.New(provider.ErrorInvalidArgument, "video request is required"))
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	if err := validateMedia(request.GetFirstFrame(), provider.ErrorInvalidArgument); err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	binding, err := s.resolve(request.GetModelProfile(), config.CapabilityVideo)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	if binding.set.Video == nil {
		err = provider.Errorf(provider.ErrorConfiguration, "profile %s does not support video", request.GetModelProfile())
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	var sendErr error
	emit := func(chunk *modelhubv1.VideoChunk) error {
		sendErr = stream.Send(chunk)
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
	set   provider.Set
	model string
}

func (s *Service) resolve(profileName, capability string) (binding, error) {
	profileCfg, ok := s.profiles[profileName]
	if !ok {
		return binding{}, provider.Errorf(provider.ErrorInvalidArgument, "unknown model_profile %s", profileName)
	}
	if profileCfg.Capability != capability {
		return binding{}, provider.Errorf(
			provider.ErrorInvalidArgument,
			"profile %s capability %s does not match RPC %s",
			profileName,
			profileCfg.Capability,
			capability,
		)
	}
	providerSet, ok := s.providers[profileCfg.Provider]
	if !ok {
		return binding{}, provider.Errorf(provider.ErrorConfiguration, "profile %s references unknown provider %s", profileName, profileCfg.Provider)
	}
	if profileCfg.Model == "" {
		return binding{}, provider.Errorf(provider.ErrorConfiguration, "profile %s model is required", profileName)
	}
	return binding{set: providerSet, model: profileCfg.Model}, nil
}

// LookupProfile 仅供测试断言路由结果。
func (s *Service) LookupProfile(profileName string) (config.ProfileConfig, error) {
	profileCfg, ok := s.profiles[profileName]
	if !ok {
		return config.ProfileConfig{}, fmt.Errorf("unknown profile %s", profileName)
	}
	return profileCfg, nil
}

// validateTextRequest 在供应商调用前校验所有内联媒体，确保超限请求得到稳定 ErrorInfo，
// 而不是把大对象交给 SDK 后才以供应商私有错误失败。
func validateTextRequest(request *modelhubv1.GenerateTextRequest) error {
	if request == nil {
		return provider.New(provider.ErrorInvalidArgument, "text request is required")
	}
	for _, message := range request.GetMessages() {
		for _, part := range message.GetParts() {
			if err := validateContentPart(part, provider.ErrorInvalidArgument); err != nil {
				return err
			}
		}
	}
	for _, output := range request.GetToolOutputs() {
		for _, image := range output.GetImages() {
			if err := validateMedia(image, provider.ErrorInvalidArgument); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateImageRequest(request *modelhubv1.GenerateImageRequest) error {
	if request == nil {
		return provider.New(provider.ErrorInvalidArgument, "image request is required")
	}
	for _, part := range request.GetParts() {
		if err := validateContentPart(part, provider.ErrorInvalidArgument); err != nil {
			return err
		}
	}
	return nil
}

// validateImageResponse 同样约束供应商输出，避免超大图片突破统一的 64 MiB 消息契约。
func validateImageResponse(response *modelhubv1.GenerateImageResponse) error {
	if response == nil {
		return provider.New(provider.ErrorInvalidResponse, "image provider returned an empty response")
	}
	for _, part := range response.GetParts() {
		if err := validateContentPart(part, provider.ErrorInvalidResponse); err != nil {
			return err
		}
	}
	return nil
}

func validateContentPart(part *modelhubv1.ContentPart, kind provider.ErrorKind) error {
	if part == nil {
		return provider.New(kind, "content part is required")
	}
	switch value := part.GetContent().(type) {
	case *modelhubv1.ContentPart_Text:
		return nil
	case *modelhubv1.ContentPart_Image:
		return validateMedia(value.Image, kind)
	case *modelhubv1.ContentPart_Video:
		return validateMedia(value.Video, kind)
	case *modelhubv1.ContentPart_Audio:
		return validateMedia(value.Audio, kind)
	case *modelhubv1.ContentPart_File:
		return validateMedia(value.File, kind)
	default:
		return provider.New(kind, "content part value is required")
	}
}

func validateMedia(media *modelhubv1.Media, kind provider.ErrorKind) error {
	if media == nil {
		return provider.New(kind, "media is required")
	}
	if media.GetMimeType() == "" {
		return provider.New(kind, "media mime_type is required")
	}
	switch source := media.GetSource().(type) {
	case *modelhubv1.Media_Data:
		if len(source.Data) == 0 {
			return provider.New(kind, "media data is empty")
		}
		if len(source.Data) > protocol.MaxMediaBytes {
			return provider.Errorf(kind, "media exceeds %d bytes", protocol.MaxMediaBytes)
		}
	case *modelhubv1.Media_Uri:
		if source.Uri == "" {
			return provider.New(kind, "media uri is empty")
		}
	default:
		return provider.New(kind, "media source is required")
	}
	return nil
}
