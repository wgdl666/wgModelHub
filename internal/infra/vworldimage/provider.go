// Package vworldimage 承接薇光点亮公网 FLUX 生图：拆衣服与虚拟换衣。
// 与 SeeTacloud 上的 FLUX.2-klein-9B OpenAI Bearer 实例分绑；鉴权、LoRA、固定尺寸，以及
// 有 parse_mask 时的 crop.applied 验收都只在本包，调用方只传 ModelHub 模型 ID 与业务图文。
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
	"strconv"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"github.com/wgdl666/wgModelHub/protocol"
)

const (
	garmentExtractionPath = "/v1/images/garment_extraction"
	editsPath             = "/v1/images/edits"

	// 上游兼容字段：业务能力由路径和 lora_name 决定，不能把 VWorld 模型 ID 原样下发。
	upstreamCompatibleModel = "FLUX.2-klein-9B"
	loraStrength            = "1.0"
	responseFormat          = "b64_json"

	loraClothGen = "cloth_gen"
	loraVTON     = "vton"

	garmentSize = "1024x1024"
	vtonSize    = "768x1024"

	// 同步长请求；文档上限 900s，避免默认 HTTP 客户端过早切断。
	requestTimeout = 900 * time.Second
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

// GenerateImage 按真实模型 ID 选择拆衣服或虚拟换衣；尺寸与 LoRA 固定在供应商内，不受衣橱 AspectRatio 换算影响。
func (p *Provider) GenerateImage(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if request == nil {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "generate request is required")
	}
	switch model {
	case models.VWorldWardrobe10:
		return p.generateGarmentExtraction(ctx, request)
	case models.VWorldOutfit10:
		return p.generateVTON(ctx, request)
	default:
		return nil, provider.NotAttemptedf(provider.ErrorInvalidArgument, "vworld-image does not serve model %q", model)
	}
}

func (p *Provider) generateGarmentExtraction(ctx context.Context, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	images := provider.ImageMedias(request.GetInput())
	// 一张图是录衣原图。两张图是 OOTD：人物裁图在前，整张人体解析标签图在后。不能把单件遮罩当成第二张图。
	if len(images) < 1 || len(images) > 2 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "VWorld_wardrobe-1.0 requires the source image and optionally the human-parser mask")
	}
	imageBytes, mimeType, err := inlineImage(images[0])
	if err != nil {
		return nil, err
	}
	meta, err := parseGarmentMeta(provider.JoinedText(request.GetInput()))
	if err != nil {
		return nil, err
	}
	hasMask := len(images) == 2
	hasLabels := len(meta.ParseLabels) > 0
	if hasMask != hasLabels {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "VWorld_wardrobe-1.0 parse mask and parse_labels must be provided together")
	}
	// use_crop 跟输入走：有 parse_mask 才裁区域；录衣单图是已主体商品图，强制裁切只会 applied=false。
	useCrop := hasMask
	fields := map[string]string{
		"model":           upstreamCompatibleModel,
		"n":               "1",
		"response_format": responseFormat,
		"lora_name":       loraClothGen,
		"lora_strength":   loraStrength,
		"use_crop":        strconv.FormatBool(useCrop),
		"garment_name":    meta.Name,
		"size":            garmentSize,
	}
	// 品类映射不上就省略 garment_role，让上游按名称推断；猜错品类会把拆解裁到错误区域。
	if role := garmentRoleFromCategory(meta.Category); role != "" {
		fields["garment_role"] = role
	}
	uploads := []imageUpload{{data: imageBytes, mimeType: mimeType}}
	if hasMask {
		maskBytes, maskMIME, err := inlineImage(images[1])
		if err != nil {
			return nil, err
		}
		// 字段名和文件名对齐 Muse：人物图走 image，标签图走 parse_mask，类别表走 parse_labels。
		uploads[0].filename = "source.png"
		labels, err := json.Marshal(meta.ParseLabels)
		if err != nil {
			return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, p.name+" encode parse_labels failed", err)
		}
		fields["parse_labels"] = string(labels)
		uploads = append(uploads, imageUpload{
			field:    "parse_mask",
			filename: "human_parse_labels.png",
			data:     maskBytes,
			mimeType: maskMIME,
		})
	}
	raw, err := p.doMultipart(ctx, garmentExtractionPath, fields, uploads)
	if err != nil {
		return nil, err
	}
	parsed, err := decodeImageResponse(raw)
	if err != nil {
		return nil, err
	}
	// 开了裁切却 applied=false 是整图/解析失败回退，不能当拆衣服成功；未请求裁切则不看 applied。
	if useCrop && (parsed.Crop == nil || !parsed.Crop.Applied) {
		return nil, provider.New(provider.ErrorInvalidResponse, p.name+" garment extraction crop.applied is not true")
	}
	return imageEvent(parsed.Data)
}

func (p *Provider) generateVTON(ctx context.Context, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	images := provider.ImageMedias(request.GetInput())
	if len(images) != 2 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "VWorld_outfit_1.0 requires person image then garment collage")
	}
	prompt := strings.TrimSpace(provider.JoinedText(request.GetInput()))
	if prompt == "" {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "VWorld_outfit_1.0 requires a rendered prompt")
	}
	uploads := make([]imageUpload, 0, 2)
	for _, media := range images {
		data, mimeType, err := inlineImage(media)
		if err != nil {
			return nil, err
		}
		uploads = append(uploads, imageUpload{data: data, mimeType: mimeType})
	}
	fields := map[string]string{
		"model":           upstreamCompatibleModel,
		"prompt":          prompt,
		"n":               "1",
		"response_format": responseFormat,
		"lora_name":       loraVTON,
		"lora_strength":   loraStrength,
		// 强制 768x1024；不能沿用 OpenAI 把 3:4/1K 换成 1024x1536 的规则。
		"size": vtonSize,
	}
	raw, err := p.doMultipart(ctx, editsPath, fields, uploads)
	if err != nil {
		return nil, err
	}
	parsed, err := decodeImageResponse(raw)
	if err != nil {
		return nil, err
	}
	return imageEvent(parsed.Data)
}

type imageUpload struct {
	field    string
	filename string
	data     []byte
	mimeType string
}

func (p *Provider) doMultipart(ctx context.Context, path string, fields map[string]string, images []imageUpload) ([]byte, error) {
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
	Name        string
	Category    string
	ParseLabels map[string]string
}

// parseGarmentMeta 只认衣橱为拆衣服模型准备的 JSON；白底中文文案不能进 garment_extraction。
// parse_labels 是类别编号到名称，和标签图一起交给上游裁区域。没有这张图时不能自己补。
func parseGarmentMeta(text string) (garmentMeta, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return garmentMeta{}, provider.NotAttempted(provider.ErrorInvalidArgument, "VWorld_wardrobe-1.0 requires garment_name in text part")
	}
	var payload struct {
		GarmentName     string            `json:"garment_name"`
		GarmentCategory string            `json:"garment_category"`
		ParseLabels     map[string]string `json:"parse_labels"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return garmentMeta{}, provider.NotAttempted(provider.ErrorInvalidArgument, "VWorld_wardrobe-1.0 text part must be JSON with garment_name")
	}
	name := strings.TrimSpace(payload.GarmentName)
	if name == "" {
		return garmentMeta{}, provider.NotAttempted(provider.ErrorInvalidArgument, "VWorld_wardrobe-1.0 garment_name is required")
	}
	return garmentMeta{Name: name, Category: strings.TrimSpace(payload.GarmentCategory), ParseLabels: payload.ParseLabels}, nil
}

// garmentRoleFromCategory 只映射有把握的衣橱品类根/关键词；其余省略以免错误裁剪。
func garmentRoleFromCategory(category string) string {
	c := strings.ToLower(strings.TrimSpace(category))
	if c == "" {
		return ""
	}
	root := c
	if i := strings.Index(c, "."); i >= 0 {
		root = c[:i]
	}
	switch root {
	case "tops", "dresses", "上装", "连衣裙":
		return "clothing::top/full"
	case "bottoms", "下装":
		return "clothing::bottom"
	case "footwear", "鞋":
		return "shoes"
	case "outfit", "整套":
		return "outfit"
	}
	if root == "包" || strings.Contains(c, "bag") {
		return "bag"
	}
	if root == "腰带" || strings.Contains(c, "belt") {
		return "belt"
	}
	return ""
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
