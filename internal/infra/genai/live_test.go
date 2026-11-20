package genai

import (
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
