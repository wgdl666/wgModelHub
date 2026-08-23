package provider

import (
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/protocol"
)

func TestEmitVideoChunksFinalMetadata(t *testing.T) {
	payload := []byte("video-data")
	var final *modelhubv2.GenerateEvent
	if err := EmitVideoChunks(payload, "video/mp4", "task-42", 1500, func(chunk *modelhubv2.GenerateEvent) error {
		if chunk.GetFinal() {
			final = chunk
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if final == nil || !final.GetFinal() {
		t.Fatalf("expected single final chunk")
	}
	if final.GetResponseId() != "task-42" || final.GetGenerationElapsedMs() != 1500 {
		t.Fatalf("final metadata=%q elapsed=%d", final.GetResponseId(), final.GetGenerationElapsedMs())
	}
}

func TestEmitVideoChunksRejectsEmpty(t *testing.T) {
	err := EmitVideoChunks(nil, "video/mp4", "", 0, nil)
	if err == nil {
		t.Fatal("expected error for empty download")
	}
}

func TestEmitVideoChunksSequence(t *testing.T) {
	payload := make([]byte, protocol.VideoChunkBytes+100)
	var chunks []*modelhubv2.GenerateEvent
	if err := EmitVideoChunks(payload, "video/mp4", "id", 0, func(chunk *modelhubv2.GenerateEvent) error {
		chunks = append(chunks, chunk)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 || chunks[0].Sequence != 0 || chunks[1].Sequence != 1 {
		t.Fatalf("chunks=%d", len(chunks))
	}
	if chunks[0].Final || !chunks[1].Final {
		t.Fatalf("final flags wrong")
	}
}
