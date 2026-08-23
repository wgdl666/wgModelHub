package provider

import (
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/protocol"
)

// EmitVideoChunks 按 protocol.VideoChunkBytes 顺序分块；仅末块 final=true 并携带 response_id 与 generation_elapsed_ms。
// 0 字节与超过 MaxVideoBytes 必须先于 emit==nil 判定失败，避免空载荷或超限下载被当成成功。
func EmitVideoChunks(data []byte, mimeType, responseID string, generationElapsedMS int64, emit EmitEvent) error {
	if len(data) == 0 {
		return New(ErrorInvalidResponse, "video download returned 0 bytes")
	}
	if len(data) > protocol.MaxVideoBytes {
		return Errorf(ErrorInvalidResponse, "video exceeds %d bytes", protocol.MaxVideoBytes)
	}
	if mimeType == "" {
		mimeType = "video/mp4"
	}
	if emit == nil {
		return nil
	}
	var sequence uint32
	for offset := 0; offset < len(data); offset += protocol.VideoChunkBytes {
		end := offset + protocol.VideoChunkBytes
		if end > len(data) {
			end = len(data)
		}
		final := end == len(data)
		chunk := &modelhubv2.GenerateEvent{
			Sequence: sequence,
			Final:    final,
			Items: []*modelhubv2.OutputItem{{Item: &modelhubv2.OutputItem_Video{Video: &modelhubv2.Media{
				MimeType: mimeType,
				Source:   &modelhubv2.Media_Data{Data: append([]byte(nil), data[offset:end]...)},
			}}}},
		}
		if final {
			chunk.ResponseId = responseID
			chunk.GenerationElapsedMs = generationElapsedMS
		}
		if err := emit(chunk); err != nil {
			return err
		}
		sequence++
	}
	return nil
}
