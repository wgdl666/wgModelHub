// Package vmindgateway 定义 ModelHub 与模型网关共享的 JSON 请求，不暴露后端表单字段或路径。
package vmindgateway

// Image 保留原始图片字节，JSON 编码自动使用 base64，不缩放或重编码图片。
type Image struct {
	MIMEType string `json:"mime_type"`
	Data     []byte `json:"data"`
}

type Metadata struct {
	UserID  string `json:"user_id,omitempty"`
	TraceID string `json:"trace_id,omitempty"`
}

// Request 的 Input 使用各模型的业务字段，图片按契约顺序放入 images。
type Request struct {
	Input    map[string]any `json:"input"`
	Metadata Metadata       `json:"metadata"`
}
