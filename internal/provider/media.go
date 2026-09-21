package provider

import (
	"encoding/base64"
	"fmt"
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/protocol"
)

// ResolveImageURL 把首帧变成供应商 image_url 字段：已有公网 URI 原样下发，内联字节编成 data URI。
func ResolveImageURL(media *modelhubv2.Media) (string, error) {
	if media == nil {
		return "", New(ErrorInvalidArgument, "image is required in input")
	}
	if uri := MediaURI(media); uri != "" {
		return uri, nil
	}
	data, ok := media.Source.(*modelhubv2.Media_Data)
	if !ok || len(data.Data) == 0 {
		return "", New(ErrorInvalidArgument, "image source is required")
	}
	if len(data.Data) > protocol.MaxMediaBytes {
		return "", Errorf(ErrorInvalidArgument, "image exceeds %d bytes", protocol.MaxMediaBytes)
	}
	return ImageDataURI(media.GetMimeType(), data.Data)
}

// ImageDataURI 把内联图片编成 data:{mime};base64,...，供方舟 / 万相 / Vidu / Dreamina 的 url 字段。
func ImageDataURI(mimeType string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", New(ErrorInvalidArgument, "image data is empty")
	}
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if mimeType == "" {
		return "", New(ErrorInvalidArgument, "first_frame mime_type is required")
	}
	if !strings.HasPrefix(mimeType, "image/") {
		return "", Errorf(ErrorInvalidArgument, "first_frame mime_type %s is not an image", mimeType)
	}
	return fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(data)), nil
}

// ParseDataURI 解析 data:{mime};base64,{payload}。不是 data URI 时返回 InvalidArgument。
func ParseDataURI(value string) (mimeType string, data []byte, err error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "data:") {
		return "", nil, New(ErrorInvalidArgument, "not a data URI")
	}
	rest := strings.TrimPrefix(value, "data:")
	meta, payload, ok := strings.Cut(rest, ",")
	if !ok {
		return "", nil, New(ErrorInvalidArgument, "invalid data URI")
	}
	mimeType = strings.TrimSuffix(strings.TrimSpace(meta), ";base64")
	data, err = base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, Wrap(ErrorInvalidArgument, "decode data URI", err)
	}
	if len(data) == 0 {
		return "", nil, New(ErrorInvalidArgument, "data URI payload is empty")
	}
	return mimeType, data, nil
}
