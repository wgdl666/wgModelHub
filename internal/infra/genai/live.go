package genai

import (
	"context"
	"strings"
	"sync"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	sdk "google.golang.org/genai"
)

// liveSession 每次 RPC 独占；关闭会解除 Receive，不能让取消后的供应商连接继续计费。
type liveSession struct {
	session  *sdk.Session
	once     sync.Once
	closeErr error
}

func (p *Provider) OpenLive(ctx context.Context, model string, start *modelhubv2.LiveStart) (provider.LiveSession, error) {
	if model != models.Gemini38Live {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "Live supports gemini-3.8-live")
	}
	request := &modelhubv2.GenerateRequest{Input: start.Input}
	cfg := &sdk.LiveConnectConfig{
		ResponseModalities:       []sdk.Modality{sdk.ModalityAudio},
		SystemInstruction:        p.buildConfig(model, request).SystemInstruction,
		Tools:                    buildTools(start.Input.GetTools()),
		InputAudioTranscription:  &sdk.AudioTranscriptionConfig{},
		OutputAudioTranscription: &sdk.AudioTranscriptionConfig{},
		RealtimeInputConfig:      &sdk.RealtimeInputConfig{AutomaticActivityDetection: &sdk.AutomaticActivityDetection{Disabled: true}},
	}
	for _, t := range cfg.Tools {
		for _, d := range t.FunctionDeclarations {
			d.Behavior = sdk.BehaviorBlocking
		}
	}
	if start.Voice != "" {
		cfg.SpeechConfig = &sdk.SpeechConfig{VoiceConfig: &sdk.VoiceConfig{PrebuiltVoiceConfig: &sdk.PrebuiltVoiceConfig{VoiceName: start.Voice}}}
	}
	session, err := p.liveClient.Live.Connect(ctx, model, cfg)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "connect Gemini Live", err)
	}
	l := &liveSession{session: session}
	context.AfterFunc(ctx, func() { _ = l.Close() })
	history := p.buildContents(request)
	if len(history) > 0 {
		if err := session.SendClientContent(sdk.LiveClientContentInput{Turns: history, TurnComplete: sdk.Ptr(false)}); err != nil {
			_ = l.Close()
			return nil, provider.Wrap(provider.ErrorUnavailable, "seed Live history", err)
		}
	}
	return l, nil
}
func (l *liveSession) Close() error {
	l.once.Do(func() { l.closeErr = l.session.Close() })
	return l.closeErr
}
func (l *liveSession) Send(msg *modelhubv2.LiveClientMessage) error {
	var err error
	switch v := msg.GetItem().(type) {
	case *modelhubv2.LiveClientMessage_Pcm:
		if len(v.Pcm) == 0 || len(v.Pcm)%2 != 0 {
			return provider.New(provider.ErrorInvalidArgument, "Live PCM must contain complete PCM16 samples")
		}
		err = l.session.SendRealtimeInput(sdk.LiveRealtimeInput{Audio: &sdk.Blob{Data: v.Pcm, MIMEType: "audio/pcm;rate=16000"}})
	case *modelhubv2.LiveClientMessage_ActivityStart:
		err = l.session.SendRealtimeInput(sdk.LiveRealtimeInput{ActivityStart: &sdk.ActivityStart{}})
	case *modelhubv2.LiveClientMessage_ActivityEnd:
		err = l.session.SendRealtimeInput(sdk.LiveRealtimeInput{ActivityEnd: &sdk.ActivityEnd{}})
	case *modelhubv2.LiveClientMessage_Text:
		if strings.TrimSpace(v.Text) == "" {
			return provider.New(provider.ErrorInvalidArgument, "Live text is empty")
		}
		err = l.session.SendClientContent(sdk.LiveClientContentInput{Turns: []*sdk.Content{sdk.NewContentFromText(v.Text, sdk.RoleUser)}, TurnComplete: sdk.Ptr(true)})
	case *modelhubv2.LiveClientMessage_ToolResponse:
		if v.ToolResponse.GetToolCallId() == "" || v.ToolResponse.GetToolName() == "" {
			return provider.New(provider.ErrorInvalidArgument, "Live tool response requires call ID and name")
		}
		err = l.session.SendToolResponse(sdk.LiveToolResponseInput{FunctionResponses: []*sdk.FunctionResponse{{ID: v.ToolResponse.ToolCallId, Name: v.ToolResponse.ToolName, Response: toolOutputObject(v.ToolResponse)}}})
	default:
		return provider.New(provider.ErrorInvalidArgument, "Live start is only allowed as first message")
	}
	if err != nil {
		return provider.Wrap(provider.ErrorUnavailable, "send Gemini Live input", err)
	}
	return nil
}
func (l *liveSession) Receive() (*modelhubv2.LiveServerMessage, error) {
	m, err := l.session.Receive()
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "receive Gemini Live", err)
	}
	return liveEvent(m), nil
}

// liveEvent 不用互斥分支，避免一个消息含音频、转写、工具时丢字段。
func liveEvent(m *sdk.LiveServerMessage) *modelhubv2.LiveServerMessage {
	out := &modelhubv2.LiveServerMessage{Ready: m.SetupComplete != nil}
	if s := m.ServerContent; s != nil {
		out.GenerationComplete = s.GenerationComplete
		out.TurnComplete = s.TurnComplete
		out.Interrupted = s.Interrupted
		// Gemini 3.8 的 inputTranscription 是最终通道；finished 可省略，预览只来自 interimInputTranscription。
		if t := s.InputTranscription; t != nil {
			out.InputTranscript = &modelhubv2.LiveTranscript{Text: t.Text, Final: true}
		}
		if t := s.InterimInputTranscription; t != nil && out.InputTranscript == nil {
			out.InputTranscript = &modelhubv2.LiveTranscript{Text: t.Text}
		}
		if t := s.OutputTranscription; t != nil {
			out.OutputTranscript = &modelhubv2.LiveTranscript{Text: t.Text, Final: t.Finished}
		}
		if s.ModelTurn != nil {
			for _, p := range s.ModelTurn.Parts {
				if b := p.InlineData; b != nil && strings.HasPrefix(b.MIMEType, "audio/") {
					out.Audio = append(out.Audio, &modelhubv2.Media{MimeType: b.MIMEType, Source: &modelhubv2.Media_Data{Data: b.Data}})
				}
			}
		}
	}
	if m.ToolCall != nil {
		for _, c := range m.ToolCall.FunctionCalls {
			out.ToolCalls = append(out.ToolCalls, convertFunctionCall(&sdk.Part{FunctionCall: c}))
		}
	}
	if m.ToolCallCancellation != nil {
		out.CancelledCallIds = m.ToolCallCancellation.IDs
	}
	return out
}
