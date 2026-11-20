package genai

import (
	"encoding/json"
	sdk "google.golang.org/genai"
	"testing"
)

func TestLiveEventPreservesConcurrentFields(t *testing.T) {
	event := liveEvent(&sdk.LiveServerMessage{
		ServerContent:        &sdk.LiveServerContent{InputTranscription: &sdk.Transcription{Text: "找粉色裙子"}, OutputTranscription: &sdk.Transcription{Text: "不应直接播放"}, ModelTurn: &sdk.Content{Parts: []*sdk.Part{{InlineData: &sdk.Blob{MIMEType: "audio/pcm;rate=24000", Data: []byte{1, 2}}}}}, TurnComplete: true, Interrupted: true},
		ToolCall:             &sdk.LiveServerToolCall{FunctionCalls: []*sdk.FunctionCall{{ID: "c1", Name: "closet_search", Args: map[string]any{"color": []string{"粉色"}}}}},
		ToolCallCancellation: &sdk.LiveServerToolCallCancellation{IDs: []string{"old"}},
	})
	if event.InputTranscript.Text != "找粉色裙子" || !event.InputTranscript.Final || len(event.Audio) != 1 || len(event.ToolCalls) != 1 || !event.Interrupted || !event.TurnComplete || len(event.CancelledCallIds) != 1 {
		t.Fatalf("lost concurrent fields: %+v", event)
	}
}

// 供应商使用 type 和毫秒偏移，不能因 SDK 字段名差异丢失原生讲话边界。
func TestLiveNativeActivityWireMapping(t *testing.T) {
	var m sdk.LiveServerMessage
	if err := json.Unmarshal([]byte(`{"voiceActivity":{"type":"ACTIVITY_START","audioOffset":"4.760s"}}`), &m); err != nil {
		t.Fatal(err)
	}
	event := liveEvent(&m)
	if event.VoiceActivity == nil || event.VoiceActivity.Type != 1 || event.VoiceActivity.AudioOffsetMs != 4760 {
		t.Fatalf("lost activity: %+v", event)
	}
}
