package provider

import (
	"io"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/protocol"
)

// EmitVideoChunksFromReader 从上游 HTTP Body 等 Reader 按块流式 emit，不缓冲整段视频。
// 预读一块以确定末块 Final=true；累计字节为 0 或超过 MaxVideoBytes 时立即失败。
func EmitVideoChunksFromReader(r io.Reader, mimeType, responseID string, generationElapsedMS int64, emit EmitEvent) error {
	if mimeType == "" {
		mimeType = "video/mp4"
	}
	buf := make([]byte, protocol.VideoChunkBytes)
	var total int64
	var sequence uint32

	readBlock := func() ([]byte, error) {
		n, err := io.ReadFull(r, buf)
		switch {
		case err == io.EOF && n == 0:
			return nil, io.EOF
		case err == io.EOF || err == io.ErrUnexpectedEOF:
			if n == 0 {
				return nil, io.EOF
			}
			block := append([]byte(nil), buf[:n]...)
			total += int64(len(block))
			if total > protocol.MaxVideoBytes {
				return nil, Errorf(ErrorInvalidResponse, "video exceeds %d bytes", protocol.MaxVideoBytes)
			}
			return block, io.EOF
		case err != nil:
			return nil, Wrap(ErrorUnavailable, "read video stream", err)
		default:
			block := append([]byte(nil), buf[:n]...)
			total += int64(len(block))
			if total > protocol.MaxVideoBytes {
				return nil, Errorf(ErrorInvalidResponse, "video exceeds %d bytes", protocol.MaxVideoBytes)
			}
			return block, nil
		}
	}

	ahead, err := readBlock()
	if err == io.EOF {
		if len(ahead) == 0 {
			return New(ErrorInvalidResponse, "video download returned 0 bytes")
		}
		if emit == nil {
			return nil
		}
		return emitVideoBlock(ahead, mimeType, responseID, generationElapsedMS, 0, true, emit)
	}
	if err != nil {
		return err
	}

	for {
		next, err := readBlock()
		if err == io.EOF {
			if emit != nil {
				if len(next) == 0 {
					if err := emitVideoBlock(ahead, mimeType, responseID, generationElapsedMS, sequence, true, emit); err != nil {
						return err
					}
				} else {
					if err := emitVideoBlock(ahead, mimeType, responseID, generationElapsedMS, sequence, false, emit); err != nil {
						return err
					}
					if err := emitVideoBlock(next, mimeType, responseID, generationElapsedMS, sequence+1, true, emit); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err != nil {
			return err
		}
		if emit != nil {
			if err := emitVideoBlock(ahead, mimeType, responseID, generationElapsedMS, sequence, false, emit); err != nil {
				return err
			}
		}
		sequence++
		ahead = next
	}
}

func emitVideoBlock(data []byte, mimeType, responseID string, generationElapsedMS int64, sequence uint32, final bool, emit EmitEvent) error {
	chunk := &modelhubv2.GenerateEvent{
		Sequence: sequence,
		Final:    final,
		Items: []*modelhubv2.OutputItem{{Item: &modelhubv2.OutputItem_Video{Video: &modelhubv2.Media{
			MimeType: mimeType,
			Source:   &modelhubv2.Media_Data{Data: data},
		}}}},
	}
	if final {
		chunk.ResponseId = responseID
		chunk.GenerationElapsedMs = generationElapsedMS
	}
	return emit(chunk)
}
