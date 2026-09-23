package callledger

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	// mediaArchiveNote 明确告知读取方：未做对象存储归档，内联字节只保留摘要。
	mediaArchiveNote = "media_bytes_not_archived; summary_only"
	maxErrorMessage  = 1024
)

var protoJSON = protojson.MarshalOptions{
	EmitUnpopulated: false,
	UseProtoNames:   true,
}

// 抹掉 Authorization/Bearer/api_key 后的令牌本体，避免只抹关键字却留下 secret。
var (
	bearerSecretPattern = regexp.MustCompile(`(?i)bearer\s+[^\s,;]+`)
	authHeaderPattern   = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)[^\s,;]+`)
	apiKeySecretPattern = regexp.MustCompile(`(?i)((?:api[_-]?key|token)\s*[:=]\s*)[^\s,;]+`)
)

// BuildGenerateInput 保留文本/工具/参数全文；内联媒体字节替换为 URI/类型/字节数摘要。
func BuildGenerateInput(req *modelhubv2.GenerateRequest) map[string]any {
	if req == nil {
		return map[string]any{}
	}
	cloned, ok := proto.Clone(req).(*modelhubv2.GenerateRequest)
	if !ok || cloned == nil {
		return map[string]any{}
	}
	redactGenerateRequestMedia(cloned)
	out := protoToMap(cloned)
	out["media_archive"] = mediaArchiveNote
	return out
}

// BuildSpeechInput 保存 TTS 文本与音色参数；不含密钥。
func BuildSpeechInput(req *modelhubv2.SynthesizeSpeechRequest) map[string]any {
	if req == nil {
		return map[string]any{}
	}
	return map[string]any{
		"model":    req.GetModel(),
		"text":     req.GetText(),
		"voice_id": req.GetVoiceId(),
	}
}

// TextAccumulator 按原始事件顺序累计文本与工具片段；工具按 index/id 聚合，互不混写。
type TextAccumulator struct {
	ordered []map[string]any
	// toolByKey 指向 ordered 中对应 tool 条目，便于追加 arguments 增量。
	toolByKey map[string]int
}

func NewTextAccumulator() *TextAccumulator {
	return &TextAccumulator{toolByKey: map[string]int{}}
}

func (a *TextAccumulator) Consume(event *modelhubv2.GenerateEvent) {
	if a == nil || event == nil {
		return
	}
	for _, item := range event.GetItems() {
		switch value := item.GetItem().(type) {
		case *modelhubv2.OutputItem_Text:
			if value.Text == "" {
				continue
			}
			a.ordered = append(a.ordered, map[string]any{"type": "text", "text": value.Text})
		case *modelhubv2.OutputItem_ToolCall:
			if value.ToolCall == nil {
				continue
			}
			a.mergeTool(value.ToolCall)
		}
	}
}

func (a *TextAccumulator) mergeTool(call *modelhubv2.ToolCall) {
	key := toolKey(call)
	if idx, ok := a.toolByKey[key]; ok {
		existing := a.ordered[idx]
		if name := call.GetName(); name != "" {
			existing["name"] = name
		}
		if id := call.GetId(); id != "" {
			existing["id"] = id
		}
		if sig := call.GetThoughtSignature(); len(sig) > 0 {
			// 与 protojson bytes 一致：标准 base64，避免非 UTF-8 经 JSON 损坏。
			existing["thought_signature"] = base64.StdEncoding.EncodeToString(sig)
		}
		if call.Index != nil {
			existing["index"] = call.GetIndex()
		}
		if args := call.GetArgumentsJson(); len(args) > 0 {
			prev, _ := existing["arguments_json"].(string)
			existing["arguments_json"] = prev + string(args)
		}
		return
	}
	entry := map[string]any{
		"type":           "tool_call",
		"id":             call.GetId(),
		"name":           call.GetName(),
		"arguments_json": string(call.GetArgumentsJson()),
	}
	if call.Index != nil {
		entry["index"] = call.GetIndex()
	}
	if sig := call.GetThoughtSignature(); len(sig) > 0 {
		// 与 protojson bytes 一致：标准 base64，避免非 UTF-8 经 JSON 损坏。
		entry["thought_signature"] = base64.StdEncoding.EncodeToString(sig)
	}
	a.toolByKey[key] = len(a.ordered)
	a.ordered = append(a.ordered, entry)
}

func toolKey(call *modelhubv2.ToolCall) string {
	if call.Index != nil {
		return fmt.Sprintf("idx:%d", call.GetIndex())
	}
	if id := call.GetId(); id != "" {
		return "id:" + id
	}
	// 无 index/id 时每个片段独立占位，避免两个无名工具被错误合并。
	return fmt.Sprintf("anon:%p", call)
}

// BuildTextOutput 保留有序片段（含完整工具字段）与终态元数据。
func BuildTextOutput(acc *TextAccumulator, final *modelhubv2.GenerateEvent) map[string]any {
	out := map[string]any{
		"items": []any{},
	}
	if acc != nil && len(acc.ordered) > 0 {
		items := make([]any, 0, len(acc.ordered))
		for _, item := range acc.ordered {
			items = append(items, item)
		}
		out["items"] = items
		// 可读聚合：按序抽出文本与完整工具，便于人工浏览且不丢字段。
		var texts []string
		var tools []map[string]any
		for _, item := range acc.ordered {
			switch item["type"] {
			case "text":
				if s, ok := item["text"].(string); ok {
					texts = append(texts, s)
				}
			case "tool_call":
				tools = append(tools, item)
			}
		}
		if len(texts) > 0 {
			out["texts"] = texts
		}
		if len(tools) > 0 {
			out["tool_calls"] = tools
		}
	}
	if final != nil {
		if id := final.GetResponseId(); id != "" {
			out["response_id"] = id
		}
		if reason := final.GetFinishReason(); reason != "" {
			out["finish_reason"] = reason
		}
		if safety := final.GetSafety(); safety != nil {
			out["safety"] = map[string]any{
				"blocked": safety.GetBlocked(),
				"reason":  safety.GetReason(),
				"message": safety.GetMessage(),
			}
		}
	}
	return out
}

// BuildImageOutput 保留诊断文本；图片只存摘要，声明未归档原文件。
func BuildImageOutput(event *modelhubv2.GenerateEvent) (payload map[string]any, imageCount int) {
	payload = map[string]any{
		"media_archive": mediaArchiveNote,
		"items":         []any{},
	}
	if event == nil {
		return payload, 0
	}
	items := make([]any, 0, len(event.GetItems()))
	for _, item := range event.GetItems() {
		switch value := item.GetItem().(type) {
		case *modelhubv2.OutputItem_Text:
			items = append(items, map[string]any{"type": "text", "text": value.Text})
		case *modelhubv2.OutputItem_Image:
			imageCount++
			items = append(items, map[string]any{"type": "image", "media": summarizeMedia(value.Image)})
		case *modelhubv2.OutputItem_Video:
			items = append(items, map[string]any{"type": "video", "media": summarizeMedia(value.Video)})
		case *modelhubv2.OutputItem_ToolCall:
			if value.ToolCall != nil {
				entry := map[string]any{
					"type": "tool_call",
					"tool_call": map[string]any{
						"id":             value.ToolCall.GetId(),
						"name":           value.ToolCall.GetName(),
						"arguments_json": string(value.ToolCall.GetArgumentsJson()),
					},
				}
				if value.ToolCall.Index != nil {
					entry["tool_call"].(map[string]any)["index"] = value.ToolCall.GetIndex()
				}
				if sig := value.ToolCall.GetThoughtSignature(); len(sig) > 0 {
					// 与 protojson bytes 一致：标准 base64，避免非 UTF-8 经 JSON 损坏。
					entry["tool_call"].(map[string]any)["thought_signature"] = base64.StdEncoding.EncodeToString(sig)
				}
				items = append(items, entry)
			}
		}
	}
	payload["items"] = items
	if event.GetSafety() != nil {
		payload["safety"] = map[string]any{
			"blocked": event.GetSafety().GetBlocked(),
			"reason":  event.GetSafety().GetReason(),
			"message": event.GetSafety().GetMessage(),
		}
	}
	return payload, imageCount
}

// BuildVideoOutputSummary 不累计分块正文（可至 200MiB）；只记条数与字节摘要。
func BuildVideoOutputSummary(videoCount int, totalBytes int64, mimeType string, chunkCount int) map[string]any {
	return map[string]any{
		"media_archive": mediaArchiveNote,
		"video_count":   videoCount,
		"total_bytes":   totalBytes,
		"mime_type":     mimeType,
		"chunk_count":   chunkCount,
		"note":          "video chunks not inlined into ledger JSON",
	}
}

// BuildSpeechOutput 音频同样只存摘要。
func BuildSpeechOutput(resp *modelhubv2.SynthesizeSpeechResponse) map[string]any {
	if resp == nil {
		return map[string]any{"media_archive": mediaArchiveNote}
	}
	return map[string]any{
		"media_archive": mediaArchiveNote,
		"audio":         summarizeMedia(resp.GetAudio()),
	}
}

// RequestImageSpec 只取请求侧尺寸与宽高比，不下载解析实际像素；不做不存在的 quality。
func RequestImageSpec(req *modelhubv2.GenerateRequest) (size, aspectRatio string) {
	if req == nil || req.GetOutput() == nil || req.GetOutput().GetImage() == nil {
		return "", ""
	}
	img := req.GetOutput().GetImage()
	return img.GetImageSize(), img.GetAspectRatio()
}

// RequestVideoSpec 只取请求侧分辨率/时长/宽高比。
func RequestVideoSpec(req *modelhubv2.GenerateRequest) (resolution string, durationSec *int, aspectRatio string) {
	if req == nil || req.GetOutput() == nil || req.GetOutput().GetVideo() == nil {
		return "", nil, ""
	}
	video := req.GetOutput().GetVideo()
	resolution = video.GetResolution()
	aspectRatio = video.GetAspectRatio()
	if video.DurationSeconds != nil {
		v := int(video.GetDurationSeconds())
		durationSec = &v
	}
	return resolution, durationSec, aspectRatio
}

// UsageFromProto：Usage 消息整体缺失时返回 nil；出现则保留各字段（含 0）。
func UsageFromProto(usage *modelhubv2.Usage) (*Usage, map[string]any) {
	if usage == nil {
		return nil, map[string]any{"usage_present": false}
	}
	u := &Usage{
		InputTokens:     usage.GetInputTokens(),
		OutputTokens:    usage.GetOutputTokens(),
		TotalTokens:     usage.GetTotalTokens(),
		CachedTokens:    usage.GetCachedTokens(),
		ReasoningTokens: usage.GetReasoningTokens(),
	}
	detail := map[string]any{
		"usage_present":    true,
		"input_tokens":     u.InputTokens,
		"output_tokens":    u.OutputTokens,
		"total_tokens":     u.TotalTokens,
		"cached_tokens":    u.CachedTokens,
		"reasoning_tokens": u.ReasoningTokens,
	}
	return u, detail
}

func summarizeMedia(media *modelhubv2.Media) map[string]any {
	if media == nil {
		return map[string]any{"missing": true}
	}
	out := map[string]any{
		"mime_type":       media.GetMimeType(),
		"content_omitted": true,
	}
	switch source := media.GetSource().(type) {
	case *modelhubv2.Media_Data:
		out["source"] = "inline"
		out["byte_size"] = len(source.Data)
	case *modelhubv2.Media_Uri:
		out["source"] = "uri"
		out["uri"] = source.Uri
	default:
		out["source"] = "unspecified"
	}
	return out
}

func redactGenerateRequestMedia(req *modelhubv2.GenerateRequest) {
	if req == nil || req.GetInput() == nil {
		return
	}
	for _, item := range req.GetInput().GetItems() {
		switch value := item.GetItem().(type) {
		case *modelhubv2.InputItem_Message:
			for _, part := range value.Message.GetParts() {
				redactContentPart(part)
			}
		case *modelhubv2.InputItem_ToolOutput:
			for i, image := range value.ToolOutput.GetImages() {
				value.ToolOutput.Images[i] = mediaSummaryAsPlaceholder(image)
			}
		}
	}
}

func redactContentPart(part *modelhubv2.ContentPart) {
	if part == nil {
		return
	}
	switch value := part.GetContent().(type) {
	case *modelhubv2.ContentPart_Image:
		part.Content = &modelhubv2.ContentPart_Image{Image: mediaSummaryAsPlaceholder(value.Image)}
	case *modelhubv2.ContentPart_Video:
		part.Content = &modelhubv2.ContentPart_Video{Video: mediaSummaryAsPlaceholder(value.Video)}
	case *modelhubv2.ContentPart_Audio:
		part.Content = &modelhubv2.ContentPart_Audio{Audio: mediaSummaryAsPlaceholder(value.Audio)}
	case *modelhubv2.ContentPart_File:
		part.Content = &modelhubv2.ContentPart_File{File: mediaSummaryAsPlaceholder(value.File)}
	}
}

// mediaSummaryAsPlaceholder 用短 URI 占位保留 mime/长度，避免把内联字节写入 JSONB。
func mediaSummaryAsPlaceholder(media *modelhubv2.Media) *modelhubv2.Media {
	if media == nil {
		return nil
	}
	summary := summarizeMedia(media)
	uri := "ledger://omitted"
	if src, ok := summary["source"].(string); ok {
		uri = "ledger://omitted/" + src
	}
	if n, ok := summary["byte_size"].(int); ok {
		uri += fmt.Sprintf("?bytes=%d", n)
	}
	if u, ok := summary["uri"].(string); ok && u != "" {
		uri = u
	}
	return &modelhubv2.Media{
		MimeType: media.GetMimeType(),
		Source:   &modelhubv2.Media_Uri{Uri: uri},
	}
}

func protoToMap(msg proto.Message) map[string]any {
	raw, err := protoJSON.Marshal(msg)
	if err != nil {
		return map[string]any{"marshal_error": err.Error()}
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{"raw": string(raw), "decode_error": err.Error()}
	}
	return out
}

func sanitizeErrorMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	msg = bearerSecretPattern.ReplaceAllString(msg, "Bearer [redacted]")
	msg = authHeaderPattern.ReplaceAllString(msg, "${1}[redacted]")
	msg = apiKeySecretPattern.ReplaceAllString(msg, "${1}[redacted]")
	if utf8.RuneCountInString(msg) <= maxErrorMessage {
		return msg
	}
	return string([]rune(msg)[:maxErrorMessage]) + "…"
}
