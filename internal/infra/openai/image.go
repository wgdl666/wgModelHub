package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
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

// GenerateImage 按 Input 是否含参考图分流 OpenAI Images API：
// 无参考图走 /v1/images/generations（JSON），有参考图走 /v1/images/edits（multipart）。
// gpt-image-2 经 OminiLink 中转时不能复用 chat/completions，也不能挂到 Gemini generateContent 实例。
func (p *Provider) GenerateImage(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	prompt := strings.TrimSpace(provider.JoinedText(request.GetInput()))
	if prompt == "" {
		return nil, provider.New(provider.ErrorInvalidArgument, "image prompt is required")
	}
	refs := imageReferenceMedias(request.GetInput())
	var raw []byte
	var err error
	if len(refs) == 0 {
		body := buildImageGenerationsBody(model, prompt, request)
		raw, err = p.doJSON(ctx, p.imagesAPIURL("generations"), body)
	} else {
		raw, err = p.doImageEdits(ctx, model, prompt, request, refs)
	}
	if err != nil {
		return nil, err
	}
	var resp imagesGenerationResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, p.name+" image decode failed", err)
	}
	return p.convertImageResponse(ctx, &resp)
}

func (p *Provider) imagesAPIURL(endpoint string) string {
	// 文本 OpenAI 实例的 base_url 已带 /v1；OminiLink Gemini 实例常用裸主机。
	// Images API 固定在 /v1/images/*，缺后缀时补上以免打到错误路径。
	base := strings.TrimRight(p.baseURL, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base + "/images/" + endpoint
}

func buildImageGenerationsBody(model, prompt string, request *modelhubv2.GenerateRequest) map[string]any {
	body := map[string]any{
		"model":  model,
		"prompt": prompt,
		"n":      1,
	}
	// gpt-image-2 把 response_format 判为 unknown_parameter，默认就返回 b64_json。
	if size := imageSize(request.GetOutput().GetImage()); size != "" {
		body["size"] = size
	}
	return body
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

func imageReferenceMedias(input *modelhubv2.Input) []*modelhubv2.Media {
	var refs []*modelhubv2.Media
	for _, part := range provider.MessageParts(input) {
		if media := part.GetImage(); media != nil {
			refs = append(refs, media)
		}
	}
	return refs
}

// materializeReferenceImage 把参考图物化为 edits 可上传字节与权威 MIME；URI 按 LTX 首帧约定用 p.client 拉取。
func (p *Provider) materializeReferenceImage(ctx context.Context, media *modelhubv2.Media) ([]byte, string, error) {
	switch source := media.Source.(type) {
	case *modelhubv2.Media_Data:
		// 内联 data 的 MIME 由协议必填，不在此处嗅探。
		return source.Data, media.GetMimeType(), nil
	case *modelhubv2.Media_Uri:
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, source.Uri, nil)
		if err != nil {
			return nil, "", provider.Wrap(provider.ErrorInvalidArgument, "create reference image request", err)
		}
		resp, err := p.client.Do(httpReq)
		if err != nil {
			if ctx.Err() != nil {
				return nil, "", ctx.Err()
			}
			return nil, "", provider.Wrap(provider.ErrorUnavailable, p.name+" fetch reference image failed", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, "", provider.FromHTTP(p.name, resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, int64(protocol.MaxMediaBytes)+1))
		if err != nil {
			return nil, "", provider.Wrap(provider.ErrorUnavailable, p.name+" read reference image failed", err)
		}
		if len(data) > protocol.MaxMediaBytes {
			return nil, "", provider.Errorf(provider.ErrorInvalidArgument, "reference image exceeds %d bytes", protocol.MaxMediaBytes)
		}
		mimeType, err := resolveReferenceImageMIME(resp.Header.Get("Content-Type"), data)
		if err != nil {
			return nil, "", err
		}
		return data, mimeType, nil
	default:
		return nil, "", nil
	}
}

// resolveReferenceImageMIME URI 参考图优先信任 HTTP Content-Type；缺失或非 image/* 时用标准库内容嗅探，仍非 image/* 则拒绝。
// 不可信输入不得走 sniffImageMIME（未知内容默认 PNG 仅用于供应商响应兜底）。
func resolveReferenceImageMIME(contentType string, data []byte) (string, error) {
	if mime := normalizedImageMIME(contentType); mime != "" {
		return mime, nil
	}
	if mime := normalizedImageMIME(http.DetectContentType(data)); mime != "" {
		return mime, nil
	}
	return "", provider.New(provider.ErrorInvalidArgument, "reference image MIME is not image/*")
}

func normalizedImageMIME(contentType string) string {
	baseType := strings.TrimSpace(contentType)
	if i := strings.Index(baseType, ";"); i >= 0 {
		baseType = strings.TrimSpace(baseType[:i])
	}
	if strings.HasPrefix(strings.ToLower(baseType), "image/") {
		return baseType
	}
	return ""
}

func (p *Provider) doImageEdits(ctx context.Context, model, prompt string, request *modelhubv2.GenerateRequest, refs []*modelhubv2.Media) ([]byte, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.WriteField("model", model); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, p.name+" build edits request failed", err)
	}
	if err := writer.WriteField("prompt", prompt); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, p.name+" build edits request failed", err)
	}
	if err := writer.WriteField("n", "1"); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, p.name+" build edits request failed", err)
	}
	if size := imageSize(request.GetOutput().GetImage()); size != "" {
		if err := writer.WriteField("size", size); err != nil {
			return nil, provider.Wrap(provider.ErrorInvalidArgument, p.name+" build edits request failed", err)
		}
	}
	for i, media := range refs {
		data, mimeType, err := p.materializeReferenceImage(ctx, media)
		if err != nil {
			_ = writer.Close()
			return nil, err
		}
		if len(data) == 0 {
			_ = writer.Close()
			return nil, provider.New(provider.ErrorInvalidArgument, "reference image has no uploadable content")
		}
		partHeader := make(textproto.MIMEHeader)
		partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image"; filename="%s"`, referenceImageFilename(mimeType, i)))
		partHeader.Set("Content-Type", mimeType)
		part, err := writer.CreatePart(partHeader)
		if err != nil {
			_ = writer.Close()
			return nil, provider.Wrap(provider.ErrorInvalidArgument, p.name+" build edits request failed", err)
		}
		if _, err := part.Write(data); err != nil {
			_ = writer.Close()
			return nil, provider.Wrap(provider.ErrorInvalidArgument, p.name+" build edits request failed", err)
		}
	}
	contentType := writer.FormDataContentType()
	if err := writer.Close(); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, p.name+" build edits request failed", err)
	}
	return p.doImageHTTP(ctx, p.imagesAPIURL("edits"), contentType, &buf)
}

func referenceImageFilename(mimeType string, index int) string {
	// 扩展名只看主类型；multipart Content-Type 仍用调用方原值（可含 charset 等参数）。
	baseType := strings.TrimSpace(mimeType)
	if i := strings.Index(baseType, ";"); i >= 0 {
		baseType = strings.TrimSpace(baseType[:i])
	}
	ext := ".bin"
	switch strings.ToLower(baseType) {
	case "image/png":
		ext = ".png"
	case "image/jpeg", "image/jpg":
		ext = ".jpg"
	case "image/webp":
		ext = ".webp"
	case "image/gif":
		ext = ".gif"
	}
	return fmt.Sprintf("reference-%d%s", index+1, ext)
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
	return p.doImageHTTP(ctx, url, "application/json", bytes.NewReader(buf.Bytes()))
}

// doImageHTTP 统一 generations/edits 的鉴权、状态码分类与响应体大小限制。
func (p *Provider) doImageHTTP(ctx context.Context, url, contentType string, body io.Reader) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, p.name+" create request failed", err)
	}
	httpReq.Header.Set("Content-Type", contentType)
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
