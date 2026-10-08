package elevenlabstts

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
	"github.com/wgdl666/wgModelHub/protocol"
)

func TestNewRequiresAPIKeyAndVoice(t *testing.T) {
	if _, err := New(Config{Name: "eleven", VoiceID: "v"}); err == nil {
		t.Fatal("expected api_key error")
	}
	if _, err := New(Config{Name: "eleven", APIKey: "k"}); err == nil {
		t.Fatal("expected voice_id error")
	}
}

func TestSynthesizeSpeechSuccessReturnsFullAudio(t *testing.T) {
	var gotModel, gotVoice, gotKey, gotFormat string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("xi-api-key")
		gotFormat = r.URL.Query().Get("output_format")
		gotVoice = strings.TrimPrefix(r.URL.Path, "/v1/text-to-speech/")
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), models.ElevenFlashV25) {
			gotModel = models.ElevenFlashV25
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3fake-mp3"))
	}))
	defer srv.Close()

	p, err := New(Config{Name: "eleven", APIKey: "k", BaseURL: srv.URL, VoiceID: "default-voice"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.SynthesizeSpeech(context.Background(), models.ElevenFlashV25, &modelhubv2.SynthesizeSpeechRequest{
		Text:    "你好",
		VoiceId: "req-voice",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.GetAudio().GetData()) != "ID3fake-mp3" || resp.GetAudio().GetMimeType() != "audio/pcm;rate=16000" {
		t.Fatalf("audio=%q mime=%q", resp.GetAudio().GetData(), resp.GetAudio().GetMimeType())
	}
	if gotKey != "k" || gotVoice != "req-voice" || gotModel != models.ElevenFlashV25 || gotFormat != defaultOutputFormat {
		t.Fatalf("upstream key=%q voice=%q model=%q format=%q", gotKey, gotVoice, gotModel, gotFormat)
	}
}

func TestSynthesizeSpeechSendsConfiguredVoiceSettings(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		_, _ = w.Write([]byte("ID3ok"))
	}))
	defer srv.Close()

	stability, similarity, style, speed := 0.30, 0.30, 0.50, 0.90
	p, err := New(Config{
		Name:    "eleven",
		APIKey:  "k",
		BaseURL: srv.URL,
		VoiceID: "v",
		VoiceSettings: VoiceSettings{
			Stability:       &stability,
			SimilarityBoost: &similarity,
			Style:           &style,
			Speed:           &speed,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.SynthesizeSpeech(context.Background(), models.ElevenFlashV25, &modelhubv2.SynthesizeSpeechRequest{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"voice_settings"`,
		`"stability":0.3`,
		`"similarity_boost":0.3`,
		`"style":0.5`,
		`"speed":0.9`,
	} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("body missing %s: %s", want, gotBody)
		}
	}
}

func TestSynthesizeSpeechOmitsVoiceSettingsWhenUnset(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		_, _ = w.Write([]byte("ID3ok"))
	}))
	defer srv.Close()

	p, err := New(Config{Name: "eleven", APIKey: "k", BaseURL: srv.URL, VoiceID: "v"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.SynthesizeSpeech(context.Background(), models.ElevenFlashV25, &modelhubv2.SynthesizeSpeechRequest{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotBody, "voice_settings") {
		t.Fatalf("unexpected voice_settings in %s", gotBody)
	}
}

func TestSynthesizeSpeechUsesConfiguredVoiceWhenRequestEmpty(t *testing.T) {
	var gotVoice string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVoice = strings.TrimPrefix(r.URL.Path, "/v1/text-to-speech/")
		_, _ = w.Write([]byte("ID3ok"))
	}))
	defer srv.Close()

	p, err := New(Config{Name: "eleven", APIKey: "k", BaseURL: srv.URL, VoiceID: "cfg-voice"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.SynthesizeSpeech(context.Background(), models.ElevenFlashV25, &modelhubv2.SynthesizeSpeechRequest{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if gotVoice != "cfg-voice" {
		t.Fatalf("voice=%q", gotVoice)
	}
}

func TestSynthesizeSpeechRejectsEnglishOnlyModel(t *testing.T) {
	p, err := New(Config{Name: "eleven", APIKey: "k", BaseURL: "http://127.0.0.1:1", VoiceID: "v"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.SynthesizeSpeech(context.Background(), "eleven_monolingual_v1", &modelhubv2.SynthesizeSpeechRequest{Text: "hi"})
	if err == nil {
		t.Fatal("expected unsupported model error")
	}
}

func TestSynthesizeSpeechRejectsEmptyAndOverlongText(t *testing.T) {
	p, err := New(Config{Name: "eleven", APIKey: "k", BaseURL: "http://127.0.0.1:1", VoiceID: "v"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.SynthesizeSpeech(context.Background(), models.ElevenFlashV25, &modelhubv2.SynthesizeSpeechRequest{Text: "  "})
	if err == nil {
		t.Fatal("expected empty text error")
	}
	long := strings.Repeat("字", protocol.MaxSpeechTextChars)
	_, err = p.SynthesizeSpeech(context.Background(), models.ElevenFlashV25, &modelhubv2.SynthesizeSpeechRequest{Text: long})
	if err == nil {
		t.Fatal("expected overlong text error")
	}
}

func TestSynthesizeSpeechUpstreamErrorNotPartialSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"no"}`))
	}))
	defer srv.Close()

	p, err := New(Config{Name: "eleven", APIKey: "k", BaseURL: srv.URL, VoiceID: "v"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.SynthesizeSpeech(context.Background(), models.ElevenFlashV25, &modelhubv2.SynthesizeSpeechRequest{Text: "hi"})
	if err == nil || resp != nil {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
}
