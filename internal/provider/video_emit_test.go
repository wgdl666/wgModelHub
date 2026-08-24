package provider

import (
	"bytes"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/protocol"
)

func TestEmitVideoChunksFromReaderRejectsEmpty(t *testing.T) {
	err := EmitVideoChunksFromReader(bytes.NewReader(nil), "video/mp4", "", 0, nil)
	if err == nil || !strings.Contains(err.Error(), "0 bytes") {
		t.Fatalf("err=%v", err)
	}
}

func TestEmitVideoChunksFromReaderRejectsOverMax(t *testing.T) {
	payload := make([]byte, protocol.MaxVideoBytes+1)
	err := EmitVideoChunksFromReader(bytes.NewReader(payload), "video/mp4", "", 0, nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err=%v", err)
	}
}

func TestEmitVideoChunksFromReaderMultiChunk(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), protocol.VideoChunkBytes+50)
	var chunks []*modelhubv2.GenerateEvent
	err := EmitVideoChunksFromReader(bytes.NewReader(payload), "video/mp4", "rid", 99, func(chunk *modelhubv2.GenerateEvent) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks=%d", len(chunks))
	}
	if chunks[0].GetFinal() || !chunks[1].GetFinal() {
		t.Fatalf("final flags %#v %#v", chunks[0].GetFinal(), chunks[1].GetFinal())
	}
	if chunks[1].GetResponseId() != "rid" || chunks[1].GetGenerationElapsedMs() != 99 {
		t.Fatalf("metadata %#v", chunks[1])
	}
}

func TestEmitVideoChunksFromReaderSingleChunk(t *testing.T) {
	payload := []byte("small")
	var got *modelhubv2.GenerateEvent
	if err := EmitVideoChunksFromReader(bytes.NewReader(payload), "video/mp4", "id-1", 0, func(chunk *modelhubv2.GenerateEvent) error {
		got = chunk
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.GetFinal() || got.GetResponseId() != "id-1" {
		t.Fatalf("got=%#v", got)
	}
}
