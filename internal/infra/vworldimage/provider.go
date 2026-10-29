// Package vworldimage 承接薇光点亮公网 FLUX 生图：拆衣服、虚拟换衣与体模。
// 仅将统一协议转换为 VMind-Image 业务接口；参数校验、模型配置与裁剪验收由上游负责。
package vworldimage

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
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"github.com/wgdl666/wgModelHub/protocol"
)

const (
	garmentExtractionPath = "/v2/images/garment_extraction"
	productGenerationPath = "/v2/images/product_generation"
	vtonPath              = "/v2/images/vton"
	bodyModelPath         = "/v2/images/body_model"
	requestTimeout        = 900 * time.Second
)

// Provider 只实现 ImageProvider。两个模型共用同一个入口，靠路径与 LoRA 分流。
type Provider struct {
	name     string
	baseURL  string
	username string
	password string
	client   *http.Client
	// resolveBaseURL 每次请求重读入口。路演要把国内公网切到俄亥俄 G6，不能把地址冻在进程启动时。
	resolveBaseURL func() string
}

func New(name, baseURL, username, password string) (*Provider, error) {
	name = strings.TrimSpace(name)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if name == "" {
		return nil, provider.New(provider.ErrorConfiguration, "vworld-image provider name is required")
	}
	if baseURL == "" {
		return nil, provider.New(provider.ErrorConfiguration, "vworld-image base_url is required")
	}
	// 公网入口固定 Basic Auth；半套凭据会让上游误报成匿名失败，难排查。
	if username == "" || password == "" {
		return nil, provider.New(provider.ErrorConfiguration, "vworld-image username and password are required")
	}
	client := telemetry.NewHTTPClient()
	client.Timeout = requestTimeout
	return &Provider{
		name:     name,
		baseURL:  baseURL,
		username: username,
		password: password,
		client:   client,
	}, nil
}

// NewResolving 与 New 相同，但地址在每次请求时通过 resolve 读取。
// 启动时仍用第一次读到的地址做校验；之后配置热更新改 base_url 会在下一次生图生效。
// 用户名和密码保持启动时的值，改凭据仍然要重启。
func NewResolving(name, username, password string, resolve func() string) (*Provider, error) {
	if resolve == nil {
		return nil, provider.New(provider.ErrorConfiguration, "vworld-image base_url resolver is required")
	}
	p, err := New(name, resolve(), username, password)
	if err != nil {
		return nil, err
	}
	p.resolveBaseURL = resolve
	return p, nil
}

func (p *Provider) currentBaseURL() string {
	if p.resolveBaseURL == nil {
		return p.baseURL
	}
	next := strings.TrimRight(strings.TrimSpace(p.resolveBaseURL()), "/")
	// 热更新瞬间读到空地址时继续用启动入口，避免请求打到空 host。
	if next == "" {
		return p.baseURL
	}
	return next
}

// GenerateImage 按真实模型 ID 选择业务接口，不下发推理配置。
func (p *Provider) GenerateImage(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if request == nil {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "generate request is required")
	}
	switch model {
	case models.VWorldWardrobe10:
		return p.generateGarmentExtraction(ctx, request)
	case models.VWorldOutfit10:
		return p.generateVTON(ctx, request)
	case models.VWorldBodyModel10:
		return p.generateBodyModel(ctx, request)
	default:
		return nil, provider.NotAttemptedf(provider.ErrorInvalidArgument, "vworld-image does not serve model %q", model)
	}
}

func (p *Provider) generateGarmentExtraction(ctx context.Context, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	images := provider.ImageMedias(request.GetInput())
	if len(images) < 1 || len(images) > 2 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "VWorld_wardrobe-1.0 requires source image and optionally parser mask")
	}
	source, mime, err := inlineImage(images[0])
	if err != nil {
		return nil, err
	}
	meta, err := parseGarmentMeta(provider.JoinedText(request.GetInput()))
	if err != nil {
		return nil, err
	}
	fields := map[string]string{"garment_name": meta.Name}
	// 拆衣服接口的 garment_role 是可选枚举，空串会被当成非法值拒掉；没给角色就不发这个字段，缺不缺由上游判定。
	if meta.GarmentRole != "" {
		fields["garment_role"] = meta.GarmentRole
	}
	uploads := []imageUpload{{data: source, mimeType: mime, filename: "source.png"}}
	path := productGenerationPath
	// Route by the supplied bbox or parser payload, without inferring roles or checking taxonomy.
	if len(images) == 2 || meta.ParseLabels != nil || meta.BBox != nil {
		path = garmentExtractionPath
		if meta.BBox != nil {
			// 坐标是否归一化、是否越界由上游按 bbox_format 判定；这里只原样转成 JSON 数组。
			bbox, err := json.Marshal(meta.BBox)
			if err != nil {
				return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, "encode bbox", err)
			}
			fields["bbox"] = string(bbox)
			if meta.BBoxFormat != "" {
				fields["bbox_format"] = meta.BBoxFormat
			}
		}
		if meta.ParseLabels != nil {
			labels, err := json.Marshal(meta.ParseLabels)
			if err != nil {
				return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, "encode parse_labels", err)
			}
			fields["parse_labels"] = string(labels)
		}
		if len(images) == 2 {
			mask, mime, err := inlineImage(images[1])
			if err != nil {
				return nil, err
			}
			uploads = append(uploads, imageUpload{field: "parse_mask", filename: "human_parse_labels.png", data: mask, mimeType: mime})
		}
	}
	raw, err := p.doMultipart(ctx, path, fields, uploads)
	if err != nil {
		return nil, err
	}
	parsed, err := decodeImageResponse(raw)
	if err != nil {
		return nil, err
	}
	return imageEvent(parsed.Data)
}

func (p *Provider) generateVTON(ctx context.Context, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	images := provider.ImageMedias(request.GetInput())
	if len(images) != 2 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "VWorld_outfit_1.0 requires person image then garment collage")
	}
	uploads := make([]imageUpload, 0, 2)
	for index, media := range images {
		data, mime, err := inlineImage(media)
		if err != nil {
			return nil, err
		}
		field := "person_image"
		if index == 1 {
			field = "garment_image"
		}
		uploads = append(uploads, imageUpload{field: field, data: data, mimeType: mime})
	}
	// Preserve prompt whitespace; the model service validates without rewriting it.
	fields := map[string]string{"prompt": provider.JoinedText(request.GetInput())}
	raw, err := p.doMultipart(ctx, vtonPath, fields, uploads)
	if err != nil {
		return nil, err
	}
	parsed, err := decodeImageResponse(raw)
	if err != nil {
		return nil, err
	}
	return imageEvent(parsed.Data)
}

func (p *Provider) generateBodyModel(ctx context.Context, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	images := provider.ImageMedias(request.GetInput())
	if len(images) != 1 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "VWorld_bodymodel_1.0 requires exactly one person image")
	}
	gender, err := bodyModelGender(request.GetInput().GetModelTraits())
	if err != nil {
		return nil, err
	}
	data, mime, err := inlineImage(images[0])
	if err != nil {
		return nil, err
	}
	raw, err := p.doMultipart(ctx, bodyModelPath, map[string]string{"gender": gender}, []imageUpload{{data: data, mimeType: mime}})
	if err != nil {
		return nil, err
	}
	parsed, err := decodeImageResponse(raw)
	if err != nil {
		return nil, err
	}
	return imageEvent(parsed.Data)
}

// Only decode the shared traits envelope. The upstream service owns gender validation.
func bodyModelGender(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	var traits struct {
		Gender string `json:"gender"`
	}
	if err := json.Unmarshal([]byte(raw), &traits); err != nil {
		return "", provider.WrapNotAttempted(provider.ErrorInvalidArgument, "model_traits must be a JSON object", err)
	}
	return traits.Gender, nil
}

type imageUpload struct {
	field    string
	filename string
	data     []byte
	mimeType string
}

func (p *Provider) doMultipart(ctx context.Context, path string, fields map[string]string, images []imageUpload) ([]byte, error) {
	// 薇光点亮生图上游不读 W3C traceparent。表单里的 trace_id 才能和账本、Logfire 对上同一次调用。
	// 没有有效 span 时不写空字段，避免上游把空字符串当成一次真实追踪。
	if traceID := telemetry.TraceID(ctx); traceID != "" {
		fields["trace_id"] = traceID
	}
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			_ = writer.Close()
			return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, p.name+" build request failed", err)
		}
	}
	for i, image := range images {
		// 换衣两张图都叫 image。拆衣服的第二张是人体解析标签图，上游只认 parse_mask 这个字段名。
		name := image.field
		if name == "" {
			name = "image"
		}
		filename := image.filename
		if filename == "" {
			filename = referenceFilename(image.mimeType, i)
		}
		partHeader := make(textproto.MIMEHeader)
		partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, name, filename))
		partHeader.Set("Content-Type", image.mimeType)
		part, err := writer.CreatePart(partHeader)
		if err != nil {
			_ = writer.Close()
			return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, p.name+" build request failed", err)
		}
		if _, err := part.Write(image.data); err != nil {
			_ = writer.Close()
			return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, p.name+" build request failed", err)
		}
	}
	contentType := writer.FormDataContentType()
	if err := writer.Close(); err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, p.name+" build request failed", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.currentBaseURL()+path, &buf)
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, p.name+" create request failed", err)
	}
	httpReq.Header.Set("Content-Type", contentType)
	httpReq.SetBasicAuth(p.username, p.password)

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
		return nil, provider.FromHTTPDetail(p.name, resp.StatusCode, string(raw))
	}
	if len(raw) > protocol.MaxRPCMessageBytes {
		return nil, provider.Errorf(provider.ErrorInvalidResponse, "image response exceeds %d bytes", protocol.MaxRPCMessageBytes)
	}
	return raw, nil
}

type garmentMeta struct {
	Name        string            `json:"garment_name"`
	GarmentRole string            `json:"garment_role"`
	ParseLabels map[string]string `json:"parse_labels"`
	// BBox 是人物图上这件衣服的 [x1,y1,x2,y2]。给了 bbox 就走 garment_extraction，不需要解析标签图。
	BBox       []float64 `json:"bbox"`
	BBoxFormat string    `json:"bbox_format"`
}

// Decode the transport envelope only; role/name/label validation belongs to VMind-Image.
func parseGarmentMeta(text string) (garmentMeta, error) {
	var meta garmentMeta
	if err := json.Unmarshal([]byte(text), &meta); err != nil {
		return meta, provider.WrapNotAttempted(provider.ErrorInvalidArgument, "VWorld_wardrobe-1.0 text part must be JSON metadata", err)
	}
	return meta, nil
}

func inlineImage(media *modelhubv2.Media) ([]byte, string, error) {
	if media == nil {
		return nil, "", provider.NotAttempted(provider.ErrorInvalidArgument, "input image is required")
	}
	data, ok := media.Source.(*modelhubv2.Media_Data)
	if !ok || len(data.Data) == 0 {
		return nil, "", provider.NotAttempted(provider.ErrorInvalidArgument, "vworld-image requires inline image bytes")
	}
	if len(data.Data) > protocol.MaxMediaBytes {
		return nil, "", provider.NotAttemptedf(provider.ErrorInvalidArgument, "input image exceeds %d bytes", protocol.MaxMediaBytes)
	}
	mimeType := strings.TrimSpace(media.GetMimeType())
	if mimeType == "" {
		mimeType = "image/png"
	}
	return data.Data, mimeType, nil
}

type imageResponse struct {
	Data []imageDataItem `json:"data"`
	Crop *cropInfo       `json:"crop"`
}

type imageDataItem struct {
	B64JSON string `json:"b64_json"`
}

type cropInfo struct {
	Applied bool `json:"applied"`
}

func decodeImageResponse(raw []byte) (*imageResponse, error) {
	var parsed imageResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, "vworld-image decode response", err)
	}
	return &parsed, nil
}

func imageEvent(items []imageDataItem) (*modelhubv2.GenerateEvent, error) {
	event := &modelhubv2.GenerateEvent{Final: true}
	for _, item := range items {
		b64 := strings.TrimSpace(item.B64JSON)
		if b64 == "" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(data) == 0 {
			return nil, provider.New(provider.ErrorInvalidResponse, "vworld-image response b64_json is invalid")
		}
		if len(data) > protocol.MaxMediaBytes {
			return nil, provider.Errorf(provider.ErrorInvalidResponse, "image exceeds %d bytes", protocol.MaxMediaBytes)
		}
		event.Items = append(event.Items, &modelhubv2.OutputItem{Item: &modelhubv2.OutputItem_Image{Image: &modelhubv2.Media{
			MimeType: sniffImageMIME(data),
			Source:   &modelhubv2.Media_Data{Data: data},
		}}})
	}
	if len(event.Items) == 0 {
		return nil, provider.New(provider.ErrorInvalidResponse, "vworld-image response has no b64_json image")
	}
	return event, nil
}

func referenceFilename(mimeType string, index int) string {
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
	}
	return fmt.Sprintf("reference-%d%s", index+1, ext)
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

var _ provider.ImageProvider = (*Provider)(nil)
