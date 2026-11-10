package vmindgateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/httpbody"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

const decisionPath = "/v1/decision/predict"

// TextProvider 在同一网关连接上按模型分流，Decision 使用图片预测协议，对话继续使用 Chat Completions。
type TextProvider struct {
	gateway *Client
	chat    provider.TextProvider
}

func NewText(gateway *Client, chat provider.TextProvider) *TextProvider {
	return &TextProvider{gateway: gateway, chat: chat}
}

func (p *TextProvider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.VMindDecisionV1 {
		return p.chat.Generate(ctx, model, request)
	}
	result, err := p.gateway.predict(ctx, request.GetInput())
	if err != nil {
		return nil, err
	}
	return provider.TextFinalEvent(result, nil, "", "", nil), nil
}

func (p *TextProvider) GenerateStream(ctx context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	if model != models.VMindDecisionV1 {
		return p.chat.GenerateStream(ctx, model, request, emit)
	}
	result, err := p.gateway.predict(ctx, request.GetInput())
	if err != nil {
		return nil, err
	}
	// 预测只有一个完整的 JSON 结果，流式调用将其作为一条增量发送。
	if emit != nil {
		if err := emit(provider.TextDeltaEvent(result)); err != nil {
			return nil, err
		}
	}
	return provider.MetadataFinalEvent("", "", nil), nil
}

func (c *Client) predict(ctx context.Context, input *modelhubv2.Input) (string, error) {
	images := provider.ImageMedias(input)
	if len(images) != 1 {
		return "", provider.NotAttemptedf(provider.ErrorInvalidArgument, "Decision requires exactly one inline image")
	}
	image, ok := provider.InlineImageBytes(images[0])
	if !ok {
		return "", provider.NotAttemptedf(provider.ErrorInvalidArgument, "Decision requires exactly one inline image")
	}

	// 预设任务和选项是模型参数，只有自定义问题来自文本 part。
	var traits struct {
		Task    string   `json:"task"`
		Options []string `json:"options"`
	}
	if raw := strings.TrimSpace(input.GetModelTraits()); raw != "" {
		if err := json.Unmarshal([]byte(raw), &traits); err != nil {
			return "", provider.WrapNotAttempted(provider.ErrorInvalidArgument, "invalid Decision model_traits", err)
		}
	}
	task := strings.TrimSpace(traits.Task)
	prompt := strings.TrimSpace(provider.UserText(input))
	if (task == "") == (prompt == "") || (task != "" && len(traits.Options) > 0) {
		return "", provider.NotAttemptedf(provider.ErrorInvalidArgument, "Decision requires a preset task or a custom prompt with optional options")
	}

	// Decision 在推理前校验文件扩展名，按图片内容生成文件名，避免依赖调用方填写的 MIME。
	var filename string
	switch http.DetectContentType(image) {
	case "image/jpeg":
		filename = "image.jpg"
	case "image/png":
		filename = "image.png"
	case "image/webp":
		filename = "image.webp"
	default:
		return "", provider.NotAttemptedf(provider.ErrorInvalidArgument, "Decision requires a JPEG, PNG or WebP image")
	}

	parts := []httpbody.Part{httpbody.File("image", filename, "application/octet-stream", image)}
	field, value := "task", task
	if prompt != "" {
		field, value = "prompt", prompt
	}
	parts = append(parts, httpbody.Field(field, value))
	if len(traits.Options) > 0 {
		options, _ := json.Marshal(traits.Options)
		parts = append(parts, httpbody.Field("options", string(options)))
	}
	body, err := httpbody.Multipart(parts)
	if err != nil {
		return "", provider.WrapNotAttempted(provider.ErrorUnavailable, "build Decision image field", err)
	}
	req, err := body.NewRequest(ctx, http.MethodPost, c.baseURL+decisionPath)
	if err != nil {
		return "", provider.WrapNotAttempted(provider.ErrorConfiguration, c.name+" create Decision request", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", provider.Wrap(provider.ErrorUnavailable, c.name+" Decision call failed", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", provider.TakeHTTPError(c.name, res.StatusCode, res.Body)
	}

	// 验证分类字段后保留原始 JSON，调用方可以直接读取候选概率及耗时。
	const maxResponse = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	var result struct {
		Prediction struct {
			Label       string   `json:"label"`
			Probability *float64 `json:"probability"`
		} `json:"prediction"`
	}
	if err != nil || len(raw) > maxResponse || json.Unmarshal(raw, &result) != nil ||
		result.Prediction.Label == "" || result.Prediction.Probability == nil ||
		*result.Prediction.Probability < 0 || *result.Prediction.Probability > 1 {
		return "", provider.New(provider.ErrorInvalidResponse, c.name+" returned an invalid Decision prediction")
	}
	return string(raw), nil
}
