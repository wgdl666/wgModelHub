package ominilinkvideo

import (
	"encoding/base64"
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/protocol"
)

// 内联图片直接生成模型所需的 Base64，不先构造 Data URI 再解码。
func inlineImageBase64(media *modelhubv2.Media) (string, string, error) {
	data, ok := provider.InlineImageBytes(media)
	if !ok {
		return "", "", provider.NotAttempted(provider.ErrorInvalidArgument, "image source is required")
	}
	if len(data) > protocol.MaxMediaBytes {
		return "", "", provider.NotAttemptedf(provider.ErrorInvalidArgument, "image exceeds %d bytes", protocol.MaxMediaBytes)
	}
	mimeType := strings.ToLower(strings.TrimSpace(media.GetMimeType()))
	if mimeType == "" {
		return "", "", provider.NotAttempted(provider.ErrorInvalidArgument, "first_frame mime_type is required")
	}
	if !strings.HasPrefix(mimeType, "image/") {
		return "", "", provider.NotAttemptedf(provider.ErrorInvalidArgument, "first_frame mime_type %s is not an image", mimeType)
	}
	return base64.StdEncoding.EncodeToString(data), mimeType, nil
}
