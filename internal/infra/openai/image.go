package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/protocol"
)

var (
	pixelSizeRe = regexp.MustCompile(`^\d+x\d+$`)
	ratioRe     = regexp.MustCompile(`^(\d+(?:\.\d+)?):(\d+(?:\.\d+)?)$`)
)

var verticalRatios = map[string]struct{}{
	"2:3": {}, "3:4": {}, "4:5": {}, "9:16": {},
}

var horizontalRatios = map[string]struct{}{
	"3:2": {}, "4:3": {}, "5:4": {}, "16:9": {}, "21:9": {},
}

// GenerateImage 走 OpenAI Images API（/v1/images/generations）。
// gpt-image-2 经 OminiLink 中转时必须用这条协议，不能复用 chat/completions，
// 也不能挂到 ominilink_image 的 Gemini generateContent 实例上。
func (p *Provider) GenerateImage(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	body, err := buildImageRequestBody(model, request)
	if err != nil {
		return nil, err
	}
	raw, err := p.doJSON(ctx, p.imagesGenerationsURL(), body)
	if err != nil {
		return nil, err
	}
	var resp imagesGenerationResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, p.name+" image decode failed", err)
	}
	return p.convertImageResponse(ctx, &resp)
}

func (p *Provider) imagesGenerationsURL() string {
	// 文本 OpenAI 实例的 base_url 已带 /v1；OminiLink Gemini 实例常用裸主机。
	// Images API 固定在 /v1/images/generations，缺后缀时补上以免打到错误路径。
	base := strings.TrimRight(p.baseURL, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base + "/images/generations"
}

func buildImageRequestBody(model string, request *modelhubv2.GenerateRequest) (map[string]any, error) {
	prompt := strings.TrimSpace(provider.JoinedText(request.GetInput()))
	if prompt == "" {
		return nil, provider.New(provider.ErrorInvalidArgument, "image prompt is required")
	}
	body := map[string]any{
		"model":  model,
		"prompt": prompt,
		"n":      1,
	}
	// gpt-image-2 把 response_format 判为 unknown_parameter，默认就返回 b64_json。
	if size := imageSize(request.GetOutput().GetImage()); size != "" {
		body["size"] = size
	}
	// OpenAI Images API 没有 Gemini 那种交错 parts，只能把全部参考图放进 image 字段。
	refs := imageReferences(request.GetInput())
	switch len(refs) {
	case 0:
	case 1:
		body["image"] = refs[0]
	default:
		body["image"] = refs
	}
	return body, nil
}

func imageSize(image *modelhubv2.ImageOutput) string {
	if image == nil {
		return ""
	}
	if size := strings.TrimSpace(deref(image.ImageSize)); pixelSizeRe.MatchString(size) {
		return size
	}
	ratio := strings.TrimSpace(strings.ReplaceAll(deref(image.AspectRatio), " ", ""))
	if ratio == "" {
		return ""
	}
	if ratio == "1:1" {
		return "1024x1024"
	}
	if _, ok := verticalRatios[ratio]; ok {
		return "1024x1536"
	}
	if _, ok := horizontalRatios[ratio]; ok {
		return "1536x1024"
	}
	matches := ratioRe.FindStringSubmatch(ratio)
	if len(matches) != 3 {
		return ""
	}
	var width, height float64
	_, _ = fmt.Sscanf(matches[1], "%f", &width)
	_, _ = fmt.Sscanf(matches[2], "%f", &height)
	if width <= 0 || height <= 0 {
		return ""
	}
	if width == height {
		return "1024x1024"
	}
	if width < height {
		return "1024x1536"
	}
	return "1536x1024"
}

func imageReferences(input *modelhubv2.Input) []string {
	var refs []string
	for _, part := range provider.MessageParts(input) {
		media := part.GetImage()
		if media == nil {
			continue
		}
		if url := mediaURL(media); url != "" {
			refs = append(refs, url)
		}
	}
	return refs
}

func (p *Provider) convertImageResponse(ctx context.Context, resp *imagesGenerationResponse) (*modelhubv2.GenerateEvent, error) {
	event := &modelhubv2.GenerateEvent{Final: true}
	if resp == nil {
		return event, nil
	}
	if resp.Usage != nil {
		event.Usage = convertUsage(resp.Usage)
	}
	for _, item := range resp.Data {
		data, err := p.decodeImageItem(ctx, item)
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			if strings.TrimSpace(item.RevisedPrompt) != "" {
				event.Items = append(event.Items, &modelhubv2.OutputItem{Item: &modelhubv2.OutputItem_Text{Text: item.RevisedPrompt}})
			}
			continue
		}
		if len(data) > protocol.MaxMediaBytes {
			return nil, provider.Errorf(provider.ErrorInvalidResponse, "image exceeds %d bytes", protocol.MaxMediaBytes)
		}
		event.Items = append(event.Items, &modelhubv2.OutputItem{Item: &modelhubv2.OutputItem_Image{Image: &modelhubv2.Media{
			MimeType: sniffImageMIME(data),
			Source:   &modelhubv2.Media_Data{Data: data},
		}}})
		if strings.TrimSpace(item.RevisedPrompt) != "" {
			event.Items = append(event.Items, &modelhubv2.OutputItem{Item: &modelhubv2.OutputItem_Text{Text: item.RevisedPrompt}})
		}
	}
	return event, nil
}

func (p *Provider) decodeImageItem(ctx context.Context, item imagesGenerationItem) ([]byte, error) {
	if item.B64JSON != "" {
		data, err := base64.StdEncoding.DecodeString(item.B64JSON)
		if err != nil {
			return nil, provider.Wrap(provider.ErrorInvalidResponse, p.name+" image base64 is invalid", err)
		}
		return data, nil
	}
	if item.URL == "" {
		return nil, nil
	}
	// 衣橱等调用方只消费内联 bytes；供应商若只给 URL，必须在这里拉回，不能把 URI 原样丢给 RPC。
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, item.URL, nil)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, p.name+" image url is invalid", err)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" image download failed", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, provider.FromHTTP(p.name, resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, int64(protocol.MaxMediaBytes)+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" image download read failed", err)
	}
	return data, nil
}

func (p *Provider) doJSON(ctx context.Context, url string, body map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, p.name+" marshal failed", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, p.name+" create request failed", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	resp, err := p.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" request failed", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxRPCMessageBytes+1))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" read failed", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, provider.FromHTTP(p.name, resp.StatusCode)
	}
	if len(raw) > protocol.MaxRPCMessageBytes {
		return nil, provider.Errorf(provider.ErrorInvalidResponse, "image response exceeds %d bytes", protocol.MaxRPCMessageBytes)
	}
	return raw, nil
}

func sniffImageMIME(data []byte) string {
	switch {
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	default:
		return "image/png"
	}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type imagesGenerationResponse struct {
	Data  []imagesGenerationItem `json:"data"`
	Usage *apiUsage              `json:"usage,omitempty"`
}

type imagesGenerationItem struct {
	B64JSON       string `json:"b64_json"`
	URL           string `json:"url"`
	RevisedPrompt string `json:"revised_prompt"`
}

var _ provider.ImageProvider = (*Provider)(nil)
